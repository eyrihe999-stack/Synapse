// middleware.go 组织模块专用的 gin 中间件。
//
// 两个中间件:
//   - OrgContextMiddleware:解析 X-Org-ID(slug 或数字 ID)→ 校验 user 是活跃成员 →
//     把 Org 和 Membership 注入 gin context,供后续 handler 和 PermissionMiddleware 使用
//   - PermissionMiddleware:读取已注入的 Membership → 判断是否持有指定权限
//
// 这两个中间件必须在 JWTAuth 之后使用(需要 user_id),
// PermissionMiddleware 必须在 OrgContextMiddleware 之后使用(需要 membership)。
package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/organization"
	"github.com/eyrihe999-stack/Synapse/internal/organization/model"
	"github.com/eyrihe999-stack/Synapse/internal/organization/service"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
	"github.com/gin-gonic/gin"
)

// ctxKey 是 gin context 中注入 org 相关值的 key,跨 middleware/handler 共享。
const (
	ctxKeyOrg        = "organization_org"
	ctxKeyMembership = "organization_membership"
)

// OrgContextMiddleware 解析请求的 org 上下文。
//
// org 的定位可以来自两个来源,优先级:
//  1. URL path 参数 :slug(例如 /orgs/acme-corp/members)
//  2. HTTP header X-Org-ID(支持 slug 或数字 ID)
//
// 解析后:
//   - 确认 org 存在且未解散
//   - 调用 roleSvc.GetMembership 确认 user 是成员
//   - 把 org 和 membership 注入 gin context
//
// 任一步骤失败都会返回 401/403/404(业务错误统一 200 + body code,但未登录和
// 不是成员直接用 HTTP 状态码以便 SDK 识别)。
func OrgContextMiddleware(
	orgSvc service.OrgService,
	roleSvc service.RoleService,
	log logger.LoggerInterface,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := middleware.GetUserID(c)
		if !ok {
			response.Unauthorized(c, "Missing user context", "")
			c.Abort()
			return
		}

		// 1. 从 path 参数拿 slug
		slug := c.Param("slug")
		var org *model.Org

		if slug != "" {
			var loadErr error
			org, loadErr = loadOrgBySlug(c, orgSvc, slug, userID, log)
			if loadErr != nil {
				return
			}
		} else {
			// 2. 从 header 拿 X-Org-ID(可以是 slug 或数字 ID)
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
			// 尝试解析为数字 ID;失败则按 slug 处理
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
		membership, err := roleSvc.GetMembership(c.Request.Context(), org.ID, userID)
		if err != nil {
			if errors.Is(err, organization.ErrOrgNotMember) {
				log.WarnCtx(c.Request.Context(), "非成员", map[string]any{"org_id": org.ID, "user_id": userID})
				c.JSON(http.StatusOK, response.BaseResponse{
					Code:    organization.CodeOrgNotMember,
					Message: "Not a member of this organization",
				})
				c.Abort()
				return
			}
			log.ErrorCtx(c.Request.Context(), "查询成员关系失败", err, map[string]any{"org_id": org.ID, "user_id": userID})
			response.InternalServerError(c, "Internal server error", "")
			c.Abort()
			return
		}

		// 注入 gin context
		c.Set(ctxKeyOrg, org)
		c.Set(ctxKeyMembership, membership)
		c.Next()
	}
}

// PermissionMiddleware 检查 membership 是否持有指定权限点。
// 必须在 OrgContextMiddleware 之后使用。
func PermissionMiddleware(permission string, log logger.LoggerInterface) gin.HandlerFunc {
	return func(c *gin.Context) {
		m, ok := GetMembership(c)
		if !ok {
			log.ErrorCtx(c.Request.Context(), "PermissionMiddleware 缺少 membership 上下文", nil, nil)
			response.InternalServerError(c, "Internal server error", "")
			c.Abort()
			return
		}
		if !m.Has(permission) {
			log.WarnCtx(c.Request.Context(), "权限不足", map[string]any{
				"user_id":    m.UserID,
				"org_id":     m.OrgID,
				"permission": permission,
				"role_name":  m.RoleName,
			})
			c.JSON(http.StatusOK, response.BaseResponse{
				Code:    organization.CodeOrgPermissionDenied,
				Message: "Permission denied: " + permission,
			})
			c.Abort()
			return
		}
		c.Next()
	}
}

// GetOrg 从 gin context 取出已注入的 org。调用方在 OrgContextMiddleware 之后使用。
//sayso-lint:ignore handler-no-response
func GetOrg(c *gin.Context) (*model.Org, bool) {
	v, ok := c.Get(ctxKeyOrg)
	if !ok {
		return nil, false
	}
	org, ok := v.(*model.Org)
	return org, ok
}

// GetMembership 从 gin context 取出已注入的 Membership。
//sayso-lint:ignore handler-no-response
func GetMembership(c *gin.Context) (*service.Membership, bool) {
	v, ok := c.Get(ctxKeyMembership)
	if !ok {
		return nil, false
	}
	m, ok := v.(*service.Membership)
	return m, ok
}

// loadOrgBySlug 通过 slug 加载 org 并通过 service 拿到完整 model.Org。
// 失败时 handleOrgContextError 已写响应和日志,此处仅透传 error 标记调用方 return。
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

// loadOrgByID 通过 org ID 加载 org。语义同 loadOrgBySlug。
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
