// router.go 权限模块路由注册。
//
// 所有路由挂在 /api/v2/orgs/:slug/groups 之下,共享 organization 模块的
// JWTAuthWithSession + OrgContextMiddleware:
//   - JWT 校验:登录态 + session 有效
//   - org 上下文:解析 :slug → 加载 org → 校验 user 是成员 → 注入 ctx
//
// 组级 owner-only 操作的细粒度授权(改名 / 删组 / 加减成员)在 service 层硬规则里做。
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

// RegisterRoutes 注册 /api/v2/orgs/:slug/groups/* 和 /audit-log。
//
// M4 注入参数:
//   - permCtxMW:挂在 OrgContextMiddleware 后,加载 user 的 groups + permissions 到 ctx
//   - requirePerm:perm 中间件工厂,POST /groups 用 group.create 包一层(member 默认有,
//     防止未来 custom role 没勾时漏过)
//
// M6:auditH 是 audit-log 端点的 handler;不挂 RequirePerm,scope 在 service 层决定。
//
// 组改名/删组/加减成员的 owner-only 检查仍在 service 层硬规则做,不走 RBAC。
func RegisterRoutes(
	r *gin.Engine,
	h *PermHandler,
	auditH *AuditHandler,
	jwtManager *jwt.JWTManager,
	sessionStore user.SessionStore,
	orgSvc orgservice.OrgService,
	permCtxMW gin.HandlerFunc,
	requirePerm func(perm string) gin.HandlerFunc,
	log logger.LoggerInterface,
) {
	g := r.Group("/api/v2/orgs/:slug/groups")
	g.Use(
		middleware.JWTAuthWithSession(jwtManager, sessionStore),
		orghandler.OrgContextMiddleware(orgSvc, log),
		permCtxMW,
	)
	{
		// 组本体
		g.GET("", h.ListGroups)
		g.GET("/mine", h.ListMyGroups)
		g.POST("", requirePerm("group.create"), h.CreateGroup)
		g.GET("/:group_id", h.GetGroup)
		g.PATCH("/:group_id", h.UpdateGroup)
		g.DELETE("/:group_id", h.DeleteGroup)

		// 组成员
		g.GET("/:group_id/members", h.ListMembers)
		g.POST("/:group_id/members", h.AddMember)
		g.DELETE("/:group_id/members/:user_id", h.RemoveMember)
	}

	// M6 审计查询(任何成员可访问,scope 由 service 决定)
	auditGroup := r.Group("/api/v2/orgs/:slug/audit-log")
	auditGroup.Use(
		middleware.JWTAuthWithSession(jwtManager, sessionStore),
		orghandler.OrgContextMiddleware(orgSvc, log),
		permCtxMW,
	)
	{
		auditGroup.GET("", auditH.ListAuditLog)
	}
}
