// role_handler.go 角色管理 HTTP 端点。
package handler

import (
	"net/http"
	"strconv"

	"github.com/eyrihe999-stack/Synapse/internal/organization/dto"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
	"github.com/gin-gonic/gin"
)

// ListRoles GET /api/v2/orgs/:slug/roles
// 成员即可查看。
func (h *OrgHandler) ListRoles(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	list, err := h.roleSvc.ListRoles(c.Request.Context(), org.ID)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "ok",
		Result:  list,
	})
}

// CreateRole POST /api/v2/orgs/:slug/roles
// 需要 PermRoleManage(owner 独占)。
func (h *OrgHandler) CreateRole(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	var req dto.CreateCustomRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body", err.Error())
		return
	}
	resp, err := h.roleSvc.CreateCustomRole(c.Request.Context(), org.ID, req)
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

// UpdateRole PATCH /api/v2/orgs/:slug/roles/:id
// 需要 PermRoleManage。
func (h *OrgHandler) UpdateRole(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid role id", err.Error())
		return
	}
	var req dto.UpdateCustomRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body", err.Error())
		return
	}
	resp, err := h.roleSvc.UpdateCustomRole(c.Request.Context(), org.ID, id, req)
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

// DeleteRole DELETE /api/v2/orgs/:slug/roles/:id
// 需要 PermRoleManage。
func (h *OrgHandler) DeleteRole(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid role id", err.Error())
		return
	}
	if err := h.roleSvc.DeleteCustomRole(c.Request.Context(), org.ID, id); err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "Role deleted",
	})
}

// ListPermissions GET /api/v2/orgs/:slug/permissions
// 成员即可查看系统权限点清单。
func (h *OrgHandler) ListPermissions(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	resp := h.roleSvc.ListPermissions(c.Request.Context())
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "ok",
		Result:  resp,
	})
}
