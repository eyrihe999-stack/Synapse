// ingest_service.go 同步主流程实现。
//
// 流程(per user):
//   1. ListRepos → 拿 PAT 可见的 repo 快照
//   2. for each repo:
//      - Upsert code_repositories 元信息
//      - ListFiles → 拉 repo 当前文件树(已过滤)
//      - Diff:本地 DB 现有 files vs 源端 files,分四类:unchanged / changed / created / deleted
//      - 处理 changed + created:FetchFile → chunk → embed → swap chunks
//      - 处理 deleted:DeleteChunksByFileID + DeleteByFileID
//      - UpdateLastSynced
//
// 错误处理粒度:
//   - 致命错(PAT 无效 / embed auth 错) → 整轮立刻终止,返 error
//   - 单 repo 错误 → 记录 FailedRepos,继续别的 repo
//   - 单 file 错误 → 记录 FailedFiles,继续别的 file
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pgvector/pgvector-go"

	ajsvc "github.com/eyrihe999-stack/Synapse/internal/asyncjob/service"
	"github.com/eyrihe999-stack/Synapse/internal/code"
	"github.com/eyrihe999-stack/Synapse/internal/code/model"
	"github.com/eyrihe999-stack/Synapse/internal/code/source"
	"github.com/eyrihe999-stack/Synapse/pkg/codechunker"
	"github.com/eyrihe999-stack/Synapse/pkg/embedding"
)

// embedTimeout 单文件 embed 调用的超时。太短会让中等文件跑不完,太长会拖慢整轮。
// 60s 是经验值:30 chunks 走 Azure 一次 batch 调用通常 2-5s,留量余。
const embedTimeout = 60 * time.Second

// embedInputMaxBytes 单条 embed 输入的字节硬上限。chunker 已按 MaxChunkBytes(8KB)切分,
// 这里也设 8KB 是给"单行超 8KB 的病态文件 / 中文注释密集的 preamble"兜底。
//
// 为什么不是 16KB:中文 UTF-8 字符 3 字节 ≈ 1.5-2 tokens,16KB 中文 ≈ 10-11K tokens 会炸 8192 限制
// (实测 sayso-server 同步时触发)。8KB 字节在三种输入下的 tokens 近似:
//   - 纯 ASCII 代码:~2K tokens(充足余量)
//   - 中英混合:~3-4K tokens
//   - 纯中文注释:~5-6K tokens(worst case 仍安全)
//
// 超限时 truncate 只用于 embed 输入;chunk 的 Content 字段仍存全量原文,检索回带完整代码。
const embedInputMaxBytes = 8 * 1024

// truncateUTF8 按 utf-8 rune 边界截到不超过 maxBytes 字节。中文/emoji 等多字节字符不会被切一半
// 导致 embedder 反序列化报 invalid utf-8。
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// 回退到一个 rune 起始字节(utf-8 续字节的高 2 位是 10)
	for maxBytes > 0 && (s[maxBytes]&0xC0) == 0x80 {
		maxBytes--
	}
	return s[:maxBytes]
}

// repoPlan 一个 repo 的处理计划,Phase 1 产出,Phase 2 消费。
//
// 拆两阶段的动机:把进度条从 "repo 粒度"提升到 "文件粒度"。
// Phase 1 一口气把所有 repo 的文件树扫完 + 算 diff,得到 "这次总共要处理多少个文件",
// 再 SetTotal(总文件数),Phase 2 按 file 推进度。
// 用户体验:扫描阶段前端展示 "正在扫描..." → 进入处理阶段进度条以文件为单位平滑推进。
type repoPlan struct {
	repo      source.RepoSnapshot
	repoID    uint64
	toProcess []source.FileEntry // 要 fetch+embed 的文件(新增 / blob_sha 变化)
	toDelete  []*model.CodeFile  // 要本地清除的文件(源端已消失)
}

