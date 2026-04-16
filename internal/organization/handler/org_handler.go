// org_handler.go 组织本体 HTTP 端点。
package handler

import (
	"net/http"
	"strconv"

	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/organization/dto"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
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

// ListMyOrgs 列出当前用户所属的所有 active 组织的 HTTP 端点,对应 GET /api/v2/orgs/mine。
// 返回中每条记录附带当前用户在该 org 内的角色信息。
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

// GetOrg GET /api/v2/orgs/:slug
// OrgContextMiddleware 已注入 org 对象。
func (h *OrgHandler) GetOrg(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	// 用 service 转 DTO 的形状:重新按 slug 查询保证返回最新状态
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

// UpdateOrg PATCH /api/v2/orgs/:slug
// 需要 PermOrgUpdate,由 PermissionMiddleware 前置校验。
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

// UpdateOrgSettings PATCH /api/v2/orgs/:slug/settings
// 需要 PermOrgSettingsReviewToggle。
func (h *OrgHandler) UpdateOrgSettings(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	var req dto.UpdateOrgSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body", err.Error())
		return
	}
	resp, err := h.orgSvc.UpdateSettings(c.Request.Context(), org.ID, req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "Settings updated",
		Result:  resp,
	})
}

// TransferOwnership POST /api/v2/orgs/:slug/transfer
// 需要 PermOrgTransfer(owner 独占)。
// 接口语义是"发起转让邀请",真正的 owner 交接发生在被转让人接受邀请时。
func (h *OrgHandler) TransferOwnership(c *gin.Context) {
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
	var req dto.TransferOrgOwnershipRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body", err.Error())
		return
	}
	resp, err := h.inviteSvc.InitiateOwnershipTransfer(c.Request.Context(), userID, org.ID, req.TargetUserID)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "Ownership transfer invitation created",
		Result:  resp,
	})
}

// DissolveOrg DELETE /api/v2/orgs/:slug
// 需要 PermOrgDelete(owner 独占)。
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

// parsePagination 从 query 参数解析 page/size,提供默认值。
// 非法输入会被解析为 0,由 service 层回退到默认分页值,因此此处主动丢弃 Atoi 的 error。
func parsePagination(c *gin.Context) (int, int) {
	//sayso-lint:ignore err-swallow
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	//sayso-lint:ignore err-swallow
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	return page, size
}
