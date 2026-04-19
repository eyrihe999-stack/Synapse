// search_service.go 代码知识库检索 —— 两路召回(向量 + 符号精确) + 同文件 siblings 拼接。
//
// 流程:
//   1. 并发召回:
//      - 向量路径:embed(query) → SearchByVector(ANN)
//      - 符号路径:SearchBySymbol(ILIKE on symbol_name)
//   2. 合并去重:symbol 命中优先(精确匹配置信度更高),向量命中按距离升序补位
//   3. 回填元信息:批量拉 file(path/language)+ repo(path_with_namespace/web_url)
//   4. (可选)拼同文件 siblings:命中 chunk 的邻近 chunks,给 agent 更完整的上下文
//
// 返回的 Hit 对 agent 友好:自带文件路径 + 行号 + 仓库 URL,agent 能直接在回答里引用。
package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/eyrihe999-stack/Synapse/internal/code/model"
	"github.com/eyrihe999-stack/Synapse/internal/code/repository"
	"github.com/eyrihe999-stack/Synapse/pkg/embedding"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
)

// SearchService 对外接口。
type SearchService interface {
	// SearchCode 跨 org 的代码检索。query 可以是自然语言问题或函数/类名;
	// opts 未设字段走默认(TopK=20,IncludeSiblings=true)。
	// 两路召回任一路失败不算整体失败,另一路仍返结果 + 日志警告。两路都挂才返 error。
	SearchCode(ctx context.Context, orgID uint64, query string, opts SearchOptions) (*SearchResult, error)
}

// SearchOptions 检索可选参数。零值都有合理默认,调用方可按需覆盖。
type SearchOptions struct {
	// Languages 命中 chunks 的 language 必须在此列表里(例 ["go", "python"])。空 = 不过滤。
	Languages []string
	// RepoIDs 限定只搜这几个 repo。空 = 跨 org 全部。
	RepoIDs []uint64
	// TopK 最终合并后返回的条数。默认 20,上限 50(再多对 agent 没帮助)。
	TopK int
	// IncludeSiblings 是否为每条命中回带同文件其他 chunks 作上下文。默认 true。
	IncludeSiblings bool
	// SiblingWindow 同文件 chunks 数超过此阈值时,只回带命中点邻近的 N 条(前后各 N/2)。
	// <=0 = 不限制,全部回带。默认 20 —— 对一般文件全带,对巨型文件兜底。
	SiblingWindow int
}

// SearchResult 一次检索的完整输出。Stats 给前端 debug / agent 决定要不要继续深挖用。
type SearchResult struct {
	Query string
	Hits  []*Hit
	Stats SearchStats
}

// SearchStats 两路召回各自的原始 hit 数 + 合并后总数 + 耗时。
type SearchStats struct {
	VectorHits int
	SymbolHits int
	Merged     int
	ElapsedMs  int64
}

// Hit 单条命中。承载给 agent / 前端展示所需的全部上下文:chunk 本身 + 文件坐标 + 仓库坐标 + siblings。
type Hit struct {
	Chunk       *model.CodeChunk
	File        *FileRef
	Repo        *RepoRef
	Distance    float32 // 0 = 符号精确命中;>0 = 向量 cosine 距离,越小越相关
	MatchSource string  // "vector" / "symbol"
	// Siblings 同文件其他 chunks(按 chunk_idx 升序,不含命中自身)。
	// 给 agent 拼上下文:一个函数被切成多段时,命中其中一段能看到其他段。
	Siblings []*model.CodeChunk
}

// FileRef 命中 chunk 所在文件的精简元信息。不含 content —— 那得从 code_file_contents 按 blob_sha 拉。
type FileRef struct {
	ID       uint64 `json:"id"`
	Path     string `json:"path"`
	Language string `json:"language"`
}

// RepoRef 命中 chunk 所在 repo 的精简元信息。WebURL 给前端做"在 GitLab 查看"链接用。
type RepoRef struct {
	ID                uint64 `json:"id"`
	PathWithNamespace string `json:"path_with_namespace"`
	WebURL            string `json:"web_url"`
}

// SearchDeps SearchService 的依赖。和 IngestService 的 Deps 字段有重合,但不复用 struct ——
// search 路径不需要 Chunker,拆开让依赖边界对每个 service 来说都清晰。
type SearchDeps struct {
	Repo     repository.Repository
	Embedder embedding.Embedder
	Log      logger.LoggerInterface
}

// NewSearchService 构造。
func NewSearchService(deps SearchDeps) SearchService {
	return &searchService{deps: deps}
}

type searchService struct {
	deps SearchDeps
}