// SyncUser 见接口注释。两阶段执行:
//
//	Phase 1(扫描):为每个 repo upsert 元信息 + 拉源端 tree + 和 DB 对比算出 plan。
//	             汇总 Σ|toProcess| = 总文件处理量,SetTotal 交给 reporter。此阶段进度 0/?,前端显示 "扫描中..."。
//	Phase 2(处理):按 plan 遍历文件 fetch+embed+persist,每完成一个文件 reporter.Inc(1,0)。
//	             前端进度条以文件为粒度平滑推进。
//
// 为什么不边扫描边处理:若边扫边处理,SetTotal 只能一开始拍脑袋(如 repo 数),无法准确反映
// "实际要处理多少个文件"。对一个大 repo,同一 repo 内处理几百个 file 期间进度条卡在 1/N,
// 是之前版本"卡 0%"的根本原因。
func (s *ingestService) SyncUser(ctx context.Context, src source.Source, orgID, userID uint64, reporter ajsvc.ProgressReporter) (*SyncResult, error) {
	if reporter == nil {
		reporter = noopReporter{}
	}
	result := &SyncResult{}

	// ─── Phase 1: 扫描 + 算 plan ─────────────────────────────────────────
	repos, err := src.ListRepos(ctx)
	if err != nil {
		return nil, fmt.Errorf("code ingest: list repos: %w", err)
	}
	result.ReposTotal = len(repos)

	plans := make([]*repoPlan, 0, len(repos))
	totalFiles := 0
	for _, repo := range repos {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		plan, err := s.buildRepoPlan(ctx, src, orgID, repo)
		if err != nil {
			if isFatalError(err) {
				return result, err
			}
			result.ReposFailed++
			result.FailedRepos = append(result.FailedRepos, FailedItem{
				Ref:   repo.PathWithNamespace,
				Error: unwrapFirst(err),
			})
			s.deps.Log.WarnCtx(ctx, "code ingest: plan failed", map[string]any{
				"org_id": orgID, "user_id": userID, "repo": repo.PathWithNamespace, "err": err.Error(),
			})
			continue
		}
		if plan == nil {
			// repo 不可访问(ErrRepoUnavailable / archived 等)→ 跳过,不算失败
			result.ReposSkipped++
			continue
		}
		plans = append(plans, plan)
		totalFiles += len(plan.toProcess)
	}

	// 至此所有 plan 算完 —— SetTotal 反映的是"真正要 fetch+embed 的 file 数",
	// 已 skip(blob_sha 未变)的文件不计入总量,用户体感的进度条就是"实际工作量"。
	_ = reporter.SetTotal(totalFiles)

	// ─── Phase 2: 按 plan 执行 ───────────────────────────────────────────
	for _, plan := range plans {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := s.executePlan(ctx, src, orgID, plan, reporter, result); err != nil {
			if isFatalError(err) {
				return result, err
			}
			// 非致命:记 repo 级失败,继续下一个 repo
			result.ReposFailed++
			result.FailedRepos = append(result.FailedRepos, FailedItem{
				Ref:   plan.repo.PathWithNamespace,
				Error: unwrapFirst(err),
			})
			s.deps.Log.WarnCtx(ctx, "code ingest: execute failed", map[string]any{
				"org_id": orgID, "user_id": userID, "repo": plan.repo.PathWithNamespace, "err": err.Error(),
			})
			continue
		}
		result.ReposSynced++
	}

	result.LastSyncAt = time.Now().Unix()
	return result, nil
}

