// Package handler integration 模块的 HTTP handler。
//
// 路由布局(详见 router.go):
//   GET    /api/v2/orgs/:slug/integrations/feishu            状态查询(已授权 / 未授权)
//   POST   /api/v2/orgs/:slug/integrations/feishu/connect    生成 OAuth 授权 URL
//   DELETE /api/v2/orgs/:slug/integrations/feishu            撤销授权
//   GET    /api/v2/integrations/feishu/callback              OAuth 回调(飞书 302 过来,公开端点)
//
// callback 之所以不在 :slug 组里,是因为飞书 302 时 redirect_uri 必须和白名单精确匹配,
// 多 org 都用同一 URL 简化配置;user_id / org_id 通过 state 自包含还原。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	ajmodel "github.com/eyrihe999-stack/Synapse/internal/asyncjob/model"
	ajsvc "github.com/eyrihe999-stack/Synapse/internal/asyncjob/service"
	"github.com/eyrihe999-stack/Synapse/internal/integration"
	intgmodel "github.com/eyrihe999-stack/Synapse/internal/integration/model"
	intgsvc "github.com/eyrihe999-stack/Synapse/internal/integration/service"
	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
)

// OrgResolver 按 :slug 查 orgID —— 因为本包要做 org context 校验但不想强依赖 document.OrgPort,
// 所以剥一个最小接口:只要给我 slug → orgID 的查询即可。
//
// 调用方(cmd/synapse)注入时传 OrgPort 的 GetOrgBySlug 即可。
type OrgResolver interface {
	GetOrgIDBySlug(ctx context.Context, slug string) (uint64, error)
}

// FeishuIntegrationService 本 handler 依赖的 service 方法集。抽接口方便测试时 mock。
//
// 签名里的 orgID:凭证按 org 查,所以 BuildAuthURL / ExchangeCode 都需要 orgID。
// 未配置 App 时 BuildAuthURL / ExchangeCode 会返 ErrAppNotConfigured,
// handler 捕此转成 412 + 引导文案。
type FeishuIntegrationService interface {
	BuildAuthURL(ctx context.Context, orgID uint64, state string, scopes []string) (string, error)
	ExchangeCode(ctx context.Context, userID, orgID uint64, code string) (*intgmodel.UserIntegration, error)
	Revoke(ctx context.Context, userID uint64) error
}

// IntegrationRepo handler 读当前状态用。只暴露读接口的子集,不给写权限。
type IntegrationRepo interface {
	FindByUserProvider(ctx context.Context, userID uint64, provider string) (*intgmodel.UserIntegration, error)
}

// AsyncJobScheduler 飞书一键同步端点依赖。签名和 *asyncjob/service.Service 的方法匹配。
// 注入 nil 时 sync 端点会返 503 —— 部署未启用 asyncjob 模块时的友好降级。
//
// FindActive 给 FeishuStatus 用:回带当前活跃 job 的 id,让前端跨页面跳转回来时能接上进度。
// FindLatest 用于 "最近一次失败"的持久展示:前端 mount 时若无 active 任务,但最近一次是 failed,
// 仍然回带 job id 让前端渲染"同步失败 + 重试"横幅。
type AsyncJobScheduler interface {
	Schedule(ctx context.Context, in ajsvc.ScheduleInput) (*ajmodel.Job, error)
	FindActive(ctx context.Context, userID uint64, kind string) (*ajmodel.Job, error)
	FindLatest(ctx context.Context, userID uint64, kind string) (*ajmodel.Job, error)
}

// Handler 集成管理 HTTP 入口。目前有 feishu + gitlab;将来加 google / slack 都挂在这一个 handler 下。
type Handler struct {
	feishu       FeishuIntegrationService
	gitlab       GitLabIntegrationService // v1 PAT 模式,无 OAuth
	repo         IntegrationRepo
	stateSigner  *StateSigner
	orgResolver  OrgResolver
	jobScheduler AsyncJobScheduler // 可选。未注入时 sync 端点返 503。
	frontendURL  string            // OAuth 回调成功 / 失败后 302 跳回的前端页面,如 "https://app.x.com/integrations"
	log          logger.LoggerInterface

	// feishuScopes OAuth 授权 URL 带的 scope 列表。空 = 走开发者后台配置的默认 scope。
	// 显式声明能让前端看到"会被授权哪些权限"(未来扩 UI 时有用)。
	feishuScopes []string
}

