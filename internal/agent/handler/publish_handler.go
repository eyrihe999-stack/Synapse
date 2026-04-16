// publish_handler.go agent 发布接口 handler。
package handler

import (
	"net/http"
	"strconv"

	"github.com/eyrihe999-stack/Synapse/internal/agent/dto"
	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
	"github.com/gin-gonic/gin"
)

// SubmitPublish 提交 agent 发布申请到 org。POST /api/v2/orgs/:slug/agent-publishes [agent.publish]
func (h *AgentHandler) SubmitPublish(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	var req dto.PublishAgentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid body", err.Error())
		return
	}
	// OrgInfo 已经由 OrgContextMiddleware 通过 OrgPort.GetOrgBySlug 装填,
	// 其中的 RequireAgentReview 字段直接反映 org 当前设置,不用再查一次。
	resp, err := h.publishSvc.Submit(c.Request.Context(), userID, org, org.RequireAgentReview, req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}

// ListPublishes 分页列出 org 内的 agent publish 记录。GET /api/v2/orgs/:slug/agent-publishes
func (h *AgentHandler) ListPublishes(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	status := c.Query("status")
	//sayso-lint:ignore err-swallow
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1")) // 解析失败回退为 0,下游 service 会兜底
	//sayso-lint:ignore err-swallow
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	list, total, err := h.publishSvc.ListByOrg(c.Request.Context(), org.ID, status, page, size)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: dto.PageResponse{
		Items: list, Total: total, Page: page, Size: size,
	}})
}

// ApprovePublish 审核通过一条 pending publish。POST /api/v2/orgs/:slug/agent-publishes/:id/approve [agent.review]
func (h *AgentHandler) ApprovePublish(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	publishID, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req dto.ReviewPublishRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid body", err.Error())
		return
	}
	resp, err := h.publishSvc.Approve(c.Request.Context(), publishID, userID, req.Note)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}

// RejectPublish 审核拒绝一条 pending publish。POST /api/v2/orgs/:slug/agent-publishes/:id/reject [agent.review]
func (h *AgentHandler) RejectPublish(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	publishID, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req dto.ReviewPublishRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid body", err.Error())
		return
	}
	resp, err := h.publishSvc.Reject(c.Request.Context(), publishID, userID, req.Note)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}

// RevokePublish 提交者主动下架自己发布的 publish。DELETE /api/v2/orgs/:slug/agent-publishes/:id [agent.unpublish.self]
func (h *AgentHandler) RevokePublish(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	publishID, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	if err := h.publishSvc.RevokeByAuthor(c.Request.Context(), publishID, userID); err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok"})
}

// BanPublish 管理员强制下架 publish(reason=admin_banned)。POST /api/v2/orgs/:slug/agent-publishes/:id/ban [agent.ban]
func (h *AgentHandler) BanPublish(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	publishID, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	if err := h.publishSvc.RevokeByAdmin(c.Request.Context(), publishID, userID); err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok"})
}
