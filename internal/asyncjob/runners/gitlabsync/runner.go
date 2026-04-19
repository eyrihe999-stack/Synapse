// Package gitlabsync asyncjob 里 GitLab 一键同步的 Runner。
//
// 业务核心 SyncOneUser 在 logic.go;本 Runner 把它包装成 asyncjob.Runner 接口,
// 让前端"一键导入"端点触发 + 前端按 job_id 轮询进度。
//
// 与 feishusync 的区别:
//   - 无 token 刷新(PAT 是长期凭证)
//   - ingest 目标是 code_* 表而非 documents
//   - 依赖 OrgGitLabConfig 查 BaseURL(per-org 实例配置)
package gitlabsync

import (
	"context"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/asyncjob/model"
	"github.com/eyrihe999-stack/Synapse/internal/asyncjob/service"
	codesvc "github.com/eyrihe999-stack/Synapse/internal/code/service"
	"github.com/eyrihe999-stack/Synapse/internal/integration"
	intgrepo "github.com/eyrihe999-stack/Synapse/internal/integration/repository"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"github.com/eyrihe999-stack/Synapse/pkg/sourceadapter/gitlab"
)

// GitLabClientBuilder 按 orgID 构造 GitLab 客户端的能力。
// 生产实现 = *intgsvc.GitLabService(见 BuildClientForOrg 方法);测试可用 fake。
type GitLabClientBuilder interface {
	BuildClientForOrg(ctx context.Context, orgID uint64) (gitlab.ClientAPI, error)
}

// Deps Runner 需要的所有外部能力。在 cmd/synapse 主装配一次后复用。
// 凭证(PAT)不在 Deps 里 —— 运行时按 job.UserID 查 intg.AccessToken。
type Deps struct {
	IntgRepo       intgrepo.Repository
	ClientBuilder  GitLabClientBuilder
	IngestService  codesvc.IngestService
}

// Runner 实现 asyncjob/service.Runner。
type Runner struct {
	deps Deps
	log  logger.LoggerInterface
}

// NewRunner 构造。
func NewRunner(deps Deps, log logger.LoggerInterface) *Runner {
	return &Runner{deps: deps, log: log}
}

// Kind 见 asyncjob/service.Runner。
func (r *Runner) Kind() string { return model.KindGitLabSync }

// Run 按 job.UserID 找该用户的 GitLab integration,调 SyncOneUser 做一次同步。
// 进度通过 reporter 汇报;result 返 codesvc.SyncResult。
func (r *Runner) Run(ctx context.Context, job *model.Job, reporter service.ProgressReporter) (any, error) {
	intg, err := r.deps.IntgRepo.FindByUserProvider(ctx, job.UserID, integration.ProviderGitLab)
	if err != nil {
		return nil, fmt.Errorf("load integration: %w", err)
	}
	if intg == nil || intg.AccessToken == "" {
		// PAT 模式下 AccessToken 就是 PAT;空或未 Connect 时明确拒绝,不让 Runner 白跑一轮。
		return nil, fmt.Errorf("gitlab not connected for user %d", job.UserID)
	}

	result, err := SyncOneUser(ctx, SyncDeps{
		IntgRepo:      r.deps.IntgRepo,
		ClientBuilder: r.deps.ClientBuilder,
		IngestService: r.deps.IngestService,
		Log:           r.log,
	}, intg, reporter)
	if err != nil {
		return nil, err
	}
	return result, nil
}