// Config Handler 构造参数。
type Config struct {
	Feishu       FeishuIntegrationService
	GitLab       GitLabIntegrationService // 可选;nil = /gitlab 端点返 503
	Repo         IntegrationRepo
	StateSigner  *StateSigner
	OrgResolver  OrgResolver
	JobScheduler AsyncJobScheduler // 可选。nil = /feishu/sync 端点返 503。
	FrontendURL  string
	FeishuScopes []string // 可选
	Logger       logger.LoggerInterface
}

// HasFeishu 此 Handler 是否装配了飞书 service。router 据此决定是否挂飞书路由。
func (h *Handler) HasFeishu() bool { return h.feishu != nil }

// HasGitLab 此 Handler 是否装配了 GitLab service。router 据此决定是否挂 GitLab 路由。
func (h *Handler) HasGitLab() bool { return h.gitlab != nil }

// New 构造。FrontendURL 空 fallback 到返 JSON(无页面可跳)—— 开发期便利,生产应该填。
func New(cfg Config) *Handler {
	scopes := cfg.FeishuScopes
	if len(scopes) == 0 {
		// 默认需要的三个 scope,和 docs/feishu-integration.md 文档对齐。
		scopes = []string{
			"drive:drive:readonly",
			"wiki:wiki:readonly",
			"docx:document:readonly",
		}
	}
	return &Handler{
		feishu:       cfg.Feishu,
		gitlab:       cfg.GitLab,
		repo:         cfg.Repo,
		stateSigner:  cfg.StateSigner,
		orgResolver:  cfg.OrgResolver,
		jobScheduler: cfg.JobScheduler,
		frontendURL:  cfg.FrontendURL,
		feishuScopes: scopes,
		log:          cfg.Logger,
	}
}

// ─── handler 方法 ────────────────────────────────────────────────────────────

// feishuStatusResponse GET /integrations/feishu 的响应。
// Connected=false 时其他字段空 —— 前端据此决定显示"连接"按钮还是"已连接信息"。
type feishuStatusResponse struct {
	Connected   bool   `json:"connected"`
	OpenID      string `json:"open_id,omitempty"`
	Name        string `json:"name,omitempty"`
	Email       string `json:"email,omitempty"`
	LastSyncAt  *int64 `json:"last_sync_at,omitempty"` // unix seconds,nil = 从未同步
	ConnectedAt *int64 `json:"connected_at,omitempty"`
	// ActiveSyncJobID 当前用户是否有活跃(queued/running)的飞书同步任务。
	// 非 0 → 前端可直接用此 id 走 /async-jobs/:id 轮询,跨页面跳转能接上进度;
	// 0 / 字段缺失 → 没有活跃任务。
	ActiveSyncJobID uint64 `json:"active_sync_job_id,omitempty"`
	// LastFailedSyncJobID 最近一次同步任务整体失败(status=failed)时的 job id。
	// 前端 mount 时抓下来展示"失败 + 重试"横幅。成功 / canceled 不回带。
	LastFailedSyncJobID uint64 `json:"last_failed_sync_job_id,omitempty"`
	// LastPartialSyncJobID 最近一次同步"整体成功但有部分文件失败"时的 job id。
	// 前端抓 job 详情后渲染可展开的"部分失败"横幅,让用户看到哪些文件 / 为啥挂了。
	// 和 LastFailedSyncJobID 互斥 —— 一次 sync 要么整体失败要么整体成功。
	LastPartialSyncJobID uint64 `json:"last_partial_sync_job_id,omitempty"`
}

