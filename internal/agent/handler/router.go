// router.go agent 模块路由注册。
//
// 路由分组:
//
//  1. /api/v2/agents              — 个人 agent CRUD(无需 org context)
//  2. /api/v2/orgs/:slug/agent-*  — 发布流程(需要 org context + 权限)
//  3. /api/v2/orgs/:slug/agents/  — chat 网关 + session 列表
//  4. /api/v2/orgs/:slug/sessions — session 详情/消息/删除
//  5. /api/v2/agents/:owner_uid/:agent_slug/mcp — MCP 代理(OAuth 保护,RegisterMCPRoutes 单挂)
package handler

import (
	"github.com/eyrihe999-stack/Synapse/internal/agent"
	"github.com/eyrihe999-stack/Synapse/internal/agent/service"
	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	oauthhandler "github.com/eyrihe999-stack/Synapse/internal/oauth/handler"
	oauthmw "github.com/eyrihe999-stack/Synapse/internal/oauth/middleware"
	oauthsvc "github.com/eyrihe999-stack/Synapse/internal/oauth/service"
	orghandler "github.com/eyrihe999-stack/Synapse/internal/organization/handler"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"github.com/eyrihe999-stack/Synapse/pkg/utils"
	"github.com/gin-gonic/gin"
)
var _ = orghandler.GetOrg // keep import if unused after refactor

// RegisterRoutes 注册 agent 模块所有路由。
//
// oauthSvc 可为 nil;非 nil 时 agent CRUD 会额外接受 OAuth access token(给 CLI 用),
// 否则只认 web JWT。登录流程详见 mcp-agent-template。
func RegisterRoutes(
	router *gin.Engine,
	h *AgentHandler,
	jwtManager *utils.JWTManager,
	oauthSvc oauthsvc.Service,
	orgPort service.OrgPort,
	log logger.LoggerInterface,
) {
	// ─── 个人 agent CRUD(无 org context) ─────
	// 用 BearerAuth 双向兼容 web JWT + OAuth access token,让 CLI(agent register)能走 OAuth 路径。
	agentsBase := router.Group("/api/v2/agents")
	agentsBase.Use(middleware.BearerAuth(jwtManager, oauthSvc))
	{
		agentsBase.POST("", h.CreateAgent)
		agentsBase.GET("/mine", h.ListMyAgents)
		agentsBase.GET("/:id", h.GetAgent)
		agentsBase.PATCH("/:id", h.UpdateAgent)
		agentsBase.DELETE("/:id", h.DeleteAgent)
	}

	// ─── 需要 org context 的接口 ─────
	orgCtx := router.Group("/api/v2/orgs/:slug")
	orgCtx.Use(
		middleware.JWTAuth(jwtManager),
		OrgContextMiddleware(orgPort, log),
	)
	{
		// 发布流程
		orgCtx.POST("/agent-publishes", PermissionMiddleware("agent.publish", log), h.SubmitPublish)
		orgCtx.GET("/agent-publishes", h.ListPublishes)
		orgCtx.DELETE("/agent-publishes/:id", PermissionMiddleware("agent.unpublish.self", log), h.RevokePublish)
		orgCtx.POST("/agent-publishes/:id/approve", PermissionMiddleware("agent.review", log), h.ApprovePublish)
		orgCtx.POST("/agent-publishes/:id/reject", PermissionMiddleware("agent.review", log), h.RejectPublish)

		// Chat 网关:单独收紧 body 上限到 128KB(远小于全局 1MB),
		// 避免恶意请求在 service 层 rune 校验前先吃掉 IO + JSON 解析开销。
		orgCtx.POST("/agents/:owner_uid/:agent_slug/chat",
			middleware.MaxBodySize(agent.MaxChatRequestBodyBytes),
			PermissionMiddleware("agent.invoke", log),
			h.Chat,
		)

		// Session 列表(属于某个 agent)
		orgCtx.GET("/agents/:owner_uid/:agent_slug/sessions", h.ListSessions)

		// Session 详情/消息/删除
		orgCtx.GET("/sessions/:session_id", h.GetSession)
		orgCtx.GET("/sessions/:session_id/messages", h.GetSessionMessages)
		orgCtx.DELETE("/sessions/:session_id", h.DeleteSession)
	}
}

// RegisterMCPRoutes 挂 agent 的 MCP 代理路由。
//
// 路径 /api/v2/agents/:owner_uid/:agent_slug/mcp 刻意不含 :slug ——
// orgID 必须来自 OAuth token 的 claims,不从 URL 猜;URL 里再出现 slug 反而是冗余
// 且容易让人误以为可以 spoof。
//
// 鉴权:OAuth middleware(要求 mcp scope)。CORS 放宽,给 Claude.ai / Inspector 跨源访问。
func RegisterMCPRoutes(
	router *gin.Engine,
	h *MCPHandler,
	oauthSvc oauthsvc.Service,
	resourceMetadataURL string,
	log logger.LoggerInterface,
) {
	cors := oauthhandler.CORSAllowAll()
	g := router.Group("/api/v2/agents/:owner_uid/:agent_slug", cors)
	g.OPTIONS("/mcp")
	g.POST("/mcp", oauthmw.AccessToken(oauthSvc, resourceMetadataURL, log), h.Invoke)
}
