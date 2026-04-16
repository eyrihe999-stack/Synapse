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

// SubmitPublish 提交 agent 发布请求。
// POST /api/v2/orgs/:slug/agent-publishes
func (h *AgentHandler) SubmitPublish(c *gin.Context) {
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
	resp, err := h.publishSvc.Submit(c.Request.Context(), userID, org, org.RequireAgentReview, req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}

// ListPublishes 分页列出组织下的 agent 发布记录。
// GET /api/v2/orgs/:slug/agent-publishes
func (h *AgentHandler) ListPublishes(c *gin.Context) {
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	status := c.Query("status")
	//sayso-lint:ignore err-swallow
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
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

// ApprovePublish 审核通过 agent 发布请求。
// POST /api/v2/orgs/:slug/agent-publishes/:id/approve
func (h *AgentHandler) ApprovePublish(c *gin.Context) {
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

// RejectPublish 审核拒绝 agent 发布请求。
// POST /api/v2/orgs/:slug/agent-publishes/:id/reject
func (h *AgentHandler) RejectPublish(c *gin.Context) {
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

// RevokePublish 撤销已提交的 agent 发布。
// DELETE /api/v2/orgs/:slug/agent-publishes/:id
func (h *AgentHandler) RevokePublish(c *gin.Context) {
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
