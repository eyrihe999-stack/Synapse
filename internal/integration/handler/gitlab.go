// gitlab.go GitLab PAT 集成的 HTTP handler。
//
// 相对 feishu 的简化:
//   - 无 OAuth 回调(PAT 是用户直接粘贴,不走跳转)
//   - 无 admin-scope 的 App 配置(BaseURL 是部署级,不是 per-org)
//   - 无 state 签名
//
// 挂在同一个 *Handler 结构上,复用 orgIDFromSlug / userID 提取逻辑 / log 注入。
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
	intgsvc "github.com/eyrihe999-stack/Synapse/internal/integration/service"
	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
)

// GitLabIntegrationService handler 依赖的 service 方法集。抽接口便于测试 mock。
// 只有 Connect / Revoke;Status 直接读 repo。
type GitLabIntegrationService interface {
	Connect(ctx context.Context, userID, orgID uint64, token string) (*intgsvc.GitLabConnectResult, error)
	Revoke(ctx context.Context, userID uint64) error
}

// gitlabStatusResponse GET /integrations/gitlab 响应。
type gitlabStatusResponse struct {
	Connected   bool   `json:"connected"`
	UserID      int64  `json:"user_id,omitempty"`
	Username    string `json:"username,omitempty"`
	Name        string `json:"name,omitempty"`
	Email       string `json:"email,omitempty"`
	AvatarURL   string `json:"avatar_url,omitempty"`
	WebURL      string `json:"web_url,omitempty"`
	ConnectedAt *int64 `json:"connected_at,omitempty"`
	LastSyncAt  *int64 `json:"last_sync_at,omitempty"`

	// ActiveSyncJobID / LastFailedSyncJobID / LastPartialSyncJobID 三个字段语义和 feishuStatusResponse 对称 —— 让前端
	// mount 时能接上 in-flight 的同步,或展示"上次失败 / 部分失败"横幅。
	// jobScheduler 未注入(未启用 asyncjob 或 code 模块不可用)时这三个都不填,omitempty 不序列化。
	ActiveSyncJobID      uint64 `json:"active_sync_job_id,omitempty"`
	LastFailedSyncJobID  uint64 `json:"last_failed_sync_job_id,omitempty"`
	LastPartialSyncJobID uint64 `json:"last_partial_sync_job_id,omitempty"`
}

