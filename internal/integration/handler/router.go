// router.go integration 模块路由注册。
//
// 路由分 3 层:
//
//  1. 顶层公开 callback —— 飞书 302 过来,不能带 :slug(redirect_uri 白名单精确匹配要求)
//     GET  /api/v2/integrations/feishu/callback
//
//  2. user-scope 操作(只需 JWT,不需 org 级权限)—— "我要不要授权我自己的 provider 账号"
//     GET    /api/v2/orgs/:slug/integrations/feishu           状态
//     POST   /api/v2/orgs/:slug/integrations/feishu/connect   构造授权 URL
//     POST   /api/v2/orgs/:slug/integrations/feishu/sync      触发异步导入
//     DELETE /api/v2/orgs/:slug/integrations/feishu           断开
//     GET    /api/v2/orgs/:slug/integrations/gitlab           状态
//     PUT    /api/v2/orgs/:slug/integrations/gitlab           贴 PAT
//     DELETE /api/v2/orgs/:slug/integrations/gitlab           断开
//
//  3. admin-scope 实例配置(需 PermIntegrationManage)—— "本 org 对该 provider 的实例配置"
//     GET    /api/v2/orgs/:slug/integrations/feishu/config    飞书 App 凭证
//     PUT    /api/v2/orgs/:slug/integrations/feishu/config
//     DELETE /api/v2/orgs/:slug/integrations/feishu/config
//     GET    /api/v2/orgs/:slug/integrations/gitlab/config    GitLab 实例地址
//     PUT    /api/v2/orgs/:slug/integrations/gitlab/config
//     DELETE /api/v2/orgs/:slug/integrations/gitlab/config
//
// 2 和 3 共享前缀但权限语义不同,分多个 Group 挂中间件。
package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/organization"
	orghandler "github.com/eyrihe999-stack/Synapse/internal/organization/handler"
	orgsvc "github.com/eyrihe999-stack/Synapse/internal/organization/service"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"github.com/eyrihe999-stack/Synapse/pkg/utils"
)

// RegisterRoutes 注册 integration 模块所有 HTTP 路由。
//
// 参数:
//   - h: user-scope handler(OAuth 流 / PAT Connect)
//   - feishuConfigH / gitlabConfigH: 各 provider 的 admin-scope handler;nil 时不挂对应端点
//   - orgSvc / roleSvc / log: 给 OrgContextMiddleware + PermissionMiddleware 用
func RegisterRoutes(
	router *gin.Engine,
	h *Handler,
	feishuConfigH *FeishuConfigHandler,
	gitlabConfigH *GitLabConfigHandler,
	jwtManager *utils.JWTManager,
	orgSvc orgsvc.OrgService,
	roleSvc orgsvc.RoleService,
	log logger.LoggerInterface,
) {
	// 1) 顶层 callback —— 公开访问(飞书调的),不过 JWT middleware。
	router.GET("/api/v2/integrations/feishu/callback", h.FeishuCallback)

	// 2) user-scope:用户自己的集成管理。只要 JWT,不走 org permission middleware
	//    —— integration 是"我要不要授权我自己的账号"这种 personal 操作。
	orgCtx := router.Group("/api/v2/orgs/:slug/integrations")
	orgCtx.Use(middleware.JWTAuth(jwtManager))
	{
		// 各 provider 独立挂 —— 只有 service 注入了才挂路由,避免未配置 provider 被调用时 nil panic。
		if h.HasFeishu() {
			orgCtx.GET("/feishu", h.FeishuStatus)
			orgCtx.POST("/feishu/connect", h.FeishuConnect)
			orgCtx.POST("/feishu/sync", h.FeishuSync)
			orgCtx.DELETE("/feishu", h.FeishuDisconnect)
		}
		if h.HasGitLab() {
			orgCtx.GET("/gitlab", h.GitLabStatus)
			orgCtx.PUT("/gitlab", h.GitLabConnect)
			orgCtx.POST("/gitlab/sync", h.GitLabSync)
			orgCtx.DELETE("/gitlab", h.GitLabDisconnect)
		}
	}

	// 3) admin-scope:各 provider 实例配置。OrgContextMiddleware + PermissionMiddleware。
	//    各 handler 可能为 nil(测试 / 特殊部署不想开 admin 写入);为 nil 就跳过。
	if feishuConfigH != nil {
		mountAdminConfig(
			router, "/api/v2/orgs/:slug/integrations/feishu/config",
			feishuConfigH.GetConfig, feishuConfigH.PutConfig, feishuConfigH.DeleteConfig,
			jwtManager, orgSvc, roleSvc, log,
		)
	}
	if gitlabConfigH != nil {
		mountAdminConfig(
			router, "/api/v2/orgs/:slug/integrations/gitlab/config",
			gitlabConfigH.GetConfig, gitlabConfigH.PutConfig, gitlabConfigH.DeleteConfig,
			jwtManager, orgSvc, roleSvc, log,
		)
	}
}

// mountAdminConfig 公共装配:/<path> 上挂 GET(成员可见)/ PUT / DELETE(PermIntegrationManage)。
// 抽出来避免 feishu + gitlab 两份重复代码,加 provider 时一行即可。
func mountAdminConfig(
	router *gin.Engine,
	path string,
	get, put, del gin.HandlerFunc,
	jwtManager *utils.JWTManager,
	orgSvc orgsvc.OrgService,
	roleSvc orgsvc.RoleService,
	log logger.LoggerInterface,
) {
	g := router.Group(path)
	g.Use(
		middleware.JWTAuth(jwtManager),
		orghandler.OrgContextMiddleware(orgSvc, roleSvc, log),
	)
	// GET 放宽到任何成员可见 —— 让普通成员判断"本 org 是否已配置该 provider,能否走连接按钮"。
	g.GET("", get)
	g.PUT("", orghandler.PermissionMiddleware(organization.PermIntegrationManage, log), put)
	g.DELETE("", orghandler.PermissionMiddleware(organization.PermIntegrationManage, log), del)
}
