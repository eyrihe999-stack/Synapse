// handler_middleware.go document 模块的 gin 中间件(org context + 权限点)。
//
// 和 agent 模块 OrgContextMiddleware / PermissionMiddleware 结构对齐,只是依赖 document 的 OrgPort。
package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/eyrihe999-stack/Synapse/internal/document"
	"github.com/eyrihe999-stack/Synapse/internal/document/service"
	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
	"github.com/gin-gonic/gin"
)

const (
	ctxKeyOrg        = "document_org"
	ctxKeyMembership = "document_membership"
)

// OrgContextMiddleware 解析 URL :slug 或 X-Org-ID,注入 OrgInfo + Membership。
func OrgContextMiddleware(orgPort service.OrgPort, log logger.LoggerInterface) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := middleware.GetUserID(c)
		if !ok {
			response.Unauthorized(c, "Missing user context", "")
			c.Abort()
			return
		}
		slug := c.Param("slug")
		var (
			org *service.OrgInfo
			err error
		)
		if slug != "" {
			org, err = orgPort.GetOrgBySlug(c.Request.Context(), slug)
		} else {
			headerVal := c.GetHeader("X-Org-ID")
			if headerVal == "" {
				c.JSON(http.StatusOK, response.BaseResponse{Code: document.CodeDocumentInvalidRequest, Message: "X-Org-ID header or org slug required"})
				// 内层 if 里 return 了,else 块的剩余逻辑不会跑——lint 的 gin-no-return 在 else 块层面会误报。
				//sayso-lint:ignore gin-no-return
				c.Abort()
				return
			}
			if id, perr := strconv.ParseUint(headerVal, 10, 64); perr == nil {
				org, err = orgPort.GetOrgByID(c.Request.Context(), id)
			} else {
				org, err = orgPort.GetOrgBySlug(c.Request.Context(), headerVal)
			}
		}
		if err != nil {
			log.WarnCtx(c.Request.Context(), "document: org resolve failed", map[string]any{"user_id": userID, "error": err.Error()})
			c.JSON(http.StatusOK, response.BaseResponse{Code: document.CodeDocumentInvalidRequest, Message: "Org not found"})
			c.Abort()
			return
		}
		membership, err := orgPort.GetMembership(c.Request.Context(), org.ID, userID)
		if err != nil {
			log.WarnCtx(c.Request.Context(), "document: membership check failed", map[string]any{"org_id": org.ID, "user_id": userID, "error": err.Error()})
			c.JSON(http.StatusOK, response.BaseResponse{Code: document.CodeDocumentPermissionDenied, Message: "Not a member of this organization"})
			c.Abort()
			return
		}
		c.Set(ctxKeyOrg, org)
		c.Set(ctxKeyMembership, membership)
		c.Next()
	}
}

// PermissionMiddleware 校验当前 membership 有指定权限点,不满足返回 403 业务码。
func PermissionMiddleware(permission string, log logger.LoggerInterface) gin.HandlerFunc {
	return func(c *gin.Context) {
		m, ok := GetMembership(c)
		if !ok {
			log.ErrorCtx(c.Request.Context(), "document: missing membership in permission middleware", errors.New("missing membership"), nil)
			response.InternalServerError(c, "Internal server error", "")
			c.Abort()
			return
		}
		if !m.Has(permission) {
			log.WarnCtx(c.Request.Context(), "document: permission denied", map[string]any{
				"user_id":    m.UserID,
				"org_id":     m.OrgID,
				"permission": permission,
			})
			c.JSON(http.StatusOK, response.BaseResponse{Code: document.CodeDocumentPermissionDenied, Message: "Permission denied: " + permission})
			c.Abort()
			return
		}
		c.Next()
	}
}

// GetOrg 从 gin context 取出 OrgInfo。
//
//sayso-lint:ignore handler-no-response
func GetOrg(c *gin.Context) (*service.OrgInfo, bool) {
	v, ok := c.Get(ctxKeyOrg)
	if !ok {
		return nil, false
	}
	o, ok := v.(*service.OrgInfo)
	return o, ok
}

// GetMembership 从 gin context 取出 Membership。
//
//sayso-lint:ignore handler-no-response
func GetMembership(c *gin.Context) (*service.OrgMembership, bool) {
	v, ok := c.Get(ctxKeyMembership)
	if !ok {
		return nil, false
	}
	m, ok := v.(*service.OrgMembership)
	return m, ok
}
