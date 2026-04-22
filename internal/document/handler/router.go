// router.go document 模块路由。挂在 /api/v2/orgs/:slug/documents/* 下,走 org 上下文 middleware。
package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/common/jwt"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	orghandler "github.com/eyrihe999-stack/Synapse/internal/organization/handler"
	orgservice "github.com/eyrihe999-stack/Synapse/internal/organization/service"
	permhandler "github.com/eyrihe999-stack/Synapse/internal/permission/handler"
	permsvc "github.com/eyrihe999-stack/Synapse/internal/permission/service"
	"github.com/eyrihe999-stack/Synapse/internal/user"
)

// RegisterRoutes 注册 /api/v2/orgs/:slug/documents/*。
//
// 路由全走 JWTAuthWithSession + OrgContextMiddleware(成员校验)+ PermContextMiddleware
// (M3:把 user 在该 org 的 group ids 一次性查出来塞 ctx,后续 ACL 判定零打 DB)。
func RegisterRoutes(
	r *gin.Engine,
	h *Handler,
	jwtManager *jwt.JWTManager,
	sessionStore user.SessionStore,
	orgSvc orgservice.OrgService,
	permSvc permsvc.PermissionService,
	log logger.LoggerInterface,
) {
	g := r.Group("/api/v2/orgs/:slug/documents")
	g.Use(
		middleware.JWTAuthWithSession(jwtManager, sessionStore),
		orghandler.OrgContextMiddleware(orgSvc, log),
		permhandler.PermContextMiddleware(permSvc, log),
	)
	{
		g.POST("/upload", h.Upload)
		g.GET("", h.List)
		g.GET("/:id", h.Get)
		g.GET("/:id/content", h.Content)
		g.GET("/:id/versions", h.ListVersions)
		g.DELETE("/:id", h.Delete)
	}
}
