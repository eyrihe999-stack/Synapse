// logic.go 飞书单用户同步的业务实现。
//
// Reporter 参数允许调用方注入不同的进度汇报策略:
//   - asyncjob Runner(当前唯一消费者):dbReporter,写 async_jobs 表
//   - 将来若要做后台批量同步:传 nil 走 noopReporter,无需细进度
package feishusync

import (
	"context"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/asyncjob/service"
	"github.com/eyrihe999-stack/Synapse/internal/document/dto"
	docsvc "github.com/eyrihe999-stack/Synapse/internal/document/service"
	intgmodel "github.com/eyrihe999-stack/Synapse/internal/integration/model"
	intgrepo "github.com/eyrihe999-stack/Synapse/internal/integration/repository"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"github.com/eyrihe999-stack/Synapse/pkg/sourceadapter"
	"github.com/eyrihe999-stack/Synapse/pkg/sourceadapter/feishu"
)

// FeishuTokenService 同步过程用到的 token 刷新 + 凭证查询能力。
// 把接口定在本包(不是 intgsvc),减少对具体实现的耦合,便于测试 mock。
// 生产实现 = *intgsvc.FeishuService。
type FeishuTokenService interface {
	RefreshViaIntegration(ctx context.Context, intg *intgmodel.UserIntegration) (string, error)
	// GetOrgAppCreds 按 orgID 查该 org 的飞书 App 凭证。org 没配置时返 intgsvc.ErrAppNotConfigured。
	GetOrgAppCreds(ctx context.Context, orgID uint64) (appID, appSecret string, err error)
}

// DocumentUploader 注入 document 服务的窄接口 —— 只要 Upload。
// 同样是为了 decouple + 便于测试(mock 起来就一个方法)。
// 签名须和 docsvc.DocumentService.Upload 严格一致,这样 *docsvc 实现可直接赋值。
type DocumentUploader interface {
	Upload(ctx context.Context, in docsvc.UploadInput) (*dto.DocumentResponse, error)
}

// SyncDeps 跑 SyncOneUser 需要的所有依赖。凭证不在此结构,按 intg.OrgID 动态查。
type SyncDeps struct {
	FeishuBaseURL string
	IntgRepo      intgrepo.Repository
	FeishuService FeishuTokenService
	DocSvc        DocumentUploader
	Log           logger.LoggerInterface
}

// FailedItem 单个失败条目 —— 记 source_ref + 失败原因,前端展开看明细用。
//
// Title 可能为空:Fetch 成功但 Upload 挂掉时能拿到,Fetch 本身挂掉时就没有。
// 前端对 Title 做 fallback:空时展示 Ref(也就是 source_ref JSON),比纯 token 稍有信息量。
type FailedItem struct {
	Ref   string `json:"ref"`             // source_ref JSON,稳定标识
	Title string `json:"title,omitempty"` // 文档标题(Fetch 成功后才有)
	Error string `json:"error"`           // err.Error() 去包后的根因
}

// SyncResult Runner 结束时写入 Job.Result 的 json payload。
type SyncResult struct {
	Total       int          `json:"total"`        // 扫到的变更数
	Synced      int          `json:"synced"`       // 成功导入
	Failed      int          `json:"failed"`       // 失败(Fetch / Upload 任一阶段挂掉)
	FailedItems []FailedItem `json:"failed_items"` // 失败条目明细 —— ref + title(可能空)+ 错误原因
	LastSyncAt  int64        `json:"last_sync_at"` // 本轮 sync 完成时间戳(unix seconds)
}

