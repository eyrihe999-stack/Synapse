// router.go agent 模块路由注册。
//
// 路由分组:
//
//  1. /api/v2/agents              — 个人 agent CRUD(无需 org context)
//  2. /api/v2/orgs/:slug/agent-*  — 发布流程(需要 org context + 权限)
//  3. /api/v2/orgs/:slug/agents/  — chat 网关 + session 列表
//  4. /api/v2/orgs/:slug/sessions — session 详情/消息/删除
package handler

import (
	"github.com/eyrihe999-stack/Synapse/internal/agent/service"
	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"github.com/eyrihe999-stack/Synapse/pkg/utils"
	"github.com/gin-gonic/gin"
)

// RegisterRoutes 注册 agent 模块所有路由。
func RegisterRoutes(
	router *gin.Engine,
	h *AgentHandler,
	jwtManager *utils.JWTManager,
	orgPort service.OrgPort,
	log logger.LoggerInterface,
) {
	// ─── 个人 agent CRUD(无 org context) ─────
	agentsBase := router.Group("/api/v2/agents")
	agentsBase.Use(middleware.JWTAuth(jwtManager))
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

		// Chat 网关
		orgCtx.POST("/agents/:owner_uid/:agent_slug/chat",
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
