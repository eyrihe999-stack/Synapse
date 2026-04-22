// role_handler.go 组织角色管理 HTTP 端点。
//
// 路由:
//   GET    /api/v2/orgs/:slug/roles                       列角色
//   POST   /api/v2/orgs/:slug/roles                       创建自定义角色
//   PATCH  /api/v2/orgs/:slug/roles/:role_slug            修改自定义角色(display_name)
//   DELETE /api/v2/orgs/:slug/roles/:role_slug            删除自定义角色
//   PATCH  /api/v2/orgs/:slug/members/:user_id/role       修改成员角色
//
// 所有接口调用方必须是 org 成员(OrgContextMiddleware 保证)。
// 现阶段不分 owner/admin/member 权限 —— 所有成员操作等价。
package handler

import (
	"net/http"
	"strconv"

	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	"github.com/eyrihe999-stack/Synapse/internal/organization/dto"
	permsvc "github.com/eyrihe999-stack/Synapse/internal/permission/service"
	"github.com/gin-gonic/gin"
)

// ListRoles 列出 org 的所有角色。
// GET /api/v2/orgs/:slug/roles
func (h *OrgHandler) ListRoles(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	roles, err := h.roleSvc.ListRoles(c.Request.Context(), org.ID)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "ok",
		Result:  roles,
	})
}

// CreateRole 创建一个自定义角色。
// POST /api/v2/orgs/:slug/roles
func (h *OrgHandler) CreateRole(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	var req dto.CreateRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body", err.Error())
		return
	}
	callerPerms := callerPermsFromCtx(c, org.ID)
	resp, err := h.roleSvc.CreateCustomRole(c.Request.Context(), org.ID, callerPerms, req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "Role created",
		Result:  resp,
	})
}

// UpdateRole 修改一个自定义角色的 display_name。slug 不可改,系统角色拒绝。
// PATCH /api/v2/orgs/:slug/roles/:role_slug
func (h *OrgHandler) UpdateRole(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	roleSlug := c.Param("role_slug")
	if roleSlug == "" {
		response.BadRequest(c, "role_slug required", "")
		return
	}
	var req dto.UpdateRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body", err.Error())
		return
	}
	callerPerms := callerPermsFromCtx(c, org.ID)
	resp, err := h.roleSvc.UpdateCustomRole(c.Request.Context(), org.ID, roleSlug, callerPerms, req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "Role updated",
		Result:  resp,
	})
}

// UpdateRolePermissions M5.2:任意 role 的 permissions 编辑端点。
//
// PATCH /api/v2/orgs/:slug/roles/:role_slug/permissions
//
// router 上挂 RequirePerm("role.manage_system") —— 默认只 owner 有这个 perm。
// 系统角色和自定义角色都可以经此端点改 perms;custom role 通常 admin 走 PATCH /roles/:slug
// (role.manage 权限)就行,但 admin 改不了系统角色 —— 系统角色改 perms 必须经此端点。
func (h *OrgHandler) UpdateRolePermissions(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	roleSlug := c.Param("role_slug")
	if roleSlug == "" {
		response.BadRequest(c, "role_slug required", "")
		return
	}
	var req dto.UpdateRolePermissionsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body", err.Error())
		return
	}
	callerPerms := callerPermsFromCtx(c, org.ID)
	resp, err := h.roleSvc.UpdateRolePermissions(c.Request.Context(), org.ID, roleSlug, callerPerms, req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "Role permissions updated", Result: resp})
}

// callerPermsFromCtx 从 PermContextMiddleware 注入的 ctx 取 caller 在该 org 的 permissions。
// 没拿到(中间件没跑)→ 返 nil;service 层用空集做 ceiling 检查会拒绝任何非空 perm 集,
// 行为安全(等于"caller 没权限")。
//
//sayso-lint:ignore handler-no-response
func callerPermsFromCtx(c *gin.Context, orgID uint64) []string {
	perms, _ := permsvc.PermissionsFromContext(c.Request.Context(), orgID)
	return perms
}

// DeleteRole 删除一个自定义角色。有成员挂在该角色时拒绝。
// DELETE /api/v2/orgs/:slug/roles/:role_slug
func (h *OrgHandler) DeleteRole(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	roleSlug := c.Param("role_slug")
	if roleSlug == "" {
		response.BadRequest(c, "role_slug required", "")
		return
	}
	if err := h.roleSvc.DeleteCustomRole(c.Request.Context(), org.ID, roleSlug); err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "Role deleted",
	})
}

// AssignRoleToMember 修改某个成员的角色。
// PATCH /api/v2/orgs/:slug/members/:user_id/role
func (h *OrgHandler) AssignRoleToMember(c *gin.Context) {
	if !h.checkReady(c) {
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
	var req dto.AssignRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body", err.Error())
		return
	}
	callerPerms := callerPermsFromCtx(c, org.ID)
	resp, err := h.roleSvc.AssignRoleToMember(c.Request.Context(), org.ID, targetID, callerPerms, req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "Member role updated",
		Result:  resp,
	})
}