// FeishuStatus GET /api/v2/orgs/:slug/integrations/feishu
// 查当前 user 对飞书的授权状态。给前端 integrations 页面渲染用。
func (h *Handler) FeishuStatus(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	intg, err := h.repo.FindByUserProvider(c.Request.Context(), userID, integration.ProviderFeishu)
	if err != nil {
		h.log.ErrorCtx(c.Request.Context(), "feishu status: repo error", err, map[string]any{"user_id": userID})
		response.InternalServerError(c, "failed to load integration status", err.Error())
		return
	}
	if intg == nil || intg.RefreshToken == "" {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: http.StatusOK, Message: "ok",
			Result: feishuStatusResponse{Connected: false},
		})
		return
	}

	// metadata 里存了 open_id / name / email(ExchangeCode 时写入);解析失败 ignore,只丢失副信息。
	meta := parseFeishuMetadata(intg.Metadata)
	connectedAt := intg.CreatedAt.Unix()
	resp := feishuStatusResponse{
		Connected:   true,
		OpenID:      meta.OpenID,
		Name:        meta.Name,
		Email:       meta.Email,
		ConnectedAt: &connectedAt,
	}
	if intg.LastSyncAt != nil {
		ts := intg.LastSyncAt.Unix()
		resp.LastSyncAt = &ts
	}
	// 回填 sync job 相关字段 —— 让前端 mount / 刷新时接上已在跑的任务进度,或展示上次失败横幅。
	// asyncjob 未启用(jobScheduler=nil)时跳过,omitempty 字段不会序列化。
	// 查询失败只 log 不 fail —— 锦上添花,不该阻塞连接状态页。
	if h.jobScheduler != nil {
		active, aerr := h.jobScheduler.FindActive(c.Request.Context(), userID, ajmodel.KindFeishuSync)
		if aerr != nil {
			h.log.WarnCtx(c.Request.Context(), "feishu status: find active job failed", map[string]any{
				"user_id": userID, "err": aerr.Error(),
			})
		} else if active != nil {
			resp.ActiveSyncJobID = active.ID
		} else {
			// 没有 active → 看最近一次是否"值得用户关注":整体失败 OR 整体成功但有部分失败。
			// 两种都走 FindLatest 一次查询,分别填到两个字段,前端渲染不同的横幅。
			latest, lerr := h.jobScheduler.FindLatest(c.Request.Context(), userID, ajmodel.KindFeishuSync)
			if lerr != nil {
				h.log.WarnCtx(c.Request.Context(), "feishu status: find latest job failed", map[string]any{
					"user_id": userID, "err": lerr.Error(),
				})
			} else if latest != nil {
				switch {
				case latest.Status == ajmodel.StatusFailed:
					resp.LastFailedSyncJobID = latest.ID
				case latest.Status == ajmodel.StatusSucceeded && latest.ProgressFailed > 0:
					resp.LastPartialSyncJobID = latest.ID
				}
			}
		}
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}

// feishuConnectResponse POST /integrations/feishu/connect 的响应。前端拿 auth_url 做 window.location。
// state 一并返回,纯粹给前端 debug / 记录用;真正的 state 校验由 callback handler 完成。
type feishuConnectResponse struct {
	AuthURL string `json:"auth_url"`
	State   string `json:"state"`
}

// FeishuConnect POST /api/v2/orgs/:slug/integrations/feishu/connect
// 生成 OAuth 授权 URL + HMAC-签的 state(5 分钟有效),前端引导用户跳转。
func (h *Handler) FeishuConnect(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	orgID, err := h.orgIDFromSlug(c)
	if err != nil {
		// 已经 writeResponse 过,直接 return
		return
	}

	state, err := h.stateSigner.Sign(userID, orgID)
	if err != nil {
		h.log.ErrorCtx(c.Request.Context(), "feishu connect: sign state", err, nil)
		response.InternalServerError(c, "failed to prepare oauth state", err.Error())
		return
	}
	authURL, err := h.feishu.BuildAuthURL(c.Request.Context(), orgID, state, h.feishuScopes)
	if errors.Is(err, intgsvc.ErrAppNotConfigured) {
		// 引导前端:org 尚未配置飞书应用,admin 需要先填 app_id / app_secret。
		response.Error(c, http.StatusPreconditionRequired, "feishu app not configured", "org_feishu_configs missing")
		return
	}
	if err != nil {
		h.log.ErrorCtx(c.Request.Context(), "feishu connect: build auth url", err, nil)
		response.InternalServerError(c, "failed to build auth url", err.Error())
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code: http.StatusOK, Message: "ok",
		Result: feishuConnectResponse{AuthURL: authURL, State: state},
	})
}

