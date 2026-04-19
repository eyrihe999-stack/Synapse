// Package feishusync asyncjob 里飞书一键同步的 Runner。
//
// 业务核心 SyncOneUser 在 logic.go;本 Runner 把它包装成 asyncjob.Runner 接口,
// 让前端"一键导入"端点触发 + 前端按 job_id 轮询进度。
//
// 目前不存在后台无人值守 cron —— 所有同步走前端用户主动触发。若将来要做定时同步,
// 直接在 cmd/synapse 起 ticker goroutine 调 SyncOneUser 即可(不必单独 binary)。
package feishusync

import (
	"context"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/asyncjob/model"
	"github.com/eyrihe999-stack/Synapse/internal/asyncjob/service"
	"github.com/eyrihe999-stack/Synapse/internal/integration"
	intgrepo "github.com/eyrihe999-stack/Synapse/internal/integration/repository"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
)

// Runner 实现 asyncjob/service.Runner。通过 Deps 注入下层能力,不直接依赖 cmd/。
type Runner struct {
	deps Deps
	log  logger.LoggerInterface
}

// Deps Runner 运行一次 Sync 所需的所有外部能力。
// 在 cmd/synapse 主装配线构造一次后复用。凭证不在 Deps 里 —— 运行时按 orgID 动态查。
type Deps struct {
	FeishuBaseURL string
	IntgRepo      intgrepo.Repository
	FeishuService FeishuTokenService
	DocSvc        DocumentUploader
}

// NewRunner 构造。
func NewRunner(deps Deps, log logger.LoggerInterface) *Runner {
	return &Runner{deps: deps, log: log}
}

// Kind 见 asyncjob/service.Runner。
func (r *Runner) Kind() string { return model.KindFeishuSync }

// Run 按 job.UserID 找该用户的飞书 integration,调 SyncOneUser 做一次同步。
// 进度通过 reporter 汇报;result 返 SyncResult。
func (r *Runner) Run(ctx context.Context, job *model.Job, reporter service.ProgressReporter) (any, error) {
	intg, err := r.deps.IntgRepo.FindByUserProvider(ctx, job.UserID, integration.ProviderFeishu)
	if err != nil {
		return nil, fmt.Errorf("load integration: %w", err)
	}
	if intg == nil || intg.RefreshToken == "" {
		return nil, fmt.Errorf("feishu not connected for user %d", job.UserID)
	}
	// OrgID 以 integration 行上记录的为准(授权时的 org context)。
	// 不用 job.OrgID —— 虽然一致,但 integration 侧是 source of truth。

	result, err := SyncOneUser(ctx, SyncDeps{
		FeishuBaseURL: r.deps.FeishuBaseURL,
		IntgRepo:      r.deps.IntgRepo,
		FeishuService: r.deps.FeishuService,
		DocSvc:        r.deps.DocSvc,
		Log:           r.log,
	}, intg, reporter)
	if err != nil {
		return nil, err
	}
	return result, nil
}
