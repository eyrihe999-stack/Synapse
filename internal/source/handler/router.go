// router.go source 模块路由注册。
//
// 通用 source 端点(create custom / list / get / visibility / acl)挂在
// /api/v2/orgs/:slug/sources 下,共享 JWTAuthWithSession + OrgContextMiddleware。
//
// **GitLab 同步源专属端点**(create / delete / resync)挂在 /sources/gitlab 子前缀下,
// 在通用中间件之上额外挂 PermContextMiddleware + RequirePerm("integration.gitlab.manage")
// —— 默认只 owner 拿到该 perm。
package handler

import (
	"github.com/eyrihe999-stack/Synapse/internal/common/jwt"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	orghandler "github.com/eyrihe999-stack/Synapse/internal/organization/handler"
	orgservice "github.com/eyrihe999-stack/Synapse/internal/organization/service"
	"github.com/eyrihe999-stack/Synapse/internal/permission"
	"github.com/eyrihe999-stack/Synapse/internal/user"
	"github.com/gin-gonic/gin"
)

// PermFactory 给定 perm slug 返回对应的 RequirePerm 中间件。
// main.go 用 internal/permission/handler.RequirePerm 注入(签名一致)。
type PermFactory func(perm string) gin.HandlerFunc

// RegisterRoutes 注册 /api/v2/orgs/:slug/sources/*。
//
// permCtxMW / requirePerm 用于 GitLab 子前缀的 RBAC 鉴权;若装配侧暂未接(测试场景),
// 这两个 nil 会让 GitLab 端点不挂载 —— 不会 panic,旧端点照常工作。
func RegisterRoutes(
	r *gin.Engine,
	h *SourceHandler,
	jwtManager *jwt.JWTManager,
	sessionStore user.SessionStore,
	orgSvc orgservice.OrgService,
	permCtxMW gin.HandlerFunc,
	requirePerm PermFactory,
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
		g.DELETE("/:source_id", h.DeleteSource)

		// M3 ACL(owner-only 在 service 层校验)
		g.GET("/:source_id/acl", h.ListACL)
		g.POST("/:source_id/acl", h.GrantACL)
		g.PATCH("/:source_id/acl/:acl_id", h.UpdateACL)
		g.DELETE("/:source_id/acl/:acl_id", h.RevokeACL)
	}

	// ── GitLab webhook 端点(无 JWT,验签走 service 层 X-Gitlab-Token)──────
	// 必须挂在主 r 上,**不能**放进 g 子组(g 已经叠了 JWTAuthWithSession + OrgContext,
	// GitLab 不会发那些 header)。
	r.POST("/api/v2/webhooks/gitlab/:source_id", h.HandleGitLabWebhook)

	// ── GitLab 同步源专属(默认只 owner) ──────────────────────────────────
	if permCtxMW == nil || requirePerm == nil {
		log.Warn("source 路由:GitLab 端点跳过装配 (permCtxMW / requirePerm 未注入)", nil)
		return
	}
	gl := r.Group("/api/v2/orgs/:slug/sources/gitlab")
	gl.Use(
		middleware.JWTAuthWithSession(jwtManager, sessionStore),
		orghandler.OrgContextMiddleware(orgSvc, log),
		permCtxMW,
		requirePerm(permission.PermIntegrationGitLabManage),
	)
	{
		gl.POST("", h.CreateGitLabSource)
		gl.DELETE("/:source_id", h.DeleteGitLabSource)
		gl.POST("/:source_id/resync", h.TriggerGitLabResync)
		gl.GET("/:source_id/sync-status", h.GetGitLabSyncStatus)
	}
}