// searchEmbedTimeout query 的 embedding 调用超时。单条 query,1-2s 够用,留余量防网络抖。
const searchEmbedTimeout = 10 * time.Second

// SearchCode 见接口注释。
func (s *searchService) SearchCode(ctx context.Context, orgID uint64, query string, opts SearchOptions) (*SearchResult, error) {
	start := time.Now()

	opts = normalizeSearchOptions(opts)
	filter := &repository.ChunkSearchFilter{
		Languages: opts.Languages,
		RepoIDs:   opts.RepoIDs,
	}

	// 召回 limit 稍大于 topK,给合并去重留余量 —— symbol + vector 结果可能严重重叠。
	recallLimit := opts.TopK + opts.TopK/2
	if recallLimit < opts.TopK {
		recallLimit = opts.TopK
	}

	// ─── 并发两路召回 ───────────────────────────────────────────────────
	var (
		vectorHits []repository.ChunkHit
		symbolHits []repository.ChunkHit
		vecErr     error
		symErr     error
		mu         sync.Mutex
	)

	eg, egCtx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		// 向量路径:先 embed query 再 ANN
		embedCtx, cancel := context.WithTimeout(egCtx, searchEmbedTimeout)
		defer cancel()
		vecs, err := s.deps.Embedder.Embed(embedCtx, []string{query})
		if err != nil {
			mu.Lock()
			vecErr = err
			mu.Unlock()
			return nil // 单路失败不终止 errgroup
		}
		hits, err := s.deps.Repo.Chunks().SearchByVector(egCtx, orgID, vecs[0], recallLimit, filter)
		if err != nil {
			mu.Lock()
			vecErr = err
			mu.Unlock()
			return nil
		}
		mu.Lock()
		vectorHits = hits
		mu.Unlock()
		return nil
	})
	eg.Go(func() error {
		hits, err := s.deps.Repo.Chunks().SearchBySymbol(egCtx, orgID, query, recallLimit, filter)
		if err != nil {
			mu.Lock()
			symErr = err
			mu.Unlock()
			return nil
		}
		mu.Lock()
		symbolHits = hits
		mu.Unlock()
		return nil
	})
	_ = eg.Wait()

	// 两路都挂才算整体失败;单路挂只 log 警告,另一路的结果继续用。
	if vecErr != nil && symErr != nil {
		return nil, fmt.Errorf("code search: both recall paths failed: vec=%v symbol=%v", vecErr, symErr)
	}
	if vecErr != nil {
		s.deps.Log.WarnCtx(ctx, "code search: vector recall failed, using symbol only", map[string]any{
			"org_id": orgID, "err": vecErr.Error(),
		})
	}
	if symErr != nil {
		s.deps.Log.WarnCtx(ctx, "code search: symbol recall failed, using vector only", map[string]any{
			"org_id": orgID, "err": symErr.Error(),
		})
	}

	// ─── 合并去重 ───────────────────────────────────────────────────────
	merged := mergeHits(symbolHits, vectorHits, opts.TopK)

	// ─── 批量回填 file / repo 元信息 ────────────────────────────────────
	fileIDs := uniqueIDsFromHits(merged, func(c *model.CodeChunk) uint64 { return c.FileID })
	repoIDs := uniqueIDsFromHits(merged, func(c *model.CodeChunk) uint64 { return c.RepoID })

	files, fErr := s.deps.Repo.Files().GetByIDs(ctx, fileIDs)
	if fErr != nil {
		// 元信息拿不到不该让整个检索 fail —— chunk 内容还在,agent 能用 chunk 本身的数据答题。
		s.deps.Log.WarnCtx(ctx, "code search: load files failed", map[string]any{"err": fErr.Error()})
		files = map[uint64]*model.CodeFile{}
	}
	repos, rErr := s.deps.Repo.Repos().GetByIDs(ctx, repoIDs)
	if rErr != nil {
		s.deps.Log.WarnCtx(ctx, "code search: load repos failed", map[string]any{"err": rErr.Error()})
		repos = map[uint64]*model.CodeRepository{}
	}

	// ─── (可选)拼 siblings ─────────────────────────────────────────────
	// 按 file 分组拉,一次拉完该 file 的所有 chunks,再按命中点切窗口。
	siblingsByFile := map[uint64][]*model.CodeChunk{}
	if opts.IncludeSiblings {
		for _, fid := range fileIDs {
			all, err := s.deps.Repo.Chunks().ListByFileID(ctx, fid)
			if err != nil {
				s.deps.Log.WarnCtx(ctx, "code search: list siblings failed", map[string]any{"file_id": fid, "err": err.Error()})
				continue
			}
			siblingsByFile[fid] = all
		}
	}

	// ─── 组装 Hit ───────────────────────────────────────────────────────
	out := make([]*Hit, 0, len(merged))
	for _, h := range merged {
		hit := &Hit{
			Chunk:       h.Chunk,
			Distance:    h.Distance,
			MatchSource: h.MatchSource,
		}
		if f, ok := files[h.Chunk.FileID]; ok {
			hit.File = &FileRef{ID: f.ID, Path: f.Path, Language: f.Language}
		}
		if r, ok := repos[h.Chunk.RepoID]; ok {
			hit.Repo = &RepoRef{ID: r.ID, PathWithNamespace: r.PathWithNamespace, WebURL: r.WebURL}
		}
		if opts.IncludeSiblings {
			hit.Siblings = pickSiblings(siblingsByFile[h.Chunk.FileID], h.Chunk, opts.SiblingWindow)
		}
		out = append(out, hit)
	}

	return &SearchResult{
		Query: query,
		Hits:  out,
		Stats: SearchStats{
			VectorHits: len(vectorHits),
			SymbolHits: len(symbolHits),
			Merged:     len(out),
			ElapsedMs:  time.Since(start).Milliseconds(),
		},
	}, nil
}

