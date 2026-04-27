// agent_handler.go agent CRUD + rotate-key 具体 handler 方法。
package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/agents"
	"github.com/eyrihe999-stack/Synapse/internal/agents/dto"
	"github.com/eyrihe999-stack/Synapse/internal/agents/model"
	"github.com/eyrihe999-stack/Synapse/internal/agents/service"
	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	orghandler "github.com/eyrihe999-stack/Synapse/internal/organization/handler"
)

// OnlineChecker handler 查 agent 在线状态用的窄接口,main.go 注入 *transport/service.LocalHub。
type OnlineChecker interface {
	IsOnline(agentID string) bool
}

// Create POST /api/v2/orgs/:slug/agents
//
// 响应:200 + CreateAgentResp(含一次性明文 apikey)
func (h *Handler) Create(c *gin.Context, online OnlineChecker) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}

	var req dto.CreateAgentReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code:    agents.CodeAgentInvalidRequest,
			Message: "Invalid request body",
			Error:   err.Error(),
		})
		return
	}

	out, err := h.svc.Create(c.Request.Context(), service.CreateInput{
		OrgID:       org.ID,
		CallerUID:   userID,
		DisplayName: req.DisplayName,
	})
	if err != nil {
		h.handleServiceError(c, err)
		return
	}

	resp := dto.CreateAgentResp{
		Agent:  toAgentResp(out.Agent, online),
		APIKey: out.APIKey,
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code: http.StatusOK, Message: "ok", Result: resp,
	})
}

// List GET /api/v2/orgs/:slug/agents?offset=0&limit=50
func (h *Handler) List(c *gin.Context, online OnlineChecker) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}

	offset, _ := strconv.Atoi(c.Query("offset"))
	limit, _ := strconv.Atoi(c.Query("limit"))

	rows, total, err := h.svc.List(c.Request.Context(), userID, org.ID, offset, limit)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	items := make([]dto.AgentResp, 0, len(rows))
	for _, a := range rows {
		items = append(items, toAgentResp(a, online))
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code: http.StatusOK, Message: "ok",
		Result: dto.ListAgentResp{
			Items: items, Total: total, Offset: offset, Limit: limit,
		},
	})
}

// Get GET /api/v2/orgs/:slug/agents/:agent_id
func (h *Handler) Get(c *gin.Context, online OnlineChecker) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	agentID := c.Param("agent_id")
	a, err := h.svc.Get(c.Request.Context(), userID, org.ID, agentID)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code: http.StatusOK, Message: "ok", Result: toAgentResp(a, online),
	})
}

// Update PATCH /api/v2/orgs/:slug/agents/:agent_id
func (h *Handler) Update(c *gin.Context, online OnlineChecker) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	agentID := c.Param("agent_id")

	var req dto.UpdateAgentReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code:    agents.CodeAgentInvalidRequest,
			Message: "Invalid request body",
			Error:   err.Error(),
		})
		return
	}

	a, err := h.svc.Update(c.Request.Context(), userID, org.ID, agentID, service.UpdateInput{
		DisplayName: req.DisplayName,
		Enabled:     req.Enabled,
	})
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code: http.StatusOK, Message: "ok", Result: toAgentResp(a, online),
	})
}

// Delete DELETE /api/v2/orgs/:slug/agents/:agent_id
func (h *Handler) Delete(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	agentID := c.Param("agent_id")
	if err := h.svc.Delete(c.Request.Context(), userID, org.ID, agentID); err != nil {
		h.handleServiceError(c, err)
		return
	}
	response.Success(c, "ok", nil)
}

// RotateKey POST /api/v2/orgs/:slug/agents/:agent_id/rotate-key
//
// 响应:200 + RotateKeyResp(含新 apikey 明文,只返一次)
func (h *Handler) RotateKey(c *gin.Context, online OnlineChecker) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	agentID := c.Param("agent_id")
	a, newKey, err := h.svc.RotateKey(c.Request.Context(), userID, org.ID, agentID)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code: http.StatusOK, Message: "ok",
		Result: dto.RotateKeyResp{
			Agent:  toAgentResp(a, online),
			APIKey: newKey,
		},
	})
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// toAgentResp model → DTO。apikey 永不出现在响应里。
func toAgentResp(a *model.Agent, online OnlineChecker) dto.AgentResp {
	out := dto.AgentResp{
		ID:           a.ID,
		PrincipalID:  a.PrincipalID,
		AgentID:      a.AgentID,
		OrgID:        a.OrgID,
		Kind:         a.Kind,
		DisplayName:  a.DisplayName,
		Enabled:      a.Enabled,
		CreatedByUID: a.CreatedByUID,
		CreatedAt:    a.CreatedAt.Unix(),
		UpdatedAt:    a.UpdatedAt.Unix(),
		// 内置系统 agent(top-orchestrator)在 backend 进程内常驻跑 tool-loop,
		// 不走 transport 连 Hub,IsOnline 永远 false。只要服务在跑就视为在线。
		Online: a.AgentID == agents.TopOrchestratorAgentID || (online != nil && online.IsOnline(a.AgentID)),
	}
	if a.LastSeenAt != nil {
		t := a.LastSeenAt.Unix()
		out.LastSeenAt = &t
	}
	if a.RotatedAt != nil {
		t := a.RotatedAt.Unix()
		out.RotatedAt = &t
	}
	return out
}