// buildRepoPlan Phase 1 per-repo:upsert 元信息 + 拉源端 tree + 和 DB 对比。
//
// 返回:
//   - (plan, nil):成功,plan.toProcess + plan.toDelete 可能为空(repo 没变更)
//   - (nil, nil):repo 不可访问(ErrRepoUnavailable)或类似可跳过的情况
//   - (nil, err):致命或非致命错误,调用方按 isFatalError 分派
func (s *ingestService) buildRepoPlan(ctx context.Context, src source.Source, orgID uint64, repo source.RepoSnapshot) (*repoPlan, error) {
	// 1. upsert code_repositories
	repoRow := &model.CodeRepository{
		OrgID:             orgID,
		Provider:          src.Provider(),
		ExternalProjectID: repo.ExternalProjectID,
		PathWithNamespace: repo.PathWithNamespace,
		DefaultBranch:     repo.DefaultBranch,
		WebURL:            repo.WebURL,
		Archived:          false,
	}
	if err := s.deps.Repo.Repos().Upsert(ctx, repoRow); err != nil {
		return nil, fmt.Errorf("upsert repo: %w", err)
	}

	// 用 upsert 写回的 id 查一次拿 ID —— GORM Upsert 在 ON CONFLICT 分支不会把 id 回填,
	// 必须主动查一次(简单,一次 index lookup)。
	saved, err := s.deps.Repo.Repos().GetByExternalID(ctx, orgID, src.Provider(), repo.ExternalProjectID)
	if err != nil {
		return nil, fmt.Errorf("load saved repo: %w", err)
	}
	if saved == nil {
		return nil, fmt.Errorf("upsert succeeded but repo not found")
	}
	repoID := saved.ID

	// 2. 拉源端 tree
	entries, err := src.ListFiles(ctx, repo)
	if err != nil {
		if errors.Is(err, source.ErrRepoUnavailable) {
			return nil, nil // 跳过信号
		}
		return nil, fmt.Errorf("list files: %w", err)
	}

	// 3. 拉 DB 现有 files(不含 content)
	existing, err := s.deps.Repo.Files().ListByRepoID(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("list existing files: %w", err)
	}
	existingByPath := make(map[string]*model.CodeFile, len(existing))
	for _, f := range existing {
		existingByPath[f.Path] = f
	}
	sourceByPath := make(map[string]source.FileEntry, len(entries))
	for _, e := range entries {
		sourceByPath[e.Path] = e
	}

	// 4. 算 diff
	var toProcess []source.FileEntry
	for path, entry := range sourceByPath {
		prev, had := existingByPath[path]
		if had && prev.BlobSHA == entry.BlobSHA {
			continue // 内容未变,skip
		}
		toProcess = append(toProcess, entry)
		_ = path
	}
	var toDelete []*model.CodeFile
	for path, prev := range existingByPath {
		if _, stillThere := sourceByPath[path]; !stillThere {
			toDelete = append(toDelete, prev)
		}
		_ = path
	}

	return &repoPlan{
		repo:      repo,
		repoID:    repoID,
		toProcess: toProcess,
		toDelete:  toDelete,
	}, nil
}

// executePlan Phase 2 per-repo:按 plan 跑 fetch+embed+persist、清过时 file、记 last_synced。
//
// 每完成一个文件调 reporter.Inc(1,0) 或 Inc(0,1),让前端进度条以文件为粒度推进。
// 致命错误抛上去让 SyncUser 终止整轮;非致命错误记 FailedFiles,继续别的 file。
func (s *ingestService) executePlan(ctx context.Context, src source.Source, orgID uint64, plan *repoPlan, reporter ajsvc.ProgressReporter, result *SyncResult) error {
	// 处理变更/新增
	for _, entry := range plan.toProcess {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.syncOneFile(ctx, src, plan.repo, plan.repoID, orgID, entry, result); err != nil {
			if isFatalError(err) {
				return err
			}
			result.FailedFiles = append(result.FailedFiles, FailedItem{
				Ref:   plan.repo.PathWithNamespace + ":" + entry.Path,
				Error: unwrapFirst(err),
			})
			_ = reporter.Inc(0, 1)
			continue
		}
		_ = reporter.Inc(1, 0)
	}

	// 处理 deleted:DB 有、source 没有 → 删 file + chunks。不占进度(toProcess 才算)。
	for _, prev := range plan.toDelete {
		if _, err := s.deps.Repo.Chunks().DeleteByFileID(ctx, prev.ID); err != nil {
			s.deps.Log.WarnCtx(ctx, "code ingest: delete chunks failed", map[string]any{
				"file_id": prev.ID, "err": err.Error(),
			})
		}
		if err := s.deps.Repo.Files().DeleteByID(ctx, prev.ID); err != nil && !errors.Is(err, code.ErrFileNotFound) {
			s.deps.Log.WarnCtx(ctx, "code ingest: delete file failed", map[string]any{
				"file_id": prev.ID, "err": err.Error(),
			})
			continue
		}
		result.FilesDeleted++
	}

	// 标 repo 同步完成(commit 等信息 MVP 留空,见 schema 注释)
	now := time.Now()
	if err := s.deps.Repo.Repos().UpdateLastSynced(ctx, plan.repoID, "", now); err != nil {
		s.deps.Log.WarnCtx(ctx, "code ingest: update last_synced failed", map[string]any{
			"repo_id": plan.repoID, "err": err.Error(),
		})
	}
	return nil
}

