// handler_middleware.go agent 模块的 gin 中间件。
package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/eyrihe999-stack/Synapse/internal/agent"
	"github.com/eyrihe999-stack/Synapse/internal/agent/service"
	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
	"github.com/gin-gonic/gin"
)

const (
	ctxKeyOrg        = "agent_org"
	ctxKeyMembership = "agent_membership"
)

// OrgContextMiddleware 从 URL :slug 或 X-Org-ID header 解析 org,校验成员身份,
// 并将 OrgInfo 和 Membership 注入 gin context。
func OrgContextMiddleware(orgPort service.OrgPort, log logger.LoggerInterface) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := middleware.GetUserID(c)
		if !ok {
			response.Unauthorized(c, "Missing user context", "")
			c.Abort()
			return
		}

		slug := c.Param("slug")
		var org *service.OrgInfo
		var err error
		if slug != "" {
			org, err = orgPort.GetOrgBySlug(c.Request.Context(), slug)
		} else {
			headerVal := c.GetHeader("X-Org-ID")
			if headerVal == "" {
				log.WarnCtx(c.Request.Context(), "missing org context", map[string]any{"user_id": userID})
				c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentInvalidRequest, Message: "X-Org-ID header or org slug required"})
				//sayso-lint:ignore gin-no-return
				c.Abort()
				return
			}
			if id, parseErr := strconv.ParseUint(headerVal, 10, 64); parseErr == nil {
				org, err = orgPort.GetOrgByID(c.Request.Context(), id)
			} else {
				org, err = orgPort.GetOrgBySlug(c.Request.Context(), headerVal)
			}
		}
		if err != nil {
			log.WarnCtx(c.Request.Context(), "org resolve failed", map[string]any{"user_id": userID, "error": err.Error()})
			c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentInvalidRequest, Message: "Org not found"})
			c.Abort()
			return
		}
		membership, err := orgPort.GetMembership(c.Request.Context(), org.ID, userID)
		if err != nil {
			log.WarnCtx(c.Request.Context(), "membership check failed", map[string]any{"org_id": org.ID, "user_id": userID, "error": err.Error()})
			c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentPermissionDenied, Message: "Not a member of this organization"})
			c.Abort()
			return
		}
		c.Set(ctxKeyOrg, org)
		c.Set(ctxKeyMembership, membership)
		c.Next()
	}
}

// PermissionMiddleware 校验 membership 持有指定权限点,不满足则返回权限拒绝响应。
func PermissionMiddleware(permission string, log logger.LoggerInterface) gin.HandlerFunc {
	return func(c *gin.Context) {
		m, ok := GetMembership(c)
		if !ok {
			log.ErrorCtx(c.Request.Context(), "permission middleware missing membership", errors.New("missing membership"), nil)
			response.InternalServerError(c, "Internal server error", "")
			c.Abort()
			return
		}
		if !m.Has(permission) {
			log.WarnCtx(c.Request.Context(), "permission denied", map[string]any{
				"user_id":    m.UserID,
				"org_id":     m.OrgID,
				"permission": permission,
			})
			c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentPermissionDenied, Message: "Permission denied: " + permission})
			c.Abort()
			return
		}
		c.Next()
	}
}

// GetOrg 从 gin context 取出已注入的 OrgInfo,未找到时返回 false。
//
//sayso-lint:ignore handler-no-response
func GetOrg(c *gin.Context) (*service.OrgInfo, bool) {
	v, ok := c.Get(ctxKeyOrg)
	if !ok {
		return nil, false
	}
	m, ok := v.(*service.OrgInfo)
	return m, ok
}

// GetMembership 从 gin context 取出已注入的 Membership,未找到时返回 false。
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
