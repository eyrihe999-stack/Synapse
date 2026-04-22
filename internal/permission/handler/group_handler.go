// group_handler.go 权限组管理 HTTP 端点。
//
// 路由(全部挂在 /api/v2/orgs/:slug/groups,共享 OrgContextMiddleware):
//
//	GET    /api/v2/orgs/:slug/groups                  分页列出 org 下所有组
//	GET    /api/v2/orgs/:slug/groups/mine             列我加入的组(不分页)
//	POST   /api/v2/orgs/:slug/groups                  创建组(任何成员可建)
//	GET    /api/v2/orgs/:slug/groups/:group_id        查单个组详情
//	PATCH  /api/v2/orgs/:slug/groups/:group_id        改组(目前仅改名;owner-only)
//	DELETE /api/v2/orgs/:slug/groups/:group_id        删组(owner-only,级联删成员)
//	GET    /api/v2/orgs/:slug/groups/:group_id/members         分页列组成员
//	POST   /api/v2/orgs/:slug/groups/:group_id/members         加成员(owner-only)
//	DELETE /api/v2/orgs/:slug/groups/:group_id/members/:user_id  踢成员(owner 或本人)
//
// 所有接口要求调用方是 org 成员(OrgContextMiddleware 保证)。
// 组级 owner-only 检查在 service 层硬规则里做,不依赖中间件。
package handler

import (
	"net/http"
	"strconv"

	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	orghandler "github.com/eyrihe999-stack/Synapse/internal/organization/handler"
	"github.com/eyrihe999-stack/Synapse/internal/permission"
	"github.com/eyrihe999-stack/Synapse/internal/permission/dto"
	"github.com/gin-gonic/gin"
)

// ListGroups 分页列出 org 下所有组。
// GET /api/v2/orgs/:slug/groups?page=1&size=20
func (h *PermHandler) ListGroups(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", strconv.Itoa(permission.DefaultPageSize)))

	resp, err := h.groupSvc.ListGroups(c.Request.Context(), org.ID, page, size)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}

// ListMyGroups 列出登录用户在该 org 中加入的所有组。
// GET /api/v2/orgs/:slug/groups/mine
func (h *PermHandler) ListMyGroups(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	items, err := h.groupSvc.ListMyGroups(c.Request.Context(), org.ID, userID)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: items})
}

// CreateGroup 创建一个权限组。任何 org 成员可调用,callerUserID 自动成为 owner。
// POST /api/v2/orgs/:slug/groups
func (h *PermHandler) CreateGroup(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	var req dto.CreateGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body", err.Error())
		return
	}
	resp, err := h.groupSvc.CreateGroup(c.Request.Context(), org.ID, userID, req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "Group created", Result: resp})
}

// GetGroup 查单个组详情。
// GET /api/v2/orgs/:slug/groups/:group_id
func (h *PermHandler) GetGroup(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	groupID, err := parseGroupID(c)
	if err != nil {
		return
	}
	resp, err := h.groupSvc.GetGroup(c.Request.Context(), org.ID, groupID)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}

// UpdateGroup 改组(目前只允许改名)。仅组 owner 可调用。
// PATCH /api/v2/orgs/:slug/groups/:group_id
func (h *PermHandler) UpdateGroup(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	groupID, err := parseGroupID(c)
	if err != nil {
		return
	}
	var req dto.UpdateGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body", err.Error())
		return
	}
	resp, err := h.groupSvc.UpdateGroup(c.Request.Context(), org.ID, groupID, userID, req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "Group updated", Result: resp})
}

// DeleteGroup 删组(级联删成员)。仅组 owner 可调用。
// DELETE /api/v2/orgs/:slug/groups/:group_id
func (h *PermHandler) DeleteGroup(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	groupID, err := parseGroupID(c)
	if err != nil {
		return
	}
	if err := h.groupSvc.DeleteGroup(c.Request.Context(), org.ID, groupID, userID); err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "Group deleted"})
}

// ListMembers 分页列出某组的成员。任何 org 成员可查。
// GET /api/v2/orgs/:slug/groups/:group_id/members?page=1&size=20
func (h *PermHandler) ListMembers(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	groupID, err := parseGroupID(c)
	if err != nil {
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", strconv.Itoa(permission.DefaultPageSize)))

	resp, err := h.groupSvc.ListMembers(c.Request.Context(), org.ID, groupID, page, size)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}

// AddMember 把目标 user 加入组。仅组 owner 可调用;目标 user 必须是 org 成员。
// POST /api/v2/orgs/:slug/groups/:group_id/members
func (h *PermHandler) AddMember(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	callerID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	groupID, err := parseGroupID(c)
	if err != nil {
		return
	}
	var req dto.AddGroupMemberRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body", err.Error())
		return
	}
	if err := h.groupSvc.AddMember(c.Request.Context(), org.ID, groupID, callerID, req.UserID); err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "Member added"})
}

// RemoveMember 把目标 user 从组移除。组 owner 可踢任何人(除自己),普通成员可自我退出。
// DELETE /api/v2/orgs/:slug/groups/:group_id/members/:user_id
func (h *PermHandler) RemoveMember(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	callerID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	groupID, err := parseGroupID(c)
	if err != nil {
		return
	}
	targetIDStr := c.Param("user_id")
	targetID, err := strconv.ParseUint(targetIDStr, 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid user_id", err.Error())
		return
	}
	if err := h.groupSvc.RemoveMember(c.Request.Context(), org.ID, groupID, callerID, targetID); err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "Member removed"})
}

// parseGroupID 从 URL path 取 :group_id 并转 uint64,失败时直接 BadRequest。
//
//sayso-lint:ignore handler-no-response
func parseGroupID(c *gin.Context) (uint64, error) {
	idStr := c.Param("group_id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid group_id", err.Error())
		return 0, err
	}
	return id, nil
}
