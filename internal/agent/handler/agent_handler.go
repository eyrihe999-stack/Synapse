// agent_handler.go Agent CRUD HTTP handler。
package handler

import (
	"net/http"

	"github.com/eyrihe999-stack/Synapse/internal/agent/dto"
	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
	"github.com/gin-gonic/gin"
)

// CreateAgent 创建一个新的 agent。
// POST /api/v2/agents
func (h *AgentHandler) CreateAgent(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	var req dto.CreateAgentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid body", err.Error())
		return
	}
	resp, err := h.registrySvc.CreateAgent(c.Request.Context(), userID, req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}

// ListMyAgents 列出当前用户拥有的所有 agent。
// GET /api/v2/agents/mine
func (h *AgentHandler) ListMyAgents(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	list, err := h.registrySvc.ListMyAgents(c.Request.Context(), userID)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: list})
}

// GetAgent 根据 ID 获取 agent 详情。
// GET /api/v2/agents/:id
func (h *AgentHandler) GetAgent(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	agentID, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	resp, err := h.registrySvc.GetAgentByID(c.Request.Context(), agentID, userID)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}

// UpdateAgent 部分更新 agent 配置。
// PATCH /api/v2/agents/:id
func (h *AgentHandler) UpdateAgent(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	agentID, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req dto.UpdateAgentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid body", err.Error())
		return
	}
	resp, err := h.registrySvc.UpdateAgent(c.Request.Context(), agentID, userID, req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}

// DeleteAgent 删除指定 agent。
// DELETE /api/v2/agents/:id
func (h *AgentHandler) DeleteAgent(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	agentID, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	if err := h.registrySvc.DeleteAgent(c.Request.Context(), agentID, userID); err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok"})
}
