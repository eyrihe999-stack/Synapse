// router.go agents 模块路由注册。
//
// /api/v2/orgs/:slug/agents/*
//   JWT + session → OrgContextMiddleware(注入 org + 校验成员)
//   组内 owner/admin/创建者的精细校验在 service 层。
package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/common/jwt"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	orghandler "github.com/eyrihe999-stack/Synapse/internal/organization/handler"
	orgservice "github.com/eyrihe999-stack/Synapse/internal/organization/service"
	"github.com/eyrihe999-stack/Synapse/internal/user"
)

// RegisterRoutes 挂 /api/v2/orgs/:slug/agents/*。
//
// onlineChecker 查 agent 是否在线(main.go 传 *transport/service.LocalHub —— 其 IsOnline
// 方法签名对上)。
func RegisterRoutes(
	r *gin.Engine,
	h *Handler,
	jwtManager *jwt.JWTManager,
	sessionStore user.SessionStore,
	orgSvc orgservice.OrgService,
	onlineChecker OnlineChecker,
	log logger.LoggerInterface,
) {
	g := r.Group("/api/v2/orgs/:slug/agents")
	g.Use(
		middleware.JWTAuthWithSession(jwtManager, sessionStore),
		orghandler.OrgContextMiddleware(orgSvc, log),
	)
	{
		g.POST("", func(c *gin.Context) { h.Create(c, onlineChecker) })
		g.GET("", func(c *gin.Context) { h.List(c, onlineChecker) })
		g.GET("/:agent_id", func(c *gin.Context) { h.Get(c, onlineChecker) })
		g.PATCH("/:agent_id", func(c *gin.Context) { h.Update(c, onlineChecker) })
		g.DELETE("/:agent_id", h.Delete)
		g.POST("/:agent_id/rotate-key", func(c *gin.Context) { h.RotateKey(c, onlineChecker) })
	}
}