// GitLabStatus GET /api/v2/orgs/:slug/integrations/gitlab
// 返当前 user 的 GitLab 连接状态。未连接时 Connected=false,其他字段省略。
func (h *Handler) GitLabStatus(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	intg, err := h.repo.FindByUserProvider(c.Request.Context(), userID, integration.ProviderGitLab)
	if err != nil {
		h.log.ErrorCtx(c.Request.Context(), "gitlab status: repo error", err, map[string]any{"user_id": userID})
		response.InternalServerError(c, "failed to load integration status", err.Error())
		return
	}
	if intg == nil || intg.AccessToken == "" {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: http.StatusOK, Message: "ok",
			Result: gitlabStatusResponse{Connected: false},
		})
		return
	}
	meta := parseGitLabMetadata(intg.Metadata)
	connectedAt := intg.CreatedAt.Unix()
	resp := gitlabStatusResponse{
		Connected:   true,
		UserID:      meta.UserID,
		Username:    meta.Username,
		Name:        meta.Name,
		Email:       meta.Email,
		AvatarURL:   meta.AvatarURL,
		WebURL:      meta.WebURL,
		ConnectedAt: &connectedAt,
	}
	if intg.LastSyncAt != nil {
		ts := intg.LastSyncAt.Unix()
		resp.LastSyncAt = &ts
	}
	// 回填 sync job 状态(对称 FeishuStatus)。asyncjob 未启用或查询失败时跳过,不阻塞连接状态展示。
	if h.jobScheduler != nil {
		active, aerr := h.jobScheduler.FindActive(c.Request.Context(), userID, ajmodel.KindGitLabSync)
		if aerr != nil {
			h.log.WarnCtx(c.Request.Context(), "gitlab status: find active job failed", map[string]any{
				"user_id": userID, "err": aerr.Error(),
			})
		} else if active != nil {
			resp.ActiveSyncJobID = active.ID
		} else {
			latest, lerr := h.jobScheduler.FindLatest(c.Request.Context(), userID, ajmodel.KindGitLabSync)
			if lerr != nil {
				h.log.WarnCtx(c.Request.Context(), "gitlab status: find latest job failed", map[string]any{
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

// gitlabSyncResponse POST /integrations/gitlab/sync 响应。前端用 job_id 走 GET /async-jobs/:id 轮询。
type gitlabSyncResponse struct {
	JobID          uint64 `json:"job_id"`
	AlreadyRunning bool   `json:"already_running"`
}

// GitLabSync POST /api/v2/orgs/:slug/integrations/gitlab/sync
//
// 触发一次前台 GitLab 代码同步。创建 async_job → 立刻返回 job_id,前端轮询进度。
//
// 前置条件:user_integrations 里有行 + AccessToken(PAT)非空。
// 未连接返 412。已有活跃 job 返 200 + already_running=true(幂等,前端继续轮询该 job)。
func (h *Handler) GitLabSync(c *gin.Context) {
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
		// 部署未启用 asyncjob / code 模块不可用(无 PG 或 embedding 未配置)。用户不该看到此端点,但 bug / 脚本可能命中。
		response.ServiceUnavailable(c, "gitlab sync not available on this deployment", "")
		return
	}

	// 提前校验"已连接",给用户明确的 412 而不是让 runner 失败才显现。
	intg, err := h.repo.FindByUserProvider(c.Request.Context(), userID, integration.ProviderGitLab)
	if err != nil {
		h.log.ErrorCtx(c.Request.Context(), "gitlab sync: repo error", err, map[string]any{"user_id": userID})
		response.InternalServerError(c, "failed to load integration", err.Error())
		return
	}
	if intg == nil || intg.AccessToken == "" {
		response.Error(c, http.StatusPreconditionRequired, "gitlab not connected", "")
		return
	}

	job, err := h.jobScheduler.Schedule(c.Request.Context(), ajsvc.ScheduleInput{
		OrgID:  orgID,
		UserID: userID,
		Kind:   ajmodel.KindGitLabSync,
	})
	if errors.Is(err, ajsvc.ErrDuplicateJob) {
		// 幂等:返现有 job,前端继续轮询。
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: http.StatusOK, Message: "already running",
			Result: gitlabSyncResponse{JobID: job.ID, AlreadyRunning: true},
		})
		return
	}
	if err != nil {
		h.log.ErrorCtx(c.Request.Context(), "gitlab sync: schedule failed", err, map[string]any{"user_id": userID})
		response.InternalServerError(c, "schedule sync failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code: http.StatusOK, Message: "scheduled",
		Result: gitlabSyncResponse{JobID: job.ID, AlreadyRunning: false},
	})
}

// gitlabConnectRequest PUT /integrations/gitlab 请求体。
type gitlabConnectRequest struct {
	Token string `json:"token"`
}

// gitlabConnectResponse PUT /integrations/gitlab 响应。返回验证后的用户信息供前端立即渲染,
// 避免前端再发一次 GET /integrations/gitlab。
type gitlabConnectResponse struct {
	Connected bool   `json:"connected"`
	UserID    int64  `json:"user_id"`
	Username  string `json:"username"`
	Name      string `json:"name,omitempty"`
	Email     string `json:"email,omitempty"`
	AvatarURL string `json:"avatar_url,omitempty"`
	WebURL    string `json:"web_url,omitempty"`
}

// GitLabConnect PUT /api/v2/orgs/:slug/integrations/gitlab
//
// 请求体:{token: "glpat-..."}
// 流程:调 GitLab /user 验证 → 有效则 upsert user_integrations → 返用户信息。
// Token 无效返 400 + reason=invalid_token;GitLab 不可达返 502。
func (h *Handler) GitLabConnect(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	orgID, err := h.orgIDFromSlug(c)
	if err != nil {
		return
	}
	var body gitlabConnectRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		response.BadRequest(c, "invalid request body", err.Error())
		return
	}
	body.Token = strings.TrimSpace(body.Token)
	if body.Token == "" {
		response.BadRequest(c, "token required", "")
		return
	}

	result, err := h.gitlab.Connect(c.Request.Context(), userID, orgID, body.Token)
	if errors.Is(err, intgsvc.ErrGitLabNotConfigured) {
		// org admin 尚未在前端填 GitLab 实例配置,引导去 /integrations/gitlab/config 设置页。
		response.Error(c, http.StatusPreconditionRequired, "gitlab instance not configured", "org_gitlab_configs missing")
		return
	}
	if errors.Is(err, intgsvc.ErrInvalidGitLabToken) {
		response.BadRequest(c, "invalid_token", "GitLab rejected the token; verify it has read_api scope and is not expired")
		return
	}
	if err != nil {
		h.log.ErrorCtx(c.Request.Context(), "gitlab connect failed", err, map[string]any{"user_id": userID})
		// 区分"GitLab 不可达"和"我们自己的内部错误":这里粗粒度走 502,细分等用户反馈再拆。
		response.Error(c, http.StatusBadGateway, "failed to reach GitLab", err.Error())
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code: http.StatusOK, Message: "ok",
		Result: gitlabConnectResponse{
			Connected: true,
			UserID:    result.User.ID,
			Username:  result.User.Username,
			Name:      result.User.Name,
			Email:     result.User.Email,
			AvatarURL: result.User.AvatarURL,
			WebURL:    result.User.WebURL,
		},
	})
}

// GitLabDisconnect DELETE /api/v2/orgs/:slug/integrations/gitlab
// 撤销连接。幂等。
func (h *Handler) GitLabDisconnect(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	if err := h.gitlab.Revoke(c.Request.Context(), userID); err != nil {
		h.log.ErrorCtx(c.Request.Context(), "gitlab disconnect failed", err, map[string]any{"user_id": userID})
		response.InternalServerError(c, "disconnect failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: gin.H{}})
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// gitlabMeta Metadata jsonb 的解析 shape —— 和 service.gitlabMetadata 对齐(各自维护避免跨包耦合)。
type gitlabMeta struct {
	UserID    int64  `json:"user_id"`
	Username  string `json:"username"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	AvatarURL string `json:"avatar_url"`
	WebURL    string `json:"web_url"`
}

func parseGitLabMetadata(raw []byte) gitlabMeta {
	var m gitlabMeta
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &m)
	}
	return m
}

