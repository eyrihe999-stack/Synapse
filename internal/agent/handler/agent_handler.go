// agent_handler.go Agent CRUD / Method / Secret / Health 的 HTTP handler。
package handler

import (
	"net/http"
	"strconv"

	"github.com/eyrihe999-stack/Synapse/internal/agent/dto"
	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
	"github.com/gin-gonic/gin"
)

// CreateAgent 注册新的 agent 并返回一次性明文 secret。POST /api/v2/agents
func (h *AgentHandler) CreateAgent(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
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

// ListMyAgents 列出当前用户拥有的所有 agent。GET /api/v2/agents/mine
func (h *AgentHandler) ListMyAgents(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
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

// GetAgent 按 ID 查询 agent 详情(作者视角)。GET /api/v2/agents/:id
func (h *AgentHandler) GetAgent(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
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

// UpdateAgent 部分更新 agent 元信息(限作者)。PATCH /api/v2/agents/:id
func (h *AgentHandler) UpdateAgent(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
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

// DeleteAgent 作者删除 agent,级联清理 methods/secret/publish。DELETE /api/v2/agents/:id
func (h *AgentHandler) DeleteAgent(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
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

// RotateSecret 生成新 secret 并返回一次性明文,旧 secret 保留 24 小时 grace。POST /api/v2/agents/:id/secret/rotate
func (h *AgentHandler) RotateSecret(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	agentID, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	resp, err := h.registrySvc.RotateSecret(c.Request.Context(), agentID, userID)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}

// GetHealth 返回 agent 当前健康状态快照(作者视角)。GET /api/v2/agents/:id/health
func (h *AgentHandler) GetHealth(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	agentID, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	resp, err := h.registrySvc.GetHealth(c.Request.Context(), agentID, userID)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}

// ─── Method ───────────────────────────────────────────────────────────────────

// ListMethods 列出某 agent 的所有 method。GET /api/v2/agents/:id/methods
func (h *AgentHandler) ListMethods(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	agentID, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	list, err := h.registrySvc.ListMethods(c.Request.Context(), agentID, userID)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: list})
}

// CreateMethod 给 agent 追加一条 method。POST /api/v2/agents/:id/methods
func (h *AgentHandler) CreateMethod(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	agentID, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req dto.CreateMethodRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid body", err.Error())
		return
	}
	resp, err := h.registrySvc.CreateMethod(c.Request.Context(), agentID, userID, req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}

// UpdateMethod 部分更新某条 method 的元信息。PATCH /api/v2/agents/:id/methods/:method_id
func (h *AgentHandler) UpdateMethod(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	agentID, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	methodID, ok := parseUintParam(c, "method_id")
	if !ok {
		return
	}
	var req dto.UpdateMethodRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid body", err.Error())
		return
	}
	resp, err := h.registrySvc.UpdateMethod(c.Request.Context(), agentID, methodID, userID, req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}

// DeleteMethod 删除 method,需保留至少 1 条。DELETE /api/v2/agents/:id/methods/:method_id
func (h *AgentHandler) DeleteMethod(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	agentID, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	methodID, ok := parseUintParam(c, "method_id")
	if !ok {
		return
	}
	if err := h.registrySvc.DeleteMethod(c.Request.Context(), agentID, methodID, userID); err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok"})
}

// ─── 工具 ────────────────────────────────────────────────────────────────────

// parseUintParam 从 gin path 参数解析 uint64。失败时已写响应并返回 (0, false)。
func parseUintParam(c *gin.Context, key string) (uint64, bool) {
	raw := c.Param(key)
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || v == 0 {
		response.BadRequest(c, "Invalid "+key, "")
		return 0, false
	}
	return v, true
}
