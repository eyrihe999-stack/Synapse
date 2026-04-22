// router.go source 模块路由注册。
//
// 所有路由挂在 /api/v2/orgs/:slug/sources 之下,共享 organization 的
// JWTAuthWithSession + OrgContextMiddleware:
//   - JWT 校验:登录态 + session 有效
//   - org 上下文:解析 :slug → 加载 org → 校验 user 是成员 → 注入 ctx
//
// owner-only 操作(改 visibility)在 service 层硬规则里做。
package handler

import (
	"github.com/eyrihe999-stack/Synapse/internal/common/jwt"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	orghandler "github.com/eyrihe999-stack/Synapse/internal/organization/handler"
	orgservice "github.com/eyrihe999-stack/Synapse/internal/organization/service"
	"github.com/eyrihe999-stack/Synapse/internal/user"
	"github.com/gin-gonic/gin"
)

// RegisterRoutes 注册 /api/v2/orgs/:slug/sources/*。
func RegisterRoutes(
	r *gin.Engine,
	h *SourceHandler,
	jwtManager *jwt.JWTManager,
	sessionStore user.SessionStore,
	orgSvc orgservice.OrgService,
	log logger.LoggerInterface,
) {
	g := r.Group("/api/v2/orgs/:slug/sources")
	g.Use(
		middleware.JWTAuthWithSession(jwtManager, sessionStore),
		orghandler.OrgContextMiddleware(orgSvc, log),
	)
	{
		g.GET("", h.ListSources)
		g.POST("", h.CreateSource)
		g.GET("/mine", h.ListMySources)
		g.GET("/:source_id", h.GetSource)
		g.PATCH("/:source_id/visibility", h.UpdateVisibility)
		// DELETE:仅 source owner 可;前提是该 source 下的所有 doc 都已被清空,否则 409
		g.DELETE("/:source_id", h.DeleteSource)

		// M3 ACL 管理(owner-only 操作在 service 层硬规则里做)
		g.GET("/:source_id/acl", h.ListACL)
		g.POST("/:source_id/acl", h.GrantACL)
		g.PATCH("/:source_id/acl/:acl_id", h.UpdateACL)
		g.DELETE("/:source_id/acl/:acl_id", h.RevokeACL)
	}
}