// syncOneFile 单文件同步:fetch → chunk → embed → upsert + swap chunks。
//
// 非致命错误(ErrFileGone / ErrFileTooLarge / chunker 无产出)→ 返对应 sentinel,
// 让 syncOneRepo 记录到 FailedFiles/FilesSkipped 但不中断。
// 致命错(embed auth / DB 不可达)→ 抛上去。
func (s *ingestService) syncOneFile(
	ctx context.Context,
	src source.Source,
	repo source.RepoSnapshot,
	repoID, orgID uint64,
	entry source.FileEntry,
	result *SyncResult,
) error {
	// 1. Fetch 原文
	fc, err := src.FetchFile(ctx, repo, entry)
	if err != nil {
		if errors.Is(err, source.ErrFileGone) || errors.Is(err, source.ErrFileTooLarge) {
			result.FilesSkipped++
			return nil // 不算错
		}
		return fmt.Errorf("fetch file: %w", err)
	}

	// 2. 内容 CAS 落库
	if err := s.deps.Repo.Files().UpsertContent(ctx, &model.CodeFileContent{
		BlobSHA: fc.BlobSHA,
		Size:    fc.SizeBytes,
		Content: fc.Content,
	}); err != nil {
		return fmt.Errorf("upsert content: %w", err)
	}

	// 3. 切分
	language := codechunker.LanguageFromFilename(entry.Path)
	pieces := s.deps.Chunker.Chunk(language, string(fc.Content))

	// 4. upsert 文件元信息,拿 file_id
	fileRow := &model.CodeFile{
		RepoID:       repoID,
		OrgID:        orgID,
		Path:         entry.Path,
		Language:     language,
		SizeBytes:    fc.SizeBytes,
		BlobSHA:      fc.BlobSHA,
		LastCommitID: fc.LastCommitID,
	}
	if err := s.deps.Repo.Files().UpsertFile(ctx, fileRow); err != nil {
		return fmt.Errorf("upsert file: %w", err)
	}
	// 同上,Upsert 后要查一次拿 ID(ON CONFLICT 分支不会回填)
	saved, err := s.deps.Repo.Files().GetByRepoAndPath(ctx, repoID, entry.Path)
	if err != nil {
		return fmt.Errorf("load saved file: %w", err)
	}
	if saved == nil {
		return fmt.Errorf("upsert file succeeded but not found: %s", entry.Path)
	}
	fileID := saved.ID

	result.FilesChanged++

	// 5. 空 chunk → 清掉旧 chunks(无内容可索引)
	if len(pieces) == 0 {
		if _, err := s.deps.Repo.Chunks().DeleteByFileID(ctx, fileID); err != nil {
			return fmt.Errorf("delete old chunks: %w", err)
		}
		return nil
	}

	// 6. Embed 所有 pieces(batch 一次调用)。inputs 走 embedInputMaxBytes 截断兜底,
	// chunker 漏网的超大 chunk(单行超 MaxChunkBytes)也不会让整个 file 的 embed 失败。
	inputs := make([]string, len(pieces))
	for i, p := range pieces {
		if len(p.Content) > embedInputMaxBytes {
			inputs[i] = truncateUTF8(p.Content, embedInputMaxBytes)
		} else {
			inputs[i] = p.Content
		}
	}
	embedCtx, cancel := context.WithTimeout(ctx, embedTimeout)
	vecs, embedErr := s.deps.Embedder.Embed(embedCtx, inputs)
	cancel()

	// 致命 embed 错误:上抛让 SyncUser 整轮终止(重试也没用)
	if embedErr != nil && isFatalEmbedError(embedErr) {
		return fmt.Errorf("embed fatal: %w", embedErr)
	}

	// 7. 构造 chunks,原子换上
	rows := buildCodeChunks(fileID, repoID, orgID, pieces, vecs, embedErr, s.deps.Embedder.Model())
	if err := s.deps.Repo.Chunks().SwapChunksByFileID(ctx, fileID, rows); err != nil {
		return fmt.Errorf("swap chunks: %w", err)
	}
	result.ChunksCreated += len(rows)
	return nil
}

