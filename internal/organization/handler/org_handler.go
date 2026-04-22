// org_handler.go 组织本体 HTTP 端点。
package handler

import (
	"net/http"
	"strconv"

	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/organization/dto"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	"github.com/gin-gonic/gin"
)

// CreateOrg 创建新组织的 HTTP 端点,对应 POST /api/v2/orgs。
// 只需要登录态,调用方成为新 org 的 owner 并自动加入。
func (h *OrgHandler) CreateOrg(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	var req dto.CreateOrgRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body", err.Error())
		return
	}
	resp, err := h.orgSvc.CreateOrg(c.Request.Context(), userID, req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "Organization created",
		Result:  resp,
	})
}

// CheckSlug 预检 slug 的合法性与可用性,对应 GET /api/v2/orgs/check-slug?slug=xxx。
// 用于前端创建表单实时提示 slug 是否可用。
func (h *OrgHandler) CheckSlug(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	slug := c.Query("slug")
	if slug == "" {
		response.BadRequest(c, "slug query param required", "")
		return
	}
	resp, err := h.orgSvc.CheckSlug(c.Request.Context(), slug)
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

// ListMyOrgs 列出当前用户所属的所有 active 组织的 HTTP 端点。
func (h *OrgHandler) ListMyOrgs(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	list, err := h.orgSvc.ListOrgsByUser(c.Request.Context(), userID)
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

// GetOrg 查询 org 详情,对应 GET /api/v2/orgs/:slug。
// 调用方须已通过 org 成员资格中间件校验。
func (h *OrgHandler) GetOrg(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	resp, err := h.orgSvc.GetOrgBySlug(c.Request.Context(), org.Slug)
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

// UpdateOrg 部分更新 org 的 display_name / description,对应 PATCH /api/v2/orgs/:slug。
// 调用方须是 owner(由权限中间件校验)。
func (h *OrgHandler) UpdateOrg(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	var req dto.UpdateOrgRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body", err.Error())
		return
	}
	resp, err := h.orgSvc.UpdateOrg(c.Request.Context(), org.ID, req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "Organization updated",
		Result:  resp,
	})
}

// DissolveOrg 软删除 org(标记 status=dissolved),对应 DELETE /api/v2/orgs/:slug。
// 调用方须是 owner(由权限中间件校验)。
func (h *OrgHandler) DissolveOrg(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	if err := h.orgSvc.DissolveOrg(c.Request.Context(), org.ID); err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "Organization dissolved",
	})
}

// parsePagination 从 query 参数解析 page/size。
func parsePagination(c *gin.Context) (int, int) {
	//sayso-lint:ignore err-swallow
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	//sayso-lint:ignore err-swallow
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	return page, size
}
