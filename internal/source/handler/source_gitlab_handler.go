// source_gitlab_handler.go gitlab_repo 同步源专属 HTTP 端点。
//
// 路由(在 router.go 里挂 RequirePerm("integration.gitlab.manage")):
//
//	POST   /api/v2/orgs/:slug/sources/gitlab               创建一条 gitlab_repo
//	DELETE /api/v2/orgs/:slug/sources/gitlab/:source_id    删除
//	POST   /api/v2/orgs/:slug/sources/gitlab/:source_id/resync   触发全量重新同步
//
// 与现有 /api/v2/orgs/:slug/sources/* 端点的关系:GET 列表 / GET 单条 / PATCH visibility 共用旧端点
// (通过 source_id),不需要重复;**只有创建 / 删除 / 重同步**这三个写动作走 GitLab 专属端点。
package handler

import (
	"net/http"

	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	orghandler "github.com/eyrihe999-stack/Synapse/internal/organization/handler"
	"github.com/eyrihe999-stack/Synapse/internal/source/dto"
	"github.com/gin-gonic/gin"
)

// CreateGitLabSource POST /api/v2/orgs/:slug/sources/gitlab
//
// 鉴权:RequirePerm("integration.gitlab.manage")(默认只 owner)
//
// body: dto.CreateGitLabSourceRequest
// 返回 dto.CreateGitLabSourceResponse —— webhook_secret 字段是**唯一一次**返明文,
// 前端必须立刻提示 owner 复制粘贴到 GitLab Project → Settings → Webhooks。
func (h *SourceHandler) CreateGitLabSource(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	var req dto.CreateGitLabSourceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body", err.Error())
		return
	}
	resp, err := h.svc.CreateGitLabSource(c.Request.Context(), org.ID, userID, req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "GitLab source created. Save the webhook_secret now — it will not be shown again.",
		Result:  resp,
	})
}

// DeleteGitLabSource DELETE /api/v2/orgs/:slug/sources/gitlab/:source_id
// 鉴权:RequirePerm("integration.gitlab.manage")
func (h *SourceHandler) DeleteGitLabSource(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	sourceID, err := parseSourceID(c)
	if err != nil {
		return
	}
	if err := h.svc.DeleteGitLabSource(c.Request.Context(), org.ID, sourceID, userID); err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "GitLab source deleted"})
}

// GetGitLabSyncStatus GET /api/v2/orgs/:slug/sources/gitlab/:source_id/sync-status
// 鉴权:RequirePerm("integration.gitlab.manage")
//
// 前端轮询此端点展示同步进度。从未同步过返 status="never"。
func (h *SourceHandler) GetGitLabSyncStatus(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	sourceID, err := parseSourceID(c)
	if err != nil {
		return
	}
	resp, err := h.svc.GetGitLabSyncStatus(c.Request.Context(), org.ID, sourceID, userID)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}

// TriggerGitLabResync POST /api/v2/orgs/:slug/sources/gitlab/:source_id/resync
// 鉴权:RequirePerm("integration.gitlab.manage")
func (h *SourceHandler) TriggerGitLabResync(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	sourceID, err := parseSourceID(c)
	if err != nil {
		return
	}
	resp, err := h.svc.TriggerGitLabResync(c.Request.Context(), org.ID, sourceID, userID)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "Resync queued", Result: resp})
}