// buildCodeChunks 把 chunker 输出 + embed 结果组合成 CodeChunk 行。
//
// embedErr == nil:每行 Embedding 填好 + IndexStatus=indexed。
// embedErr 可重试错:每行 Embedding=nil + IndexStatus=failed + IndexError 记错误摘要,
// 后台补偿任务(Phase N)可以扫 failed 行重 embed。
//
// 致命 embedErr 已经在 syncOneFile 里被拦下抛上去,不会走到这里。
func buildCodeChunks(
	fileID, repoID, orgID uint64,
	pieces []codechunker.Piece,
	vecs [][]float32,
	embedErr error,
	modelTag string,
) []*model.CodeChunk {
	rows := make([]*model.CodeChunk, len(pieces))
	errMsg := ""
	if embedErr != nil {
		errMsg = embedErr.Error()
	}
	for i, p := range pieces {
		row := &model.CodeChunk{
			FileID:         fileID,
			RepoID:         repoID,
			OrgID:          orgID,
			ChunkIdx:       i,
			SymbolName:     p.SymbolName,
			Signature:      p.Signature,
			Language:       p.Language,
			ChunkKind:      p.Kind,
			LineStart:      p.LineStart,
			LineEnd:        p.LineEnd,
			Content:        p.Content,
			TokenCount:     p.TokenCount,
			ChunkerVersion: "v1",
		}
		if embedErr == nil && i < len(vecs) {
			vec := pgvector.NewVector(vecs[i])
			row.Embedding = &vec
			row.EmbeddingModel = modelTag
			row.IndexStatus = code.ChunkIndexStatusIndexed
		} else {
			row.IndexStatus = code.ChunkIndexStatusFailed
			row.IndexError = errMsg
		}
		rows[i] = row
	}
	return rows
}

// ─── 错误分类 ───────────────────────────────────────────────────────────────

// isFatalError 致命 = 整轮 sync 不用继续了。
//   - source.ErrInvalidToken:PAT 被拒,继续无意义
//   - embedding 的 auth/invalid/dim 错:配置问题,换个 repo 也一样失败
//
// 其他错误(网络 / rate limit / DB 暂时故障 / file not found / etc.)都不致命,
// 这轮 skip,下轮 sync 再来。
func isFatalError(err error) bool {
	if errors.Is(err, source.ErrInvalidToken) {
		return true
	}
	return isFatalEmbedError(err)
}

// isFatalEmbedError 判定一个 embed 错误是否"整轮 sync 不用继续了"。
//
// 真正 fatal 的只有"配置性错误":
//   - ErrEmbeddingAuth:api_key 错,重试每个 file 都会炸
//   - ErrEmbeddingDimMismatch:维度不匹配,schema 对不上,重试也没意义
//
// **ErrEmbeddingInvalid 刻意不算 fatal**:Azure 400 常见原因是"单条 input 超 context length"
// (中文注释密集时最容易撞),这是 per-file 的内容问题,不是 provider 挂了。
// 让它走 syncOneFile 的 non-fatal 路径 → 记入 FailedFiles,继续同步别的文件,
// 比"一个文件的某个大 chunk 让整个 repo 甚至整轮 sync 炸"好得多。
//
// 副作用:若真是配置/schema 错(极少),每个 file 都会 MarkFailed 走完一遍,user 看到
// progress_failed 跟 progress_total 1:1 能自己看出来,不算严重。
func isFatalEmbedError(err error) bool {
	return errors.Is(err, embedding.ErrEmbeddingAuth) ||
		errors.Is(err, embedding.ErrEmbeddingDimMismatch)
}

// unwrapFirst 取最外层错误消息 —— 不暴露完整 wrap 链,让前端显示的 error 干净。
// 目前简单取 err.Error(),超长也不截断(前端展示时自己处理折叠)。
func unwrapFirst(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ─── noop reporter ──────────────────────────────────────────────────────────

// noopReporter cron / batch 路径用 —— 不需要向任何持久存储汇报进度。
type noopReporter struct{}

func (noopReporter) SetTotal(int) error { return nil }
func (noopReporter) Inc(int, int) error { return nil }
