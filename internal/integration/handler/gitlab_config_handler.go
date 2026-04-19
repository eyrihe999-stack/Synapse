// gitlab_config_handler.go GitLab 实例配置的 admin 端点。
//
// 路由(在 router.go 里挂):
//
//	GET    /api/v2/orgs/:slug/integrations/gitlab/config
//	PUT    /api/v2/orgs/:slug/integrations/gitlab/config
//	DELETE /api/v2/orgs/:slug/integrations/gitlab/config
//
// 权限:PermIntegrationManage(和飞书 config 对齐)。GET 放宽到成员,让普通用户也能看到 configured 标志。
//
// base_url 不是敏感字段(公开的 GitLab 实例地址),GET 直接回;InsecureSkipVerify 同样公开。
// 所以无需像飞书 app_secret 那样做只写不读。
package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	intgmodel "github.com/eyrihe999-stack/Synapse/internal/integration/model"
	intgrepo "github.com/eyrihe999-stack/Synapse/internal/integration/repository"
	orghandler "github.com/eyrihe999-stack/Synapse/internal/organization/handler"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
	"github.com/eyrihe999-stack/Synapse/pkg/sourceadapter/gitlab"
)

// GitLabConfigHandler admin 端 GitLab 实例配置 CRUD。
type GitLabConfigHandler struct {
	configRepo intgrepo.GitLabConfigRepository
}

// NewGitLabConfigHandler 构造。
func NewGitLabConfigHandler(configRepo intgrepo.GitLabConfigRepository) *GitLabConfigHandler {
	return &GitLabConfigHandler{configRepo: configRepo}
}

// gitlabConfigResponse GET/PUT/DELETE 统一响应结构。
type gitlabConfigResponse struct {
	Configured         bool   `json:"configured"`
	BaseURL            string `json:"base_url,omitempty"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify"`
	UpdatedAt          *int64 `json:"updated_at,omitempty"` // unix seconds
	CreatedAt          *int64 `json:"created_at,omitempty"`
}

// GetConfig GET /api/v2/orgs/:slug/integrations/gitlab/config
// 返 (Configured, BaseURL, InsecureSkipVerify)。未配置时 Configured=false 其他 omit。
func (h *GitLabConfigHandler) GetConfig(c *gin.Context) {
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "missing org context", "")
		return
	}
	cfg, err := h.configRepo.GetByOrg(c.Request.Context(), org.ID)
	if err != nil {
		response.InternalServerError(c, "load gitlab config failed", err.Error())
		return
	}
	resp := gitlabConfigResponse{}
	if cfg != nil {
		resp.Configured = true
		resp.BaseURL = cfg.BaseURL
		resp.InsecureSkipVerify = cfg.InsecureSkipVerify
		cu := cfg.CreatedAt.Unix()
		uu := cfg.UpdatedAt.Unix()
		resp.CreatedAt = &cu
		resp.UpdatedAt = &uu
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}

// gitlabConfigPutRequest PUT 的请求体。
type gitlabConfigPutRequest struct {
	BaseURL            string `json:"base_url" binding:"required,min=8,max=255"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify"`
}

// PutConfig PUT /api/v2/orgs/:slug/integrations/gitlab/config
// 写入或更新本 org 的 GitLab 实例配置。幂等。
// 保存前校验 BaseURL 格式(必须含 /api/v)—— 早失败避免错误配置进库导致后续 Connect 全炸。
func (h *GitLabConfigHandler) PutConfig(c *gin.Context) {
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "missing org context", "")
		return
	}
	var req gitlabConfigPutRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body", err.Error())
		return
	}
	req.BaseURL = strings.TrimSpace(req.BaseURL)
	// 复用 gitlab.Config.Validate 统一校验口径(含 /api/v 检查)。
	if err := (&gitlab.Config{BaseURL: req.BaseURL, InsecureSkipVerify: req.InsecureSkipVerify}).Validate(); err != nil {
		response.BadRequest(c, "invalid base_url", err.Error())
		return
	}
	saved, err := h.configRepo.Upsert(c.Request.Context(), &intgmodel.OrgGitLabConfig{
		OrgID:              org.ID,
		BaseURL:            req.BaseURL,
		InsecureSkipVerify: req.InsecureSkipVerify,
	})
	if err != nil {
		response.InternalServerError(c, "save gitlab config failed", err.Error())
		return
	}
	cu := saved.CreatedAt.Unix()
	uu := saved.UpdatedAt.Unix()
	c.JSON(http.StatusOK, response.BaseResponse{
		Code: http.StatusOK, Message: "saved",
		Result: gitlabConfigResponse{
			Configured:         true,
			BaseURL:            saved.BaseURL,
			InsecureSkipVerify: saved.InsecureSkipVerify,
			CreatedAt:          &cu,
			UpdatedAt:          &uu,
		},
	})
}

// DeleteConfig DELETE /api/v2/orgs/:slug/integrations/gitlab/config
// 清空 org GitLab 配置。幂等(本来就没配置也返 200)。
// 不联动清 user_integrations —— 让 admin 显式选择是否让成员保留已有 PAT 记录。
func (h *GitLabConfigHandler) DeleteConfig(c *gin.Context) {
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "missing org context", "")
		return
	}
	if err := h.configRepo.Delete(c.Request.Context(), org.ID); err != nil {
		response.InternalServerError(c, "delete gitlab config failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code: http.StatusOK, Message: "deleted",
		Result: gitlabConfigResponse{Configured: false},
	})
}
