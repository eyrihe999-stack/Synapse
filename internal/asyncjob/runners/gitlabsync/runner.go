// Package gitlabsync asyncjob runner for "gitlab repository full sync".
//
// Kind = model.KindGitLabSync = "integration.sync.gitlab"。**不**实现 ConcurrentRunner ——
// 同一 (user, kind) 同时只允许一条全量同步在跑(防 owner 误连点重复拉);精确幂等走 IdempotencyKey
// "gitlab:<source_id>:full"。
//
// Flow:
//
//	source.service.CreateGitLabSource / TriggerGitLabResync
//	  → asyncjob.Schedule(Kind, Payload=Input{SourceID})
//	  → runner.Run:
//	      1. 反查 source(model.Source) + owner 凭据(user_integrations)
//	      2. 构造 GitLab client(base_url 来自 user_integrations.provider_meta.base_url)
//	      3. VerifyToken → 拿当前 commit sha(GetCommit(ref=branch))
//	      4. ingestion/source/gitlab.Fetcher → ingestion.Pipeline.Run
//	      5. 终态写 sources.last_sync_status / commit / error
package gitlabsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	asyncmodel "github.com/eyrihe999-stack/Synapse/internal/asyncjob/model"
	asyncsvc "github.com/eyrihe999-stack/Synapse/internal/asyncjob/service"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	docrepo "github.com/eyrihe999-stack/Synapse/internal/document/repository"
	gitlabclient "github.com/eyrihe999-stack/Synapse/internal/integration/gitlab"
	"github.com/eyrihe999-stack/Synapse/internal/ingestion"
	gitlabfetcher "github.com/eyrihe999-stack/Synapse/internal/ingestion/source/gitlab"
	"github.com/eyrihe999-stack/Synapse/internal/source"
	srcmodel "github.com/eyrihe999-stack/Synapse/internal/source/model"
	srcrepo "github.com/eyrihe999-stack/Synapse/internal/source/repository"
	uirepo "github.com/eyrihe999-stack/Synapse/internal/user_integration/repository"
)

// Kind 任务字面量。和 asyncmodel.KindGitLabSync 同步;留独立常量方便外部 import 时不跨包。
const Kind = asyncmodel.KindGitLabSync

// Mode 同步模式取值。
const (
	// ModeFull 全量同步:走 ListTreeRecursive 拉完整 tree。CreateGitLabSource / TriggerResync 用。
	ModeFull = "full"
	// ModeIncremental 增量同步:走 CompareCommits(BeforeSHA → AfterSHA)拿 diff。webhook 用。
	ModeIncremental = "incremental"
)

// 全 0 sha:GitLab push event 在"branch 首次推送 / 删除 branch"时 before / after 会是这个值。
// 用作 sentinel 触发"退化全量同步"分支。
const zeroSHA = "0000000000000000000000000000000000000000"

// Input runner 的 payload 契约。service 层 marshal 进 Job.Payload。
//
// SourceID 必填;Mode 空串 / 不识别 → 走 ModeFull。
// Incremental 模式下 BeforeSHA / AfterSHA 必填;BeforeSHA == zeroSHA 时退化为 full。
//
// 设计:**runner 自己反查凭据 / branch / commit**,不让 payload 里塞 token —— owner 改 PAT 后
// 重试自然用最新凭据,无 stale 风险。
type Input struct {
	SourceID  uint64 `json:"source_id,string"`
	Mode      string `json:"mode,omitempty"`
	BeforeSHA string `json:"before_sha,omitempty"`
	AfterSHA  string `json:"after_sha,omitempty"`
}

// Result 成功摘要。前端把它从 async_jobs.result 拿回展示。
type Result struct {
	SourceID  uint64 `json:"source_id,string"`
	CommitSHA string `json:"commit_sha"` // 本次同步索引的 commit
}

// Runner 实现 asyncjob.service.Runner。**不**实现 ConcurrentRunner —— 默认 FindActive 防重。
type Runner struct {
	pipeline *ingestion.Pipeline
	srcRepo  srcrepo.Repository
	uiRepo   uirepo.Repository
	docRepo  docrepo.Repository
	log      logger.LoggerInterface
}

