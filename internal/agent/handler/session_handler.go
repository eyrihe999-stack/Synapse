// session_handler.go session 管理 handler。
package handler

import (
	"net/http"
	"strconv"

	"github.com/eyrihe999-stack/Synapse/internal/agent/dto"
	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
	"github.com/gin-gonic/gin"
)

// ListSessions 分页列出用户在指定 agent 下的对话 session。
// GET /api/v2/orgs/:slug/agents/:owner_uid/:agent_slug/sessions
func (h *AgentHandler) ListSessions(c *gin.Context) {
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
	ownerUID, ok := parseUintParam(c, "owner_uid")
	if !ok {
		return
	}
	agentSlug := c.Param("agent_slug")

	// 解析 agent 获取 ID
	ag, err := h.registrySvc.LoadAgentByOwnerSlug(c.Request.Context(), ownerUID, agentSlug)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}

	//sayso-lint:ignore err-swallow
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	//sayso-lint:ignore err-swallow
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))

	list, total, err := h.chatSvc.ListSessions(c.Request.Context(), org.ID, userID, ag.ID, page, size)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: dto.PageResponse{
		Items: list, Total: total, Page: page, Size: size,
	}})
}

// GetSession 获取指定 session 的详情。
// GET /api/v2/orgs/:slug/sessions/:session_id
func (h *AgentHandler) GetSession(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	sessionID := c.Param("session_id")
	resp, err := h.chatSvc.GetSession(c.Request.Context(), sessionID, userID)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}

// GetSessionMessages 分页获取指定 session 的消息列表。
// GET /api/v2/orgs/:slug/sessions/:session_id/messages
func (h *AgentHandler) GetSessionMessages(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	sessionID := c.Param("session_id")
	//sayso-lint:ignore err-swallow
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	//sayso-lint:ignore err-swallow
	size, _ := strconv.Atoi(c.DefaultQuery("size", "50"))

	list, total, err := h.chatSvc.GetSessionMessages(c.Request.Context(), sessionID, userID, page, size)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: dto.PageResponse{
		Items: list, Total: total, Page: page, Size: size,
	}})
}

// DeleteSession 删除指定 session 及其消息。
// DELETE /api/v2/orgs/:slug/sessions/:session_id
func (h *AgentHandler) DeleteSession(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	sessionID := c.Param("session_id")
	if err := h.chatSvc.DeleteSession(c.Request.Context(), sessionID, userID); err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok"})
}