// FeishuCallback GET /api/v2/integrations/feishu/callback?code=X&state=Y
//
// 飞书 OAuth 成功后 302 过来。不需要 JWT(callback 是 Feishu 调的,不带 Synapse token),
// 用户身份由 state 携带并 HMAC 校验。
//
// 响应:
//   - 配置了 FrontendURL → 302 跳 `{FrontendURL}?feishu=success` / `?feishu=error&reason=...`
//   - 否则返 JSON(纯后端联调时够用)
func (h *Handler) FeishuCallback(c *gin.Context) {
	code := strings.TrimSpace(c.Query("code"))
	state := strings.TrimSpace(c.Query("state"))

	// error 参数:用户取消授权 / 飞书端错误,也走 callback 但不带 code。
	if errCode := c.Query("error"); errCode != "" {
		h.finishCallback(c, "error", errCode)
		return
	}
	if code == "" || state == "" {
		h.finishCallback(c, "error", "missing_params")
		return
	}

	userID, orgID, err := h.stateSigner.Verify(state)
	if err != nil {
		h.log.WarnCtx(c.Request.Context(), "feishu callback: state invalid", map[string]any{"err": err.Error()})
		reason := "invalid_state"
		if err == ErrStateExpired {
			reason = "state_expired"
		}
		h.finishCallback(c, "error", reason)
		return
	}

	if _, err := h.feishu.ExchangeCode(c.Request.Context(), userID, orgID, code); err != nil {
		if errors.Is(err, intgsvc.ErrAppNotConfigured) {
			// 极少见:state 签发后 admin 把 App 配置删了;给个可区分的 reason,前端可提示。
			h.finishCallback(c, "error", "app_not_configured")
			return
		}
		h.log.ErrorCtx(c.Request.Context(), "feishu callback: exchange failed", err, map[string]any{"user_id": userID})
		h.finishCallback(c, "error", "exchange_failed")
		return
	}
	h.finishCallback(c, "success", "")
}

// feishuSyncResponse POST /feishu/sync 响应。前端用 job_id 走 GET /async-jobs/:id 轮询。
// already_running=true 表示此前已有 job 在跑,本次是幂等返回(job_id 指向那个已有 job)。
type feishuSyncResponse struct {
	JobID          uint64 `json:"job_id"`
	AlreadyRunning bool   `json:"already_running"`
}