// New 构造。任一依赖 nil → 装配错,启动期 fatal。
func New(pipeline *ingestion.Pipeline, srcRepo srcrepo.Repository, uiRepo uirepo.Repository, docRepo docrepo.Repository, log logger.LoggerInterface) (*Runner, error) {
	if pipeline == nil {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("gitlabsync: nil pipeline")
	}
	if srcRepo == nil {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("gitlabsync: nil source repo")
	}
	if uiRepo == nil {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("gitlabsync: nil user_integration repo")
	}
	if docRepo == nil {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("gitlabsync: nil document repo")
	}
	if log == nil {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("gitlabsync: nil log")
	}
	return &Runner{pipeline: pipeline, srcRepo: srcRepo, uiRepo: uiRepo, docRepo: docRepo, log: log}, nil
}

// Kind 见 asyncjob.service.Runner。
func (*Runner) Kind() string { return Kind }

// Run 单轮全量同步。
//
//nolint:funlen // 串行流程清晰,拆分意义不大
func (r *Runner) Run(ctx context.Context, job *asyncmodel.Job, reporter asyncsvc.ProgressReporter) (any, error) {
	var in Input
	if err := json.Unmarshal(job.Payload, &in); err != nil {
		r.log.ErrorCtx(ctx, "gitlabsync: unmarshal payload", err, map[string]any{"job_id": job.ID})
		return nil, fmt.Errorf("unmarshal payload: %w", err)
	}
	if in.SourceID == 0 {
		return nil, fmt.Errorf("gitlabsync: source_id required")
	}

	// 1) 反查 source
	src, err := r.srcRepo.FindSourceByID(ctx, in.SourceID)
	if err != nil {
		r.log.ErrorCtx(ctx, "gitlabsync: load source", err, map[string]any{"source_id": in.SourceID})
		return nil, fmt.Errorf("load source: %w", err)
	}
	if src.Kind != srcmodel.KindGitLabRepo {
		// 不可能,但写 audit 友好
		return nil, fmt.Errorf("gitlabsync: source kind=%q is not gitlab_repo", src.Kind)
	}

	// 2) 反查凭据(严格用 source.GitLabIntegrationID,不 fallback)
	ui, err := r.findIntegration(ctx, src)
	if err != nil {
		r.markStatus(ctx, src.ID, srcmodel.SyncStatusAuthFailed, "", err.Error())
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}

	// 3) 构造 GitLab client(base_url 从 provider_meta 读;无 → DefaultBaseURL)
	baseURL := readBaseURLFromMeta(ui.ProviderMeta)
	cli := gitlabclient.New(baseURL, ui.AccessToken)

	// VerifyToken 失败 + 走 ErrSourceGitLabAuthFailed 路径 → 标 auth_failed
	if _, vErr := cli.VerifyToken(ctx); vErr != nil {
		r.handleSyncError(ctx, src, vErr, "verify_token")
		//sayso-lint:ignore sentinel-wrap
		return nil, vErr
	}

	// 4) 解析模式 + 计算本轮的 to_sha:
	//    - full / 增量但 BeforeSHA == zeroSHA(branch 首次推送)→ 走 GetCommit(branch) 解 HEAD,fetcher 全量 ListTree
	//    - incremental → CompareCommits(BeforeSHA, AfterSHA),AfterSHA 即 to_sha
	mode := normalizeMode(in.Mode, in.BeforeSHA, in.AfterSHA)
	var (
		commitSHA    string
		changedFiles []string
		removedFiles []string
	)
	switch mode {
	case ModeIncremental:
		commitSHA = in.AfterSHA
		fcs, err := cli.CompareCommits(ctx, src.ExternalRef, in.BeforeSHA, in.AfterSHA)
		if err != nil {
			r.handleSyncError(ctx, src, err, "compare_commits")
			//sayso-lint:ignore sentinel-wrap
			return nil, err
		}
		changedFiles, removedFiles = splitChanges(fcs)
		r.log.InfoCtx(ctx, "gitlabsync: incremental diff resolved", map[string]any{
			"source_id": src.ID, "before": in.BeforeSHA, "after": in.AfterSHA,
			"changed": len(changedFiles), "removed": len(removedFiles),
		})
	default: // ModeFull
		commit, err := cli.GetCommit(ctx, src.ExternalRef, src.GitLabBranch)
		if err != nil {
			r.handleSyncError(ctx, src, err, "get_commit")
			//sayso-lint:ignore sentinel-wrap
			return nil, err
		}
		commitSHA = commit.ID
	}

	// 5a) Removed 文件:增量场景下走 docRepo 直接删 doc(CASCADE 清 chunks)。
	//     不依赖 fetcher,因为 fetcher 只产出"新增/修改后的内容",不处理 tombstone。
	for _, p := range removedFiles {
		sourceKey := fmt.Sprintf("gitlab:%s:%s", src.ExternalRef, p)
		if err := r.docRepo.DeleteBySourceID(ctx, src.OrgID, ingestion.SourceTypeDocument, sourceKey); err != nil {
			// 单文件删失败 warn,不阻塞整轮(下次 sync / cron 对账兜底)
			r.log.WarnCtx(ctx, "gitlabsync: delete removed doc failed", map[string]any{
				"source_id": src.ID, "path": p, "err": err.Error(),
			})
		}
	}

	// 5b) 构造 fetcher。incremental 模式 ChangedFiles 非空,fetcher 只拉这些文件;
	//     full 模式 ChangedFiles 为 nil,fetcher 走 ListTreeRecursive。
	fetcher := gitlabfetcher.New(gitlabfetcher.Input{
		OrgID:             src.OrgID,
		UploaderID:        src.OwnerUserID,
		KnowledgeSourceID: src.ID,
		ProjectID:         src.ExternalRef,
		PathWithNamespace: src.Name,
		Branch:            src.GitLabBranch,
		CommitSHA:         commitSHA,
		WebBaseURL:        baseURL, // GitLab 站点根 URL,UI blob 链接拼接用
		ChangedFiles:      changedFiles,
	}, cli, r.log)

	// reporter 由 pipeline 自己 SetTotal(chunk 数)/Inc — 这里不 set total。
	// 跨多个文件时 SetTotal 会被 pipeline 在每个 doc 切块后覆盖,前端会看到滚动跳;一期可接受。
	if pErr := r.pipeline.Run(ctx, fetcher, reporter); pErr != nil {
		r.handleSyncError(ctx, src, pErr, "pipeline_run")
		//sayso-lint:ignore sentinel-wrap
		return nil, pErr
	}

	// 6) 终态成功
	r.markStatus(ctx, src.ID, srcmodel.SyncStatusSucceeded, commitSHA, "")
	r.log.InfoCtx(ctx, "gitlabsync: source synced", map[string]any{
		"source_id": src.ID, "project_id": src.ExternalRef, "branch": src.GitLabBranch,
		"commit": commitSHA, "mode": mode,
	})
	return Result{SourceID: src.ID, CommitSHA: commitSHA}, nil
}

