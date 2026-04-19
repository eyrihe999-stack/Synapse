// logic.go GitLab 单用户同步的业务实现。
//
// 和 feishusync/logic.go 的区别:
//   - 无 access_token 刷新(PAT 永久)
//   - ingest 调 code.IngestService.SyncUser(代码模块)而非 document.Upload
//   - 按 orgID 查 OrgGitLabConfig 构造 client(BaseURL 是 per-org 的,PAT 是 per-user 的)
package gitlabsync

import (
	"context"
	"fmt"
	"time"

	ajsvc "github.com/eyrihe999-stack/Synapse/internal/asyncjob/service"
	codesvc "github.com/eyrihe999-stack/Synapse/internal/code/service"
	codesource "github.com/eyrihe999-stack/Synapse/internal/code/source"
	intgmodel "github.com/eyrihe999-stack/Synapse/internal/integration/model"
	intgrepo "github.com/eyrihe999-stack/Synapse/internal/integration/repository"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
)

// SyncDeps 跑 SyncOneUser 需要的依赖。
//
// IngestService 就是 code 模块编排 —— 它接一个 source.Source 进来,负责 fetch/chunk/embed/persist。
// 这一层只负责:拿 PAT + 构造 GitLab client + 构造 source + 调 ingest。
type SyncDeps struct {
	IntgRepo      intgrepo.Repository
	ClientBuilder GitLabClientBuilder
	IngestService codesvc.IngestService
	Log           logger.LoggerInterface
}

// SyncOneUser 单个用户的一次 GitLab 同步。
//
// 失败语义:
//   - 构造 client 失败(org 未配置 BaseURL / 配置损坏) → 返 error,整轮失败
//   - IngestService 返 error(致命:PAT 被拒 / embed auth 错) → 原样返
//   - IngestService 返 SyncResult + 有单 repo / 单 file 失败 → SyncResult 里记明细,return nil error
//     (不让"有些 repo 失败"整单 fail;前端按 Result.Failed* 展示)
//
// reporter 为 nil 时走 noop(IngestService 内部会判,这里不重复)。
func SyncOneUser(ctx context.Context, deps SyncDeps, intg *intgmodel.UserIntegration, reporter ajsvc.ProgressReporter) (*codesvc.SyncResult, error) {
	// Step 1: 按 intg.OrgID 查配置构造 client
	client, err := deps.ClientBuilder.BuildClientForOrg(ctx, intg.OrgID)
	if err != nil {
		return nil, fmt.Errorf("build gitlab client: %w", err)
	}

	// Step 2: 构造 source(per-user,带 PAT)
	src := codesource.NewGitLabSource(client, intg.AccessToken)

	// Step 3: 跑 ingest
	result, err := deps.IngestService.SyncUser(ctx, src, intg.OrgID, intg.UserID, reporter)
	if err != nil {
		return nil, err
	}

	// Step 4: 标本轮 sync 完成时间点到 user_integrations.last_sync_at
	// (user_integrations 和 code_repositories.last_synced_at 各自记录不同时序点:前者=用户粒度"上次点了同步",
	//  后者=每个 repo 粒度"上次被 sync 完")
	now := time.Now()
	if err := deps.IntgRepo.UpdateLastSyncAt(ctx, intg.ID, now); err != nil {
		// warn 不 fail —— 数据已落,只是下次展示"上次同步时间"会不准
		deps.Log.WarnCtx(ctx, "gitlab sync: update last_sync_at failed", map[string]any{
			"user_id": intg.UserID, "err": err.Error(),
		})
	}
	if result != nil {
		result.LastSyncAt = now.Unix()
	}
	return result, nil
}
