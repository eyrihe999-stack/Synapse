// source_handler.go source 管理 HTTP 端点。
//
// 路由(全部挂在 /api/v2/orgs/:slug/sources,共享 OrgContextMiddleware):
//
//	GET    /api/v2/orgs/:slug/sources               分页列出 org 下所有 source
//	                                                可选 ?kind=manual_upload 过滤
//	GET    /api/v2/orgs/:slug/sources/mine          列我作为 owner 的 source(不分页)
//	GET    /api/v2/orgs/:slug/sources/:source_id    查单个 source 详情
//	PATCH  /api/v2/orgs/:slug/sources/:source_id/visibility   改 visibility(owner-only)
//
// 所有接口要求调用方是 org 成员(OrgContextMiddleware 保证)。
// owner-only 检查在 service 层硬规则里做。
//
// M2 不开放创建 / 删除 source 的接口 —— manual_upload 由 doc 上传链路 lazy 创建,
// 用户不需要手动建/删。未来 kind=gitlab_repo 等扩展时再补 POST/DELETE。
package handler

import (
	"net/http"
	"strconv"

	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	orghandler "github.com/eyrihe999-stack/Synapse/internal/organization/handler"
	"github.com/eyrihe999-stack/Synapse/internal/source"
	"github.com/eyrihe999-stack/Synapse/internal/source/dto"
	srvc "github.com/eyrihe999-stack/Synapse/internal/source/service"
	"github.com/gin-gonic/gin"
)

// CreateSource 创建一个 kind=custom 的自建数据源,callerUserID 自动成为 owner。
// POST /api/v2/orgs/:slug/sources
// body: { "name": "...", "visibility": "org|group|private" } (visibility 省略=org)
func (h *SourceHandler) CreateSource(c *gin.Context) {
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
	var req dto.CreateSourceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body", err.Error())
		return
	}
	resp, err := h.svc.CreateCustomSource(c.Request.Context(), org.ID, userID, req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "Source created", Result: resp})
}

// ListSources 分页列出 org 下的 source。
// GET /api/v2/orgs/:slug/sources?scope=visible&kind=manual_upload&page=1&size=20
//
// scope 取值:
//   - visible(默认):只列 caller 能读的 source(owner / visibility=org / ACL 命中)
//   - all:           全 org 列表(管理 / 审计视图)
func (h *SourceHandler) ListSources(c *gin.Context) {
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
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", strconv.Itoa(source.DefaultPageSize)))
	kind := c.Query("kind")
	scope := srvc.ParseListScope(c.Query("scope"))

	resp, err := h.svc.ListSources(c.Request.Context(), org.ID, userID, scope, kind, page, size)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}

// ListMySources 列出当前 user 在该 org 下作为 owner 的所有 source。
// GET /api/v2/orgs/:slug/sources/mine
func (h *SourceHandler) ListMySources(c *gin.Context) {
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
	items, err := h.svc.ListMySources(c.Request.Context(), org.ID, userID)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: items})
}

// GetSource 查单个 source 详情。
// GET /api/v2/orgs/:slug/sources/:source_id
func (h *SourceHandler) GetSource(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	sourceID, err := parseSourceID(c)
	if err != nil {
		return
	}
	resp, err := h.svc.GetSource(c.Request.Context(), org.ID, sourceID)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}

// UpdateVisibility 改 source 的 visibility。仅 source owner 可调用。
// PATCH /api/v2/orgs/:slug/sources/:source_id/visibility
func (h *SourceHandler) UpdateVisibility(c *gin.Context) {
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
	sourceID, err := parseSourceID(c)
	if err != nil {
		return
	}
	var req dto.UpdateVisibilityRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body", err.Error())
		return
	}
	resp, err := h.svc.UpdateVisibility(c.Request.Context(), org.ID, sourceID, userID, req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "Visibility updated", Result: resp})
}

// DeleteSource 删除一个 source。仅 source owner 可调用,前提是该 source 下的所有 doc
// 都已经被删除;否则返回 CodeSourceHasDocuments,前端提示用户先清理 doc。
// DELETE /api/v2/orgs/:slug/sources/:source_id
func (h *SourceHandler) DeleteSource(c *gin.Context) {
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
	sourceID, err := parseSourceID(c)
	if err != nil {
		return
	}
	if err := h.svc.DeleteSource(c.Request.Context(), org.ID, sourceID, userID); err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "Source deleted"})
}

// parseSourceID 从 URL path 取 :source_id 并转 uint64,失败时直接 BadRequest。
//
//sayso-lint:ignore handler-no-response
func parseSourceID(c *gin.Context) (uint64, error) {
	idStr := c.Param("source_id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid source_id", err.Error())
		return 0, err
	}
	return id, nil
}

// parseACLID 从 URL path 取 :acl_id 并转 uint64。
//
//sayso-lint:ignore handler-no-response
func parseACLID(c *gin.Context) (uint64, error) {
	idStr := c.Param("acl_id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid acl_id", err.Error())
		return 0, err
	}
	return id, nil
}

// ─── ACL endpoints ────────────────────────────────────────────────────────────

// GrantACL 给某 source 加一条 ACL。
// POST /api/v2/orgs/:slug/sources/:source_id/acl
func (h *SourceHandler) GrantACL(c *gin.Context) {
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
	sourceID, err := parseSourceID(c)
	if err != nil {
		return
	}
	var req dto.GrantSourceACLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body", err.Error())
		return
	}
	resp, err := h.svc.GrantSourceACL(c.Request.Context(), org.ID, sourceID, userID, req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ACL granted", Result: resp})
}

// ListACL 列某 source 上的所有 ACL。
// GET /api/v2/orgs/:slug/sources/:source_id/acl
func (h *SourceHandler) ListACL(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	sourceID, err := parseSourceID(c)
	if err != nil {
		return
	}
	resp, err := h.svc.ListSourceACL(c.Request.Context(), org.ID, sourceID)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}

// UpdateACL 改某条 ACL 的 permission。
// PATCH /api/v2/orgs/:slug/sources/:source_id/acl/:acl_id
func (h *SourceHandler) UpdateACL(c *gin.Context) {
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
	sourceID, err := parseSourceID(c)
	if err != nil {
		return
	}
	aclID, err := parseACLID(c)
	if err != nil {
		return
	}
	var req dto.UpdateSourceACLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body", err.Error())
		return
	}
	resp, err := h.svc.UpdateSourceACL(c.Request.Context(), org.ID, sourceID, aclID, userID, req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ACL updated", Result: resp})
}

// RevokeACL 删某条 ACL。
// DELETE /api/v2/orgs/:slug/sources/:source_id/acl/:acl_id
func (h *SourceHandler) RevokeACL(c *gin.Context) {
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
	sourceID, err := parseSourceID(c)
	if err != nil {
		return
	}
	aclID, err := parseACLID(c)
	if err != nil {
		return
	}
	if err := h.svc.RevokeSourceACL(c.Request.Context(), org.ID, sourceID, aclID, userID); err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ACL revoked"})
}