// normalizeMode payload mode 字段 + before/after 的合法性归一。
//
// 退化规则:
//   - mode 空串 / 不识别 → ModeFull
//   - mode == incremental 但缺 BeforeSHA / AfterSHA → ModeFull(可能是 payload 早期版本)
//   - BeforeSHA == zeroSHA(branch 首次推送 / 删 branch)→ ModeFull
//
// 这样 incremental 路径任何"信息不全"都安全降级到全量,不会卡住。
func normalizeMode(mode, beforeSHA, afterSHA string) string {
	if mode != ModeIncremental {
		return ModeFull
	}
	if beforeSHA == "" || afterSHA == "" {
		return ModeFull
	}
	if beforeSHA == zeroSHA {
		return ModeFull
	}
	return ModeIncremental
}

// splitChanges 把 GitLab compare 的 FileChange 拆成"待 fetch 列表"和"待删列表"。
//
//   - added / modified → fetch(直接拉新内容覆盖)
//   - renamed → 旧 doc 删 + 新路径 fetch(因为 SourceID = "gitlab:<project>:<path>" 含路径,
//     重命名等价于"删旧 + 加新")
//   - removed → 仅删
func splitChanges(fcs []gitlabclient.FileChange) (changed []string, removed []string) {
	for _, fc := range fcs {
		switch fc.Status {
		case "added", "modified":
			changed = append(changed, fc.Path)
		case "renamed":
			if fc.OldPath != "" {
				removed = append(removed, fc.OldPath)
			}
			if fc.Path != "" {
				changed = append(changed, fc.Path)
			}
		case "removed":
			if fc.Path != "" {
				removed = append(removed, fc.Path)
			}
		}
	}
	return changed, removed
}

