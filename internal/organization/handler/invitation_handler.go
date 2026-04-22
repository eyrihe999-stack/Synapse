// invitation_handler.go 组织邀请 HTTP 端点。
//
// 路由:
//   POST   /api/v2/orgs/:slug/invitations                   创建邀请 + 发邮件
//   GET    /api/v2/orgs/:slug/invitations                   列出邀请(可按 status 过滤)
//   DELETE /api/v2/orgs/:slug/invitations/:id               撤销邀请
//   POST   /api/v2/orgs/:slug/invitations/:id/resend        重发邮件(换新 token)
//
//   GET    /api/v2/invitations/preview?token=xxx            未登录预览(前端落地页用)
//   POST   /api/v2/invitations/accept                       登录后接受邀请
//
// org context 下的四条接口要求调用方是成员(OrgContextMiddleware 保证),
// 不分 owner/admin/member 权限。preview/accept 由独立路由组挂载。
package handler

import (
	"net/http"
	"strconv"

	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	"github.com/eyrihe999-stack/Synapse/internal/organization/dto"
	"github.com/gin-gonic/gin"
)

// CreateInvitation POST /api/v2/orgs/:slug/invitations
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
	resp, err := h.invitationSvc.Create(c.Request.Context(), org.ID, inviterID, req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "Invitation sent",
		Result:  resp,
	})
}

// ListInvitations GET /api/v2/orgs/:slug/invitations?status=pending&page=1&size=20
func (h *OrgHandler) ListInvitations(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	page, size := parsePagination(c)
	statusFilter := c.Query("status")
	resp, err := h.invitationSvc.List(c.Request.Context(), org.ID, statusFilter, page, size)
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
func (h *OrgHandler) RevokeInvitation(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	invID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid invitation id", err.Error())
		return
	}
	if err := h.invitationSvc.Revoke(c.Request.Context(), org.ID, invID); err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "Invitation revoked",
	})
}

// ResendInvitation POST /api/v2/orgs/:slug/invitations/:id/resend
func (h *OrgHandler) ResendInvitation(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	invID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid invitation id", err.Error())
		return
	}
	resp, err := h.invitationSvc.Resend(c.Request.Context(), org.ID, invID)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "Invitation resent",
		Result:  resp,
	})
}

// PreviewInvitation GET /api/v2/invitations/preview?token=xxx
// 未登录也可以调 —— 前端落地页据此渲染 "XX 邀请你加入 YY"。
func (h *OrgHandler) PreviewInvitation(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	token := c.Query("token")
	if token == "" {
		response.BadRequest(c, "token query param required", "")
		return
	}
	resp, err := h.invitationSvc.Preview(c.Request.Context(), token)
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

// SearchInviteCandidates GET /api/v2/orgs/:slug/invitations/search?type=email|user_id|name&q=xxx
//
// 邀请对话框用:按 email / user_id 精确匹配或按 display_name 模糊匹配,返回候选用户。
// 调用方需是该 org 成员(OrgContextMiddleware 保证)。
//
// 匹配规则(service 层校验并强制 limit):
//   - type=email   → 全 email 精确(小写归一),limit 1
//   - type=user_id → 按主键精确(需纯十进制数字),limit 1
//   - type=name    → display_name LIKE '%q%',limit 10,query 至少 2 字符
//
// 结果里 is_member / has_pending_invite 被后端直接标记,前端据此灰掉不可邀请的候选。
func (h *OrgHandler) SearchInviteCandidates(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	searchType := c.Query("type")
	query := c.Query("q")
	resp, err := h.invitationSvc.SearchCandidates(c.Request.Context(), org.ID, searchType, query)
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

// AcceptInvitation POST /api/v2/invitations/accept
// 邮件链接入口 —— body 里带 raw token。需要登录态。
func (h *OrgHandler) AcceptInvitation(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	var req dto.AcceptInvitationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body", err.Error())
		return
	}
	resp, err := h.invitationSvc.Accept(c.Request.Context(), userID, req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "Invitation accepted",
		Result:  resp,
	})
}

// ListMyInvitations GET /api/v2/invitations/mine?status=pending|accepted|rejected|expired|revoked
// 当前登录用户收到的邀请(收件箱)。status 为空时返全部状态,按 created_at DESC。
func (h *OrgHandler) ListMyInvitations(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	statusFilter := c.Query("status")
	resp, err := h.invitationSvc.ListMyInvitations(c.Request.Context(), userID, statusFilter)
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

// ListSentInvitations GET /api/v2/invitations/sent?status=...
// 当前登录用户作为 inviter 发出的邀请(跨 org 发件箱)。
func (h *OrgHandler) ListSentInvitations(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	statusFilter := c.Query("status")
	resp, err := h.invitationSvc.ListSentInvitations(c.Request.Context(), userID, statusFilter)
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

// AcceptInvitationByID POST /api/v2/invitations/:id/accept
// 站内收件箱接受入口。用 invitation id 而非 token,权限仍以 email 匹配兜底。
func (h *OrgHandler) AcceptInvitationByID(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	invID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid invitation id", err.Error())
		return
	}
	resp, err := h.invitationSvc.AcceptByID(c.Request.Context(), invID, userID)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "Invitation accepted",
		Result:  resp,
	})
}

// RejectInvitation POST /api/v2/invitations/:id/reject
// 被邀请人拒绝 pending 邀请。权限:登录 email 必须匹配邀请 email。
func (h *OrgHandler) RejectInvitation(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	invID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid invitation id", err.Error())
		return
	}
	if err := h.invitationSvc.Reject(c.Request.Context(), invID, userID); err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "Invitation rejected",
	})
}
