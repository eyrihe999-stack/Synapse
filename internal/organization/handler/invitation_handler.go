// invitation_handler.go 邀请相关 HTTP 端点。
//
// 分两类:
//   - org-scoped(需要 OrgContextMiddleware):SearchInvitees / Create / ListByOrg / Revoke
//   - user-scoped(只需 JWT):ListMine / Accept / Reject
package handler

import (
	"net/http"
	"strconv"

	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/organization/dto"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
	"github.com/gin-gonic/gin"
)

// ─── org-scoped 邀请操作 ──────────────────────────────────────────────────────

// SearchInvitees POST /api/v2/orgs/:slug/invitations/search-invitees
// 需要 PermMemberInvite。
func (h *OrgHandler) SearchInvitees(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	var req dto.SearchInviteesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body", err.Error())
		return
	}
	resp, err := h.inviteSvc.SearchInvitees(c.Request.Context(), req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "ok",
		Result:  resp,
	})
}

// CreateInvitation POST /api/v2/orgs/:slug/invitations
// 需要 PermMemberInvite。
func (h *OrgHandler) CreateInvitation(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	inviterID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	var req dto.CreateInvitationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body", err.Error())
		return
	}
	resp, err := h.inviteSvc.CreateInvitation(c.Request.Context(), inviterID, org.ID, req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "Invitation created",
		Result:  resp,
	})
}

// ListOrgInvitations GET /api/v2/orgs/:slug/invitations
// 需要 PermMemberInvite。
func (h *OrgHandler) ListOrgInvitations(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	page, size := parsePagination(c)
	resp, err := h.inviteSvc.ListByOrg(c.Request.Context(), org.ID, page, size)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "ok",
		Result:  resp,
	})
}

// RevokeInvitation DELETE /api/v2/orgs/:slug/invitations/:id
// 需要 PermMemberInvite(service 层会检查发起人或 PermMemberRemove)。
// 注意:这里做简化,权限检查由 middleware 走 PermMemberInvite,service 层允许发起人或管理员撤销。
func (h *OrgHandler) RevokeInvitation(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	operatorID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid invitation id", err.Error())
		return
	}
	if err := h.inviteSvc.Revoke(c.Request.Context(), operatorID, id); err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "Invitation revoked",
	})
}

// ─── user-scoped 邀请操作 ─────────────────────────────────────────────────────

// ListMyInvitations GET /api/v2/invitations/mine
// 仅 JWT,不需要 org context。
func (h *OrgHandler) ListMyInvitations(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	page, size := parsePagination(c)
	resp, err := h.inviteSvc.ListMine(c.Request.Context(), userID, page, size)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "ok",
		Result:  resp,
	})
}

// AcceptInvitation 接受邀请的 HTTP 端点,对应 POST /api/v2/invitations/:id/accept。
// 普通邀请接受后插入 member,所有权转让邀请接受后触发 owner 交接事务。
func (h *OrgHandler) AcceptInvitation(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid invitation id", err.Error())
		return
	}
	if err := h.inviteSvc.Accept(c.Request.Context(), userID, id); err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "Invitation accepted",
	})
}

// RejectInvitation 拒绝邀请的 HTTP 端点,对应 POST /api/v2/invitations/:id/reject。
// 拒绝后邀请状态变为 rejected,可以重新发起新邀请。
func (h *OrgHandler) RejectInvitation(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid invitation id", err.Error())
		return
	}
	if err := h.inviteSvc.Reject(c.Request.Context(), userID, id); err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "Invitation rejected",
	})
}