// ─── 内部辅助 ─────────────────────────────────────────────────────────────────

// findIntegration 严格按 source.GitLabIntegrationID 取凭据。
// 拿不到 / status 非 active → 翻 ErrSourceGitLabAuthFailed,runner 标 auth_failed。
func (r *Runner) findIntegration(ctx context.Context, src *srcmodel.Source) (*uimodelStub, error) {
	// uirepo 没有 GetByID 方法,但有 ListByUser — 取 owner 该 provider 列表回查 id。
	// 凭据数量天然小(每用户每 provider ≤ 几条),全列扫成本 O(N)<=10。
	rows, err := r.uiRepo.ListByUser(ctx, src.OwnerUserID)
	if err != nil {
		return nil, fmt.Errorf("list user_integrations: %w: %w", err, source.ErrSourceInternal)
	}
	for _, ui := range rows {
		if ui.ID != src.GitLabIntegrationID {
			continue
		}
		if ui.Status != "active" {
			return nil, fmt.Errorf("user_integration #%d status=%q: %w", ui.ID, ui.Status, source.ErrSourceGitLabAuthFailed)
		}
		// 转薄结构(避免直接返 *model.UserIntegration 把 model 包暴露给 runner 包外)
		return &uimodelStub{
			ID:           ui.ID,
			AccessToken:  ui.AccessToken,
			ProviderMeta: []byte(ui.ProviderMeta),
		}, nil
	}
	return nil, fmt.Errorf("integration #%d not found for owner #%d: %w",
		src.GitLabIntegrationID, src.OwnerUserID, source.ErrSourceGitLabAuthFailed)
}

// handleSyncError 把 sentinel 翻成 last_sync_status 写库。
func (r *Runner) handleSyncError(ctx context.Context, src *srcmodel.Source, err error, stage string) {
	status := srcmodel.SyncStatusFailed
	if errors.Is(err, source.ErrSourceGitLabAuthFailed) {
		status = srcmodel.SyncStatusAuthFailed
	}
	summary := fmt.Sprintf("[%s] %s", stage, err.Error())
	if len(summary) > 512 {
		summary = summary[:512]
	}
	r.markStatus(ctx, src.ID, status, "", summary)
	r.log.WarnCtx(ctx, "gitlabsync: stage failed", map[string]any{
		"source_id": src.ID, "stage": stage, "status": status, "err": err.Error(),
	})
}

// markStatus 更新 sources.last_sync_*,行不见时仅 warn。
func (r *Runner) markStatus(ctx context.Context, sourceID uint64, status, commit, errSummary string) {
	if err := r.srcRepo.UpdateGitLabSyncStatus(ctx, sourceID, status, commit, errSummary); err != nil {
		r.log.WarnCtx(ctx, "gitlabsync: update last_sync_* failed", map[string]any{
			"source_id": sourceID, "status": status, "err": err.Error(),
		})
	}
}

// uimodelStub runner 内部用的最小凭据视图。
type uimodelStub struct {
	ID           uint64
	AccessToken  string
	ProviderMeta []byte // datatypes.JSON 底层就是 []byte
}

// readBaseURLFromMeta provider_meta jsonb {base_url: "..."} → string。失败/空 → 返空(client 自己 fallback Default)。
func readBaseURLFromMeta(meta []byte) string {
	if len(meta) == 0 {
		return ""
	}
	var m map[string]string
	if err := json.Unmarshal(meta, &m); err != nil {
		return ""
	}
	return m["base_url"]
}
