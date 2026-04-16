// router.go 组织模块路由注册。
//
// 路由分组:
//
//  1. /api/v2/invitations/*          — 用户态接口,只需 JWT
//     - GET /invitations/mine
//     - POST /invitations/:id/accept
//     - POST /invitations/:id/reject
//
//  2. /api/v2/orgs                    — 不需要 org context 的接口
//     - POST /orgs                   (创建 org)
//     - GET  /orgs/mine               (列出我的 org)
//
//  3. /api/v2/orgs/:slug              — 需要 OrgContextMiddleware 的接口
//     - GET    /orgs/:slug           (详情)
//     - PATCH  /orgs/:slug           (更新基础信息) [PermOrgUpdate]
//     - PATCH  /orgs/:slug/settings  (更新设置)     [PermOrgSettingsReviewToggle]
//     - POST   /orgs/:slug/transfer  (发起转让)     [PermOrgTransfer]
//     - DELETE /orgs/:slug           (解散)         [PermOrgDelete]
//     - GET    /orgs/:slug/members
//     - DELETE /orgs/:slug/members/me
//     - DELETE /orgs/:slug/members/:user_id                 [PermMemberRemove]
//     - PATCH  /orgs/:slug/members/:user_id/role            [PermMemberRoleAssign]
//     - POST   /orgs/:slug/invitations/search-invitees     [PermMemberInvite]
//     - POST   /orgs/:slug/invitations                      [PermMemberInvite]
//     - GET    /orgs/:slug/invitations                      [PermMemberInvite]
//     - DELETE /orgs/:slug/invitations/:id                  [PermMemberInvite]
//     - GET    /orgs/:slug/roles
//     - POST   /orgs/:slug/roles                             [PermRoleManage]
//     - PATCH  /orgs/:slug/roles/:id                         [PermRoleManage]
//     - DELETE /orgs/:slug/roles/:id                         [PermRoleManage]
//     - GET    /orgs/:slug/permissions
package handler

import (
	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/organization"
	"github.com/eyrihe999-stack/Synapse/internal/organization/service"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"github.com/eyrihe999-stack/Synapse/pkg/utils"
	"github.com/gin-gonic/gin"
)

// RegisterRoutes 注册 organization 模块的所有路由。
//
// 参数:
//   - router: gin 引擎
//   - h: OrgHandler 实例
//   - jwtManager: JWT 校验器
//   - orgSvc / roleSvc: OrgContextMiddleware 依赖
//   - log: 日志
func RegisterRoutes(
	router *gin.Engine,
	h *OrgHandler,
	jwtManager *utils.JWTManager,
	orgSvc service.OrgService,
	roleSvc service.RoleService,
	log logger.LoggerInterface,
) {
	// ─── 用户态邀请接口(不需要 org context) ──
	invPublic := router.Group("/api/v2/invitations")
	invPublic.Use(middleware.JWTAuth(jwtManager))
	{
		invPublic.GET("/mine", h.ListMyInvitations)
		invPublic.POST("/:id/accept", h.AcceptInvitation)
		invPublic.POST("/:id/reject", h.RejectInvitation)
	}

	// ─── 不需要 org context 的 org 接口 ──
	orgsBase := router.Group("/api/v2/orgs")
	orgsBase.Use(middleware.JWTAuth(jwtManager))
	{
		orgsBase.POST("", h.CreateOrg)
		orgsBase.GET("/mine", h.ListMyOrgs)
	}

	// ─── 需要 org context 的接口 ──
	orgCtx := router.Group("/api/v2/orgs/:slug")
	orgCtx.Use(
		middleware.JWTAuth(jwtManager),
		OrgContextMiddleware(orgSvc, roleSvc, log),
	)
	{
		// org 本体
		orgCtx.GET("", h.GetOrg)
		orgCtx.PATCH("", PermissionMiddleware(organization.PermOrgUpdate, log), h.UpdateOrg)
		orgCtx.PATCH("/settings", PermissionMiddleware(organization.PermOrgSettingsReviewToggle, log), h.UpdateOrgSettings)
		orgCtx.POST("/transfer", PermissionMiddleware(organization.PermOrgTransfer, log), h.TransferOwnership)
		orgCtx.DELETE("", PermissionMiddleware(organization.PermOrgDelete, log), h.DissolveOrg)

		// 成员
		orgCtx.GET("/members", h.ListMembers)
		orgCtx.DELETE("/members/me", h.LeaveOrg)
		orgCtx.DELETE("/members/:user_id", PermissionMiddleware(organization.PermMemberRemove, log), h.RemoveMember)
		orgCtx.PATCH("/members/:user_id/role", PermissionMiddleware(organization.PermMemberRoleAssign, log), h.AssignMemberRole)

		// 邀请
		orgCtx.POST("/invitations/search-invitees", PermissionMiddleware(organization.PermMemberInvite, log), h.SearchInvitees)
		orgCtx.POST("/invitations", PermissionMiddleware(organization.PermMemberInvite, log), h.CreateInvitation)
		orgCtx.GET("/invitations", PermissionMiddleware(organization.PermMemberInvite, log), h.ListOrgInvitations)
		orgCtx.DELETE("/invitations/:id", PermissionMiddleware(organization.PermMemberInvite, log), h.RevokeInvitation)

		// 角色
		orgCtx.GET("/roles", h.ListRoles)
		orgCtx.POST("/roles", PermissionMiddleware(organization.PermRoleManage, log), h.CreateRole)
		orgCtx.PATCH("/roles/:id", PermissionMiddleware(organization.PermRoleManage, log), h.UpdateRole)
		orgCtx.DELETE("/roles/:id", PermissionMiddleware(organization.PermRoleManage, log), h.DeleteRole)
		orgCtx.GET("/permissions", h.ListPermissions)
	}
}
