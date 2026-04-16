// audit_handler.go 审计查询 handler。
package handler

import (
	"net/http"
	"strconv"

	"github.com/eyrihe999-stack/Synapse/internal/agent"
	"github.com/eyrihe999-stack/Synapse/internal/agent/dto"
	"github.com/eyrihe999-stack/Synapse/internal/agent/model"
	"github.com/eyrihe999-stack/Synapse/internal/agent/repository"
	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
	"github.com/gin-gonic/gin"
)

// 权限点字符串常量(定义在 organization 模块,这里用字符串字面量避免反向依赖)。
const (
	permAuditRead     = "audit.read"
	permAuditReadSelf = "audit.read.self"
)

// ListAudits 分页列出 org 内的 invocation 审计记录。GET /api/v2/orgs/:slug/audits [audit.read]
func (h *AgentHandler) ListAudits(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	filter := buildAuditFilter(c)
	//sayso-lint:ignore err-swallow
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1")) // 解析失败回退为 0,下游 SanitizePaging 会兜底
	//sayso-lint:ignore err-swallow
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	list, total, err := h.auditSvc.ListByOrg(c.Request.Context(), org.ID, filter, page, size)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	items := make([]dto.InvocationResponse, 0, len(list))
	for _, inv := range list {
		items = append(items, invocationToDTO(inv))
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: dto.PageResponse{
		Items: items, Total: total, Page: page, Size: size,
	}})
}

// ListMyAudits GET /api/v2/orgs/:slug/audits/mine [audit.read.self]
// 仅返回调用者 = 当前用户的记录。
func (h *AgentHandler) ListMyAudits(c *gin.Context) {
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
	filter := buildAuditFilter(c)
	filter.CallerUserID = userID
	//sayso-lint:ignore err-swallow
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1")) // 解析失败回退为 0,下游 SanitizePaging 会兜底
	//sayso-lint:ignore err-swallow
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	list, total, err := h.auditSvc.ListByOrg(c.Request.Context(), org.ID, filter, page, size)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	items := make([]dto.InvocationResponse, 0, len(list))
	for _, inv := range list {
		items = append(items, invocationToDTO(inv))
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: dto.PageResponse{
		Items: items, Total: total, Page: page, Size: size,
	}})
}

// GetAuditDetail GET /api/v2/orgs/:slug/audits/:invocation_id
//
// 权限:
//   - audit.read 全 org 可见,payload 也可见
//   - audit.read.self:仅调用者=我 或 agent 作者=我 可见
func (h *AgentHandler) GetAuditDetail(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	membership, _ := GetMembership(c)
	if membership == nil {
		response.InternalServerError(c, "Missing membership", "")
		return
	}
	invID := c.Param("invocation_id")
	if invID == "" {
		response.BadRequest(c, "Missing invocation id", "")
		return
	}
	hasFullRead := membership.Has(permAuditRead)
	inv, payload, err := h.auditSvc.GetByInvocationID(c.Request.Context(), invID, hasFullRead)
	if err != nil {
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeInvocationNotFound, Message: "Invocation not found"})
		return
	}
	// audit.read.self:非调用者/作者拒绝
	if !hasFullRead {
		if !membership.Has(permAuditReadSelf) {
			c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentPermissionDenied, Message: "Permission denied"})
			return
		}
		if inv.CallerUserID != userID && inv.AgentOwnerUserID != userID {
			c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentPermissionDenied, Message: "Permission denied"})
			return
		}
	}
	resp := dto.InvocationDetailResponse{
		Invocation: invocationToDTO(inv),
	}
	if payload != nil {
		resp.RequestBody = string(payload.RequestBody)
		resp.ResponseBody = string(payload.ResponseBody)
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}

// buildAuditFilter 从 query string 构造 InvocationFilter。
func buildAuditFilter(c *gin.Context) repository.InvocationFilter {
	f := repository.InvocationFilter{}
	if v, err := strconv.ParseUint(c.Query("agent_id"), 10, 64); err == nil {
		f.AgentID = v
	}
	if v, err := strconv.ParseUint(c.Query("agent_owner_user_id"), 10, 64); err == nil {
		f.AgentOwnerUserID = v
	}
	return f
}

// invocationToDTO 把 invocation model 转为 dto。
// 与 service.invocationToDTO 同名但位于 handler 包(避免导出 service 内部函数)。
func invocationToDTO(inv *model.AgentInvocation) dto.InvocationResponse {
	resp := dto.InvocationResponse{
		InvocationID:     inv.InvocationID,
		TraceID:          inv.TraceID,
		OrgID:            inv.OrgID,
		CallerUserID:     inv.CallerUserID,
		CallerRoleName:   inv.CallerRoleName,
		AgentID:          inv.AgentID,
		AgentOwnerUserID: inv.AgentOwnerUserID,
		MethodName:       inv.MethodName,
		Transport:        inv.Transport,
		StartedAt:        inv.StartedAt.UnixMilli(),
		Status:           inv.Status,
		ErrorCode:        inv.ErrorCode,
		ErrorMessage:     inv.ErrorMessage,
		ClientIP:         inv.ClientIP,
	}
	if inv.FinishedAt != nil {
		resp.FinishedAt = inv.FinishedAt.UnixMilli()
	}
	if inv.LatencyMs != nil {
		resp.LatencyMs = *inv.LatencyMs
	}
	if inv.RequestSizeBytes != nil {
		resp.RequestSizeBytes = *inv.RequestSizeBytes
	}
	if inv.ResponseSizeBytes != nil {
		resp.ResponseSizeBytes = *inv.ResponseSizeBytes
	}
	return resp
}