// FeishuSync POST /api/v2/orgs/:slug/integrations/feishu/sync
//
// 触发一次前台飞书同步。创建 async_job → 立刻返回 job_id,前端轮询进度。
//
// 前置条件:用户已经走完 OAuth(user_integrations 里有行 + 有 refresh_token)。
// 未连接返 412 Precondition Required。已有活跃 job 返 200 + already_running=true(幂等,
// 前端直接继续轮询那条 job_id 即可)。
func (h *Handler) FeishuSync(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	orgID, err := h.orgIDFromSlug(c)
	if err != nil {
		return
	}
	if h.jobScheduler == nil {
		// 部署未启用 asyncjob 模块。用户应该看不到此端点,但前端 bug / 脚本调用都可能走到。
		response.ServiceUnavailable(c, "sync not available on this deployment", "")
		return
	}

	// 提前校验"已连接",给用户明确的 412 而不是让 runner 失败才显现。
	intg, err := h.repo.FindByUserProvider(c.Request.Context(), userID, integration.ProviderFeishu)
	if err != nil {
		h.log.ErrorCtx(c.Request.Context(), "feishu sync: repo error", err, map[string]any{"user_id": userID})
		response.InternalServerError(c, "failed to load integration", err.Error())
		return
	}
	if intg == nil || intg.RefreshToken == "" {
		response.Error(c, http.StatusPreconditionRequired, "feishu not connected", "")
		return
	}

	job, err := h.jobScheduler.Schedule(c.Request.Context(), ajsvc.ScheduleInput{
		OrgID:  orgID,
		UserID: userID,
		Kind:   ajmodel.KindFeishuSync,
	})
	if errors.Is(err, ajsvc.ErrDuplicateJob) {
		// 幂等:返现有 job,前端继续轮询。
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: http.StatusOK, Message: "already running",
			Result: feishuSyncResponse{JobID: job.ID, AlreadyRunning: true},
		})
		return
	}
	if err != nil {
		h.log.ErrorCtx(c.Request.Context(), "feishu sync: schedule failed", err, map[string]any{"user_id": userID})
		response.InternalServerError(c, "schedule sync failed", err.Error())
		return
	}
	// 返 200 而非 202 —— 前端 apiCall 惯例只认 200/201 为成功;
	// 语义上任务已被接受调度,交互上和"同步成功"的文案一致即可。
	c.JSON(http.StatusOK, response.BaseResponse{
		Code: http.StatusOK, Message: "scheduled",
		Result: feishuSyncResponse{JobID: job.ID, AlreadyRunning: false},
	})
}

// FeishuDisconnect DELETE /api/v2/orgs/:slug/integrations/feishu
// 撤销授权。幂等 —— 即使本来没授权,也返 200(删掉不存在的行 = no-op)。
func (h *Handler) FeishuDisconnect(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	if err := h.feishu.Revoke(c.Request.Context(), userID); err != nil {
		h.log.ErrorCtx(c.Request.Context(), "feishu disconnect failed", err, map[string]any{"user_id": userID})
		response.InternalServerError(c, "disconnect failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: gin.H{}})
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// orgIDFromSlug 从 URL :slug 参数解析到 orgID。失败直接 writeResponse,调用方只 return。
func (h *Handler) orgIDFromSlug(c *gin.Context) (uint64, error) {
	slug := strings.TrimSpace(c.Param("slug"))
	if slug == "" {
		response.BadRequest(c, "missing org slug", "")
		return 0, errMissingSlug
	}
	id, err := h.orgResolver.GetOrgIDBySlug(c.Request.Context(), slug)
	if err != nil {
		response.NotFound(c, "org not found", err.Error())
		return 0, err
	}
	return id, nil
}

// finishCallback 根据 FrontendURL 配置选 302 跳转 或 JSON 返回。
// status = "success" / "error",reason 是可读码(前端渲染文案)。
func (h *Handler) finishCallback(c *gin.Context, status, reason string) {
	if h.frontendURL == "" {
		// 没配前端 URL:直接 JSON。开发期常见。
		payload := gin.H{"status": status}
		if reason != "" {
			payload["reason"] = reason
		}
		httpStatus := http.StatusOK
		if status == "error" {
			httpStatus = http.StatusBadRequest
		}
		c.JSON(httpStatus, response.BaseResponse{Code: httpStatus, Message: status, Result: payload})
		return
	}
	sep := "?"
	if strings.Contains(h.frontendURL, "?") {
		sep = "&"
	}
	redirect := h.frontendURL + sep + "feishu=" + status
	if reason != "" {
		redirect += "&reason=" + reason
	}
	c.Redirect(http.StatusFound, redirect)
}

// feishuMeta Metadata jsonb 的解析 shape —— 和 intgsvc.ExchangeCode 里写入的 userInfoResponse 对齐。
type feishuMeta struct {
	OpenID string `json:"open_id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
}

func parseFeishuMetadata(raw []byte) feishuMeta {
	var m feishuMeta
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &m) // 解析失败返空结构体,前端把字段当 optional 处理
	}
	return m
}

// errMissingSlug sentinel,仅用于 orgIDFromSlug 在已 writeResponse 后告诉调用方 "别继续"。
var errMissingSlug = errors.New("missing slug")
