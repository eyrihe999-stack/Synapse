// member_handler.go 成员管理 HTTP 端点。
package handler

import (
	"net/http"
	"strconv"

	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	"github.com/gin-gonic/gin"
)

// ListMembers 分页列出某 org 的成员,对应 GET /api/v2/orgs/:slug/members。
// 调用方须已通过 org 成员资格中间件校验。
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

// RemoveMember 踢出某位成员,对应 DELETE /api/v2/orgs/:slug/members/:user_id。
// operator 不能是 target,owner 不可被踢。
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

// LeaveOrg 当前用户主动退出 org,对应 DELETE /api/v2/orgs/:slug/members/me。
// Owner 调用此接口会被拒绝(需先走解散流程)。
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
