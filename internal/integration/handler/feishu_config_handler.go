// feishu_config_handler.go 飞书 App 凭证的 admin 端点。
//
// 路由(在 router.go 里挂):
//
//	GET    /api/v2/orgs/:slug/integrations/feishu/config
//	PUT    /api/v2/orgs/:slug/integrations/feishu/config
//	DELETE /api/v2/orgs/:slug/integrations/feishu/config
//
// 权限:PermIntegrationManage(owner + admin 默认持有)。
// 为避免泄漏密钥,GET 响应不回传 app_secret,只回 configured 标志 + app_id 明文 + redirect_uri(admin 要把这个 URL 加到飞书白名单)。
package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	intgmodel "github.com/eyrihe999-stack/Synapse/internal/integration/model"
	intgrepo "github.com/eyrihe999-stack/Synapse/internal/integration/repository"
	orghandler "github.com/eyrihe999-stack/Synapse/internal/organization/handler"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
)

// FeishuConfigHandler admin 端 Feishu App 凭证 CRUD。
//
// 独立结构体 + 独立构造函数,不和 OAuth 流的 Handler 混 —— 依赖集合不同(只需 configRepo 和 redirectURI)。
type FeishuConfigHandler struct {
	configRepo  intgrepo.FeishuConfigRepository
	redirectURI string // 部署级,用于返给前端展示"请把此 URL 加到飞书应用白名单"
}

// NewFeishuConfigHandler 构造。
func NewFeishuConfigHandler(configRepo intgrepo.FeishuConfigRepository, redirectURI string) *FeishuConfigHandler {
	return &FeishuConfigHandler{
		configRepo:  configRepo,
		redirectURI: redirectURI,
	}
}

// feishuConfigResponse GET/PUT/DELETE 统一响应结构。
//
// 字段设计:
//   - Configured:bool,未配置时 AppID/CreatedAt/UpdatedAt 都为零值,前端据此切换"未配置"空态
//   - AppID:明文回传(非敏感,飞书后台公开可见)
//   - RedirectURI:提醒 admin 去飞书开放平台白名单加这条 URL
//   - 永不回传 AppSecret —— 即使本账号填过也只读不回
type feishuConfigResponse struct {
	Configured  bool   `json:"configured"`
	AppID       string `json:"app_id,omitempty"`
	RedirectURI string `json:"redirect_uri"`
	UpdatedAt   *int64 `json:"updated_at,omitempty"` // unix seconds
	CreatedAt   *int64 `json:"created_at,omitempty"`
}

// GetConfig GET /api/v2/orgs/:slug/integrations/feishu/config
// 返 (Configured, AppID, RedirectURI)。未配置时 Configured=false 其他 omit。
func (h *FeishuConfigHandler) GetConfig(c *gin.Context) {
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "missing org context", "")
		return
	}
	cfg, err := h.configRepo.GetByOrg(c.Request.Context(), org.ID)
	if err != nil {
		response.InternalServerError(c, "load feishu config failed", err.Error())
		return
	}
	resp := feishuConfigResponse{
		RedirectURI: h.redirectURI,
	}
	if cfg != nil {
		resp.Configured = true
		resp.AppID = cfg.AppID
		cu := cfg.CreatedAt.Unix()
		uu := cfg.UpdatedAt.Unix()
		resp.CreatedAt = &cu
		resp.UpdatedAt = &uu
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}

// feishuConfigPutRequest PUT 的请求体。app_secret 必填(即使只改 app_id 也要重填 —— 强迫 admin
// 意识到自己在覆盖凭证,防止看不见的半态更新)。
type feishuConfigPutRequest struct {
	AppID     string `json:"app_id" binding:"required,min=1,max=64"`
	AppSecret string `json:"app_secret" binding:"required,min=1,max=256"`
}

// PutConfig PUT /api/v2/orgs/:slug/integrations/feishu/config
// 写入或更新本 org 的飞书 App 凭证。幂等,重复 PUT 同一内容无副作用。
func (h *FeishuConfigHandler) PutConfig(c *gin.Context) {
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "missing org context", "")
		return
	}
	var req feishuConfigPutRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body", err.Error())
		return
	}
	saved, err := h.configRepo.Upsert(c.Request.Context(), &intgmodel.OrgFeishuConfig{
		OrgID:     org.ID,
		AppID:     req.AppID,
		AppSecret: req.AppSecret,
	})
	if err != nil {
		response.InternalServerError(c, "save feishu config failed", err.Error())
		return
	}
	cu := saved.CreatedAt.Unix()
	uu := saved.UpdatedAt.Unix()
	c.JSON(http.StatusOK, response.BaseResponse{
		Code: http.StatusOK, Message: "saved",
		Result: feishuConfigResponse{
			Configured:  true,
			AppID:       saved.AppID,
			RedirectURI: h.redirectURI,
			CreatedAt:   &cu,
			UpdatedAt:   &uu,
		},
	})
}

// DeleteConfig DELETE /api/v2/orgs/:slug/integrations/feishu/config
// 清空 org 飞书配置。幂等(本来就没配置也返 200)。
// 不联动清 user_integrations —— 让 admin 显式选择是否让成员继续保留已有 token。
func (h *FeishuConfigHandler) DeleteConfig(c *gin.Context) {
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "missing org context", "")
		return
	}
	if err := h.configRepo.Delete(c.Request.Context(), org.ID); err != nil {
		response.InternalServerError(c, "delete feishu config failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code: http.StatusOK, Message: "deleted",
		Result: feishuConfigResponse{
			Configured:  false,
			RedirectURI: h.redirectURI,
		},
	})
}
