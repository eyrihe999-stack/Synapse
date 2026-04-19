// Package service code 模块的业务编排层。
//
// 目前提供:
//   - IngestService.SyncUser(source, orgID, userID):把一个用户可见的全部代码仓库同步进 Synapse。
//
// 未来会加 SearchService(检索)等,但 MVP 只做 ingest —— 先把数据拿进来,再谈检索。
//
// 设计:service 层不知道具体 provider(GitLab / GitHub...)。调用方构造好 source.Source 交给 SyncUser,
// service 只沿接口调。provider 相关的鉴权 / BaseURL / 错误映射都在 source 实现里封好。
package service

import (
	"context"

	"github.com/eyrihe999-stack/Synapse/internal/asyncjob/service"
	"github.com/eyrihe999-stack/Synapse/internal/code/repository"
	"github.com/eyrihe999-stack/Synapse/internal/code/source"
	"github.com/eyrihe999-stack/Synapse/pkg/codechunker"
	"github.com/eyrihe999-stack/Synapse/pkg/embedding"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
)

// IngestService 对外接口。只暴露业务动作,不暴露子组件。
type IngestService interface {
	// SyncUser 对传入的 source 执行一次全量(按 blob_sha 增量 diff)同步,
	// 持久化进当前 orgID 的 code_repositories / code_files / code_chunks。
	//
	// userID 目前仅用于日志诊断(code 模块的所有权是 org 级,不到 user 级)。
	//
	// reporter 可为 nil(cron / batch 调用路径不需要细进度),此时内部走 noop。
	// runner 路径注入 dbReporter,把进度写进 async_jobs.progress_*。
	//
	// 返回错误 = 整轮失败(embed auth 错 / DB 不可达等致命路径);
	// 单 repo / 单 file 失败不会让整体 fail,记录在 SyncResult.Failed* 里返给前端看。
	SyncUser(ctx context.Context, src source.Source, orgID, userID uint64, reporter service.ProgressReporter) (*SyncResult, error)
}

// SyncResult 一次 sync 的统计。写进 async_jobs.result 的 JSON。
//
// 字段设计:
//   - Repos/Files/Chunks 三组计数给前端显示"这次同步了多少"
//   - FailedRepos / FailedFiles 是明细 —— 前端可展开查看哪个 repo / 哪个文件失败了
type SyncResult struct {
	ReposTotal    int `json:"repos_total"`
	ReposSynced   int `json:"repos_synced"`
	ReposSkipped  int `json:"repos_skipped"` // 临时不可访问(ErrRepoUnavailable)或无文件变更
	ReposFailed   int `json:"repos_failed"`
	FilesChanged  int `json:"files_changed"`  // 新增 + 更新
	FilesDeleted  int `json:"files_deleted"`  // 源端消失 → 本地清除
	FilesSkipped  int `json:"files_skipped"`  // ErrFileTooLarge / ErrFileGone / chunk=0 等单文件跳过
	ChunksCreated int `json:"chunks_created"` // 新增写入 code_chunks 的行数
	FailedRepos   []FailedItem `json:"failed_repos,omitempty"`
	FailedFiles   []FailedItem `json:"failed_files,omitempty"`
	LastSyncAt    int64        `json:"last_sync_at"`
}

// FailedItem 单条失败明细。Ref 是人可读的定位符,便于前端展示。
type FailedItem struct {
	Ref   string `json:"ref"`   // repo: "group/subgroup/repo-name";file: "group/subgroup/repo-name:path/to/foo.go"
	Error string `json:"error"` // 去 wrap 的错误字符串
}

// Deps 构造 IngestService 需要的依赖。
//
// 不包含 source.Source 本身 —— source 是 per-call 构造(每个用户的 PAT 不同),
// 由 runner / handler 层构造好传入 SyncUser。
type Deps struct {
	Repo     repository.Repository
	Chunker  codechunker.Chunker
	Embedder embedding.Embedder
	Log      logger.LoggerInterface
}

// NewIngestService 构造。
func NewIngestService(deps Deps) IngestService {
	return &ingestService{deps: deps}
}

// ingestService IngestService 的具体实现。字段不可变,构造后并发安全(内部方法不共享可变状态)。
type ingestService struct {
	deps Deps
}
