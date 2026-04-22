// router.go 组织模块路由注册。
//
// 路由分组:
//
//  1. /api/v2/orgs                    — 不需要 org context 的接口
//     - POST /orgs                   (创建 org)
//     - GET  /orgs/mine               (列出我的 org)
//     - GET  /orgs/check-slug?slug=x  (创建前 slug 可用性预检)
//
//  2. /api/v2/orgs/:slug              — 需要 OrgContextMiddleware 的接口
//     M4 后改为按 perm 检查;原 RequireOwner 替换为 RequirePerm("xxx")。
//     - GET    /orgs/:slug
//     - PATCH  /orgs/:slug                            org.update
//     - DELETE /orgs/:slug                            org.dissolve
//     - GET    /orgs/:slug/members
//     - DELETE /orgs/:slug/members/me                (任何成员可)
//     - DELETE /orgs/:slug/members/:user_id          member.remove
//     - PATCH  /orgs/:slug/members/:user_id/role     member.role_assign
//     - GET    /orgs/:slug/roles
//     - POST   /orgs/:slug/roles                     role.manage
//     - PATCH  /orgs/:slug/roles/:role_slug          role.manage
//     - DELETE /orgs/:slug/roles/:role_slug          role.manage
//     - POST   /orgs/:slug/invitations               member.invite
//     - GET    /orgs/:slug/invitations               member.invite
//     - DELETE /orgs/:slug/invitations/:id           member.invite
//     - POST   /orgs/:slug/invitations/:id/resend    member.invite
//
//  3. /api/v2/invitations             — 邀请独立路由(跨 org 上下文)
//     - GET    /invitations/preview?token=xxx        (未登录,预览邀请摘要)
//     - POST   /invitations/accept                   (登录后接受邀请)
//
// 权限检查通过 RequirePerm middleware 完成,需要先经过 PermContextMiddleware
// (由 main.go 通过 permCtxMW 参数注入,避免 org/handler 反向 import permission/handler)。
package handler

import (
	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/organization/service"
	"github.com/eyrihe999-stack/Synapse/internal/user"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/common/jwt"
	"github.com/gin-gonic/gin"
)

// PermFactory 返回对应 perm 的 RequirePerm 中间件。main.go 用 permhandler.RequirePerm 注入。
//
// 注:不直接 import permhandler 是为避免 org/handler ↔ permission/handler 循环依赖
// (permission/handler 已经 import 了 org/handler 来读 OrgContextMiddleware)。
type PermFactory func(perm string) gin.HandlerFunc

// RegisterRoutes 注册 organization 模块的所有路由。
//
// 参数:
//   - permCtxMW:M4 PermContextMiddleware,挂在 OrgContextMiddleware 之后预加载 user perms
//   - requirePerm:M4 perm 中间件工厂,sensitive endpoint 调它包一层
func RegisterRoutes(
	router *gin.Engine,
	h *OrgHandler,
	jwtManager *jwt.JWTManager,
	sessionStore user.SessionStore,
	orgSvc service.OrgService,
	permCtxMW gin.HandlerFunc,
	requirePerm PermFactory,
	log logger.LoggerInterface,
) {
	// ─── 不需要 org context 的 org 接口 ──
	orgsBase := router.Group("/api/v2/orgs")
	orgsBase.Use(middleware.JWTAuthWithSession(jwtManager, sessionStore))
	{
		orgsBase.POST("", h.CreateOrg)
		orgsBase.GET("/mine", h.ListMyOrgs)
		orgsBase.GET("/check-slug", h.CheckSlug)
	}

	// ─── 需要 org context 的接口 ──
	orgCtx := router.Group("/api/v2/orgs/:slug")
	orgCtx.Use(
		middleware.JWTAuthWithSession(jwtManager, sessionStore),
		OrgContextMiddleware(orgSvc, log),
		permCtxMW, // M4:加载 user 在该 org 的 groups + permissions 到 ctx
	)
	{
		// org 本体
		orgCtx.GET("", h.GetOrg)
		orgCtx.PATCH("", requirePerm("org.update"), h.UpdateOrg)
		orgCtx.DELETE("", requirePerm("org.dissolve"), h.DissolveOrg)

		// 成员
		orgCtx.GET("/members", h.ListMembers)
		orgCtx.DELETE("/members/me", h.LeaveOrg) // 自我退出不查 perm
		orgCtx.DELETE("/members/:user_id", requirePerm("member.remove"), h.RemoveMember)
		orgCtx.PATCH("/members/:user_id/role", requirePerm("member.role_assign"), h.AssignRoleToMember)

		// 角色
		orgCtx.GET("/roles", h.ListRoles)
		orgCtx.POST("/roles", requirePerm("role.manage"), h.CreateRole)
		orgCtx.PATCH("/roles/:role_slug", requirePerm("role.manage"), h.UpdateRole)
		orgCtx.DELETE("/roles/:role_slug", requirePerm("role.manage"), h.DeleteRole)
		// M5.2:任意 role(含系统角色)的 permissions 编辑;owner 专属 perm
		orgCtx.PATCH("/roles/:role_slug/permissions", requirePerm("role.manage_system"), h.UpdateRolePermissions)

		// 邀请(创建/列/搜/撤销/重发 都是 member.invite)
		orgCtx.POST("/invitations", requirePerm("member.invite"), h.CreateInvitation)
		orgCtx.GET("/invitations", requirePerm("member.invite"), h.ListInvitations)
		orgCtx.GET("/invitations/search", requirePerm("member.invite"), h.SearchInviteCandidates)
		orgCtx.DELETE("/invitations/:id", requirePerm("member.invite"), h.RevokeInvitation)
		orgCtx.POST("/invitations/:id/resend", requirePerm("member.invite"), h.ResendInvitation)
	}

	// ─── 邀请独立路由组 ──
	//
	// Preview 不需要登录:前端落地页拿 URL 里的 token 调一次,渲染"XX 邀请你加入 YY"。
	// Accept 需要登录:登录用户 email 必须匹配邀请邮件目标。
	invitations := router.Group("/api/v2/invitations")
	{
		// 未登录 —— 邮件落地页用
		invitations.GET("/preview", h.PreviewInvitation)

		// 登录态 —— 站内收件箱 + 基于 token 的接受路径
		invitationsAuth := invitations.Group("")
		invitationsAuth.Use(middleware.JWTAuthWithSession(jwtManager, sessionStore))
		{
			invitationsAuth.POST("/accept", h.AcceptInvitation)     // by token (邮件链接)
			invitationsAuth.GET("/mine", h.ListMyInvitations)        // 我的收件箱(按 email 匹配)
			invitationsAuth.GET("/sent", h.ListSentInvitations)      // 我的发件箱(按 inviter_user_id 聚合)
			invitationsAuth.POST("/:id/accept", h.AcceptInvitationByID) // 站内 by id
			invitationsAuth.POST("/:id/reject", h.RejectInvitation)  // 被邀请人拒绝
		}
	}
}
