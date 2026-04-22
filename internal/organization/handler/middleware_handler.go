// middleware_handler.go 组织模块专用的 gin 中间件。
//
// OrgContextMiddleware:解析 X-Org-ID(slug 或数字 ID)或 path :slug →
//   校验 user 是活跃成员 → 把 Org 注入 gin context,供后续 handler 使用。
//
// 必须在 JWTAuth 之后使用(需要 user_id)。
package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/organization"
	"github.com/eyrihe999-stack/Synapse/internal/organization/model"
	"github.com/eyrihe999-stack/Synapse/internal/organization/service"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	"github.com/gin-gonic/gin"
)

// ctxKey 是 gin context 中注入 org 相关值的 key。
const ctxKeyOrg = "organization_org"

// OrgContextMiddleware 解析请求的 org 上下文。
//
// org 的定位来源,优先级:
//  1. URL path 参数 :slug(例如 /orgs/acme-corp/members)
//  2. HTTP header X-Org-ID(支持 slug 或数字 ID)
//
// 解析后:
//   - 确认 org 存在且未解散
//   - 调用 orgSvc.IsMember 确认 user 是成员
//   - 把 org 注入 gin context
func OrgContextMiddleware(
	orgSvc service.OrgService,
	log logger.LoggerInterface,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := middleware.GetUserID(c)
		if !ok {
			response.Unauthorized(c, "Missing user context", "")
			c.Abort()
			return
		}

		slug := c.Param("slug")
		var org *model.Org

		if slug != "" {
			var loadErr error
			org, loadErr = loadOrgBySlug(c, orgSvc, slug, userID, log)
			if loadErr != nil {
				return
			}
		} else {
			headerVal := c.GetHeader("X-Org-ID")
			if headerVal == "" {
				log.WarnCtx(c.Request.Context(), "缺少 org 上下文", map[string]any{"user_id": userID})
				c.JSON(http.StatusOK, response.BaseResponse{
					Code:    organization.CodeOrgNotFound,
					Message: "X-Org-ID header or org slug required",
				})
				c.Abort()
				return
			}
			var loadErr error
			if id, parseErr := strconv.ParseUint(headerVal, 10, 64); parseErr == nil {
				org, loadErr = loadOrgByID(c, orgSvc, id, userID, log)
			} else {
				org, loadErr = loadOrgBySlug(c, orgSvc, headerVal, userID, log)
			}
			if loadErr != nil {
				return
			}
		}

		// 校验成员身份
		isMember, err := orgSvc.IsMember(c.Request.Context(), org.ID, userID)
		if err != nil {
			log.ErrorCtx(c.Request.Context(), "查询成员关系失败", err, map[string]any{"org_id": org.ID, "user_id": userID})
			response.InternalServerError(c, "Internal server error", "")
			c.Abort()
			return
		}
		if !isMember {
			log.WarnCtx(c.Request.Context(), "非成员", map[string]any{"org_id": org.ID, "user_id": userID})
			c.JSON(http.StatusOK, response.BaseResponse{
				Code:    organization.CodeOrgNotMember,
				Message: "Not a member of this organization",
			})
			c.Abort()
			return
		}

		c.Set(ctxKeyOrg, org)
		c.Next()
	}
}

// RequireOwner 前置守卫:只有 org 的 OwnerUserID 可以通过。
// 必须在 OrgContextMiddleware 之后使用(依赖已注入的 org 和 user_id)。
func RequireOwner(log logger.LoggerInterface) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := middleware.GetUserID(c)
		if !ok {
			response.Unauthorized(c, "Missing user context", "")
			c.Abort()
			return
		}
		org, ok := GetOrg(c)
		if !ok {
			response.InternalServerError(c, "Missing org context", "")
			c.Abort()
			return
		}
		if org.OwnerUserID != userID {
			log.WarnCtx(c.Request.Context(), "owner-only 动作拒绝非 owner", map[string]any{
				"user_id": userID,
				"org_id":  org.ID,
			})
			c.JSON(http.StatusOK, response.BaseResponse{
				Code:    organization.CodeOrgNotMember,
				Message: "Owner only",
			})
			c.Abort()
			return
		}
		c.Next()
	}
}

// GetOrg 从 gin context 取出已注入的 org。
//sayso-lint:ignore handler-no-response
func GetOrg(c *gin.Context) (*model.Org, bool) {
	v, ok := c.Get(ctxKeyOrg)
	if !ok {
		return nil, false
	}
	org, ok := v.(*model.Org)
	return org, ok
}

// loadOrgBySlug 通过 slug 加载 org。
//sayso-lint:ignore handler-no-response
func loadOrgBySlug(c *gin.Context, orgSvc service.OrgService, slug string, userID uint64, log logger.LoggerInterface) (*model.Org, error) {
	resp, err := orgSvc.GetOrgBySlug(c.Request.Context(), slug)
	if err != nil {
		handleOrgContextError(c, log, err, "slug lookup failed", map[string]any{"slug": slug, "user_id": userID})
		//sayso-lint:ignore log-coverage
		return nil, err
	}
	m, err := orgSvc.GetOrgByID(c.Request.Context(), resp.ID)
	if err != nil {
		handleOrgContextError(c, log, err, "org lookup failed", map[string]any{"org_id": resp.ID, "user_id": userID})
		//sayso-lint:ignore log-coverage
		return nil, err
	}
	return m, nil
}

// loadOrgByID 通过 org ID 加载 org。
//sayso-lint:ignore handler-no-response
func loadOrgByID(c *gin.Context, orgSvc service.OrgService, orgID uint64, userID uint64, log logger.LoggerInterface) (*model.Org, error) {
	m, err := orgSvc.GetOrgByID(c.Request.Context(), orgID)
	if err != nil {
		handleOrgContextError(c, log, err, "org id lookup failed", map[string]any{"org_id": orgID, "user_id": userID})
		//sayso-lint:ignore log-coverage
		return nil, err
	}
	return m, nil
}

// handleOrgContextError 把 org 查询阶段的错误翻译为 HTTP 响应并 abort。
func handleOrgContextError(c *gin.Context, log logger.LoggerInterface, err error, reason string, fields map[string]any) {
	switch {
	case errors.Is(err, organization.ErrOrgNotFound):
		log.WarnCtx(c.Request.Context(), "org 不存在("+reason+")", fields)
		c.JSON(http.StatusOK, response.BaseResponse{
			Code:    organization.CodeOrgNotFound,
			Message: "Organization not found",
		})
	case errors.Is(err, organization.ErrOrgDissolved):
		log.WarnCtx(c.Request.Context(), "org 已解散("+reason+")", fields)
		c.JSON(http.StatusOK, response.BaseResponse{
			Code:    organization.CodeOrgDissolved,
			Message: "Organization dissolved",
		})
	default:
		if fields == nil {
			fields = map[string]any{}
		}
		fields["reason"] = reason
		log.ErrorCtx(c.Request.Context(), "org 上下文解析失败", err, fields)
		response.InternalServerError(c, "Internal server error", "")
	}
	c.Abort()
}