// SyncOneUser 单个用户的同步循环。
//
// 失败颗粒度到"单文件 skip":单文件炸不让整个 sync 停。
// reporter 为 nil 时走 noop —— 后台 cron 不需要细进度。
func SyncOneUser(ctx context.Context, deps SyncDeps, intg *intgmodel.UserIntegration, reporter service.ProgressReporter) (*SyncResult, error) {
	if reporter == nil {
		reporter = noopReporter{}
	}
	// Step 1: access_token 续命。到期就刷新 + 写回 DB。
	if _, err := deps.FeishuService.RefreshViaIntegration(ctx, intg); err != nil {
		return nil, fmt.Errorf("refresh token: %w", err)
	}

	// Step 2: 按 intg.OrgID 查应用凭证,构造 Adapter。凭证 per org,不同 org 的用户用不同的 App。
	appID, appSecret, err := deps.FeishuService.GetOrgAppCreds(ctx, intg.OrgID)
	if err != nil {
		return nil, fmt.Errorf("load org app creds: %w", err)
	}
	// OnRefreshTokenRotated 回调:adapter 内部 tokener 刷 access_token 时飞书会轮换 refresh_token,
	// 必须回写 user_integrations,否则下次用旧 refresh_token 调飞书会被拒(code=20026, 已用过)。
	// 用 background ctx 写库,不受当前请求 ctx 生命周期影响 —— sync 临近结束时 ctx 可能被 cancel,
	// 但这条 token 轮换必须落库,不然就是永久丢失。
	intgIDForRotate := intg.ID
	onRotated := func(newRefreshToken string, refreshExpiresIn int) {
		bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var refreshExp *time.Time
		if refreshExpiresIn > 0 {
			t := time.Now().Add(time.Duration(refreshExpiresIn) * time.Second)
			refreshExp = &t
		}
		// 只更 refresh_token 相关字段,access_token 让 tokener 内存缓存权威 —— 避免和 tokener 竞争。
		// UpdateTokens 的 accessToken 参数不能传空串(repo 约定),这里传当前 intg 缓存的即可,
		// 反正 access_token_expires_at 也会被 tokener 后续更新路径覆写。
		if err := deps.IntgRepo.UpdateTokens(bgCtx, intgIDForRotate, intg.AccessToken, intg.AccessTokenExpiresAt, newRefreshToken, refreshExp); err != nil {
			deps.Log.ErrorCtx(bgCtx, "feishu sync: persist rotated refresh_token failed", err, map[string]any{
				"integration_id": intgIDForRotate, "user_id": intg.UserID,
			})
			return
		}
		// 同步本地 struct,免得 SyncOneUser 后续步骤用老值。
		intg.RefreshToken = newRefreshToken
		if refreshExp != nil {
			intg.RefreshTokenExpiresAt = refreshExp
		}
	}
	adapter, err := feishu.NewAdapter(feishu.Config{
		AppID:                 appID,
		AppSecret:             appSecret,
		BaseURL:               deps.FeishuBaseURL,
		OnRefreshTokenRotated: onRotated,
	}, intg.RefreshToken)
	if err != nil {
		return nil, fmt.Errorf("build adapter: %w", err)
	}

	// Step 3: Sync 拿变更列表。Sync 结束前总量未知 → ProgressTotal=0,
	// 扫完这步才有准确总数,SetTotal 一下让前端进度条切换成"X / N"样式。
	var since time.Time
	if intg.LastSyncAt != nil {
		since = *intg.LastSyncAt
	}
	changes, err := adapter.Sync(ctx, intg.OrgID, since)
	if err != nil {
		return nil, fmt.Errorf("adapter sync: %w", err)
	}
	_ = reporter.SetTotal(len(changes))

	// Step 4: 逐条 Fetch + Upload。
	// ctx 被 cancel(比如服务 shutdown)就提前退出,已处理的进度保留。
	result := &SyncResult{Total: len(changes)}
	for i, ch := range changes {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		title, err := syncOneChange(ctx, deps.DocSvc, adapter, intg, ch)
		if err != nil {
			result.Failed++
			result.FailedItems = append(result.FailedItems, FailedItem{
				Ref:   string(ch.Ref),
				Title: title, // Fetch 成功后 Upload 挂掉时能拿到,Fetch 挂掉时为空
				Error: err.Error(),
			})
			_ = reporter.Inc(0, 1)
			deps.Log.WarnCtx(ctx, "feishu sync: change failed", map[string]any{
				"user_id": intg.UserID, "ref": string(ch.Ref), "idx": i, "err": err.Error(),
			})
			continue
		}
		result.Synced++
		_ = reporter.Inc(1, 0)
	}

	// Step 5: 标本轮 sync 完成时间。下次增量以此为 since。
	now := time.Now()
	if err := deps.IntgRepo.UpdateLastSyncAt(ctx, intg.ID, now); err != nil {
		// warn 不 fail —— 本次数据已落,只是下次可能重扫一遍,代价是冗余调用(有 dedup 兜底)。
		deps.Log.WarnCtx(ctx, "feishu sync: update last_sync_at failed", map[string]any{
			"user_id": intg.UserID, "err": err.Error(),
		})
	}
	result.LastSyncAt = now.Unix()
	return result, nil
}

// syncOneChange 处理单条变更。返回 (title, error):
//   - 两者都空 = 删除 / noop
//   - title 非空 + error 非空 = Fetch 成功但 Upload 挂了,上层可以用 title 做人可读展示
//   - title 空 + error 非空 = Fetch 就挂了,没拿到元信息
func syncOneChange(ctx context.Context, docSvc DocumentUploader, adapter *feishu.Adapter, intg *intgmodel.UserIntegration, ch sourceadapter.Change) (string, error) {
	if ch.Action == sourceadapter.ChangeDelete {
		// 飞书 list 不返 deleted 条目,这条永远走不到。webhook 接进来后在本分支加删除逻辑。
		return "", nil
	}
	raw, err := adapter.Fetch(ctx, intg.OrgID, ch.Ref)
	if err != nil {
		return "", fmt.Errorf("fetch: %w", err)
	}
	// docsvc.Upload 自动按 (source_type, source_ref) 做 upsert:
	// 第一次新建;后续 Sync 走 overwriteExisting 原子换 chunks。内容不变走 content_hash dedup,省 embed。
	_, err = docSvc.Upload(ctx, docsvc.UploadInput{
		OrgID:      intg.OrgID,
		UploaderID: intg.UserID,
		Title:      raw.Title,
		FileName:   raw.FileName,
		MIMEType:   raw.MIMEType,
		Content:    raw.Content,
		SourceType: adapter.Type(),
		SourceRef:  []byte(ch.Ref),
	})
	if err != nil {
		return raw.Title, fmt.Errorf("upload: %w", err)
	}
	return raw.Title, nil
}

// noopReporter cron 路径用。
type noopReporter struct{}

func (noopReporter) SetTotal(int) error     { return nil }
func (noopReporter) Inc(int, int) error     { return nil }
