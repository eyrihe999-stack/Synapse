// member_handler.go 成员管理 HTTP 端点。
package handler

import (
	"net/http"
	"strconv"

	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/organization/dto"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
	"github.com/gin-gonic/gin"
)

// ListMembers GET /api/v2/orgs/:slug/members
// 成员身份即可调用,不需要额外权限。
func (h *OrgHandler) ListMembers(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	page, size := parsePagination(c)
	resp, err := h.memberSvc.ListMembers(c.Request.Context(), org.ID, page, size)
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

// RemoveMember DELETE /api/v2/orgs/:slug/members/:user_id
// 需要 PermMemberRemove。
func (h *OrgHandler) RemoveMember(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	operatorID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	targetIDStr := c.Param("user_id")
	targetID, err := strconv.ParseUint(targetIDStr, 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid user_id", err.Error())
		return
	}
	if err := h.memberSvc.RemoveMember(c.Request.Context(), operatorID, org.ID, targetID); err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "Member removed",
	})
}

// LeaveOrg DELETE /api/v2/orgs/:slug/members/me
// 成员主动退出,owner 拒绝。
func (h *OrgHandler) LeaveOrg(c *gin.Context) {
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
	if err := h.memberSvc.LeaveOrg(c.Request.Context(), org.ID, userID); err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "Left organization",
	})
}

// AssignMemberRole PATCH /api/v2/orgs/:slug/members/:user_id/role
// 需要 PermMemberRoleAssign。
func (h *OrgHandler) AssignMemberRole(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	operatorID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	targetIDStr := c.Param("user_id")
	targetID, err := strconv.ParseUint(targetIDStr, 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid user_id", err.Error())
		return
	}
	var req dto.AssignMemberRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body", err.Error())
		return
	}
	if err := h.memberSvc.AssignRole(c.Request.Context(), operatorID, org.ID, targetID, req.RoleID); err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "Role assigned",
	})
}