// normalizeSearchOptions 给零值填合理默认。统一在一处做,调用方不用每次判。
func normalizeSearchOptions(opts SearchOptions) SearchOptions {
	if opts.TopK <= 0 {
		opts.TopK = 20
	}
	if opts.TopK > 50 {
		opts.TopK = 50
	}
	if opts.SiblingWindow <= 0 {
		opts.SiblingWindow = 20
	}
	return opts
}

// mergeHits 合并两路召回:symbol 在前(精确匹配置信度高),vector 按距离补位,同 chunk_id 去重。
// 截到 topK 返回。
func mergeHits(symbolHits, vectorHits []repository.ChunkHit, topK int) []repository.ChunkHit {
	seen := make(map[uint64]struct{}, topK)
	out := make([]repository.ChunkHit, 0, topK)

	for _, h := range symbolHits {
		if len(out) >= topK {
			break
		}
		if _, ok := seen[h.Chunk.ID]; ok {
			continue
		}
		seen[h.Chunk.ID] = struct{}{}
		out = append(out, h)
	}
	for _, h := range vectorHits {
		if len(out) >= topK {
			break
		}
		if _, ok := seen[h.Chunk.ID]; ok {
			continue
		}
		seen[h.Chunk.ID] = struct{}{}
		out = append(out, h)
	}
	return out
}

// uniqueIDsFromHits 从 hit 集合里抽一列 uint64 字段去重,结果顺序任意(上层用 map 回填,无序无碍)。
func uniqueIDsFromHits(hits []repository.ChunkHit, extract func(*model.CodeChunk) uint64) []uint64 {
	if len(hits) == 0 {
		return nil
	}
	seen := make(map[uint64]struct{}, len(hits))
	out := make([]uint64, 0, len(hits))
	for _, h := range hits {
		id := extract(h.Chunk)
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// pickSiblings 从同文件所有 chunks 里剔掉命中本身,按 window 截取。
//
// 策略:
//   - all 长度 ≤ window:全带,给 agent 最完整的上下文
//   - all 长度 > window:以 hit.ChunkIdx 为中心取前后各 window/2 个
//
// 为什么以 chunk_idx 为中心:函数级切分下邻近 chunks 往往是同一类/模块的相关函数,
// 最可能帮 agent 理解"这个函数周围发生了什么"。
func pickSiblings(all []*model.CodeChunk, hit *model.CodeChunk, window int) []*model.CodeChunk {
	if len(all) == 0 || hit == nil {
		return nil
	}
	// 剔掉 hit 本身
	filtered := make([]*model.CodeChunk, 0, len(all))
	for _, c := range all {
		if c.ID == hit.ID {
			continue
		}
		filtered = append(filtered, c)
	}
	if window <= 0 || len(filtered) <= window {
		return filtered
	}
	// 按 chunk_idx 邻近原则取窗口。ListByFileID 返的 all 已按 chunk_idx 升序。
	// 在 filtered 里找第一个 idx > hit.ChunkIdx 的位置作为中心,前后切。
	centerPos := 0
	for i, c := range filtered {
		if c.ChunkIdx > hit.ChunkIdx {
			centerPos = i
			break
		}
		centerPos = i + 1 // 全部 < hit 时 center 落到末尾
	}
	half := window / 2
	lo := centerPos - half
	if lo < 0 {
		lo = 0
	}
	hi := lo + window
	if hi > len(filtered) {
		hi = len(filtered)
		lo = hi - window
		if lo < 0 {
			lo = 0
		}
	}
	return filtered[lo:hi]
}
