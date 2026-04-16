// router.go agent 模块路由注册。
//
// 路由分组:
//
//  1. /api/v2/agents                  — 个人 agent CRUD(无需 org context)
//  2. /api/v2/agents/:id/methods      — method 管理(作者)
//  3. /api/v2/invocations/:id         — 取消接口(按 invocation 校验发起人)
//  4. /api/v2/orgs/:slug/agent-*      — 发布/审核/下架/封禁(需要 org context + 权限)
//  5. /api/v2/orgs/:slug/agents/:owner_uid/:agent_slug/invoke — 网关 ★
//  6. /api/v2/orgs/:slug/audits       — 审计查询
package handler

import (
	"github.com/eyrihe999-stack/Synapse/internal/agent/service"
	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"github.com/eyrihe999-stack/Synapse/pkg/utils"
	"github.com/gin-gonic/gin"
)

// RegisterRoutes 注册 agent 模块所有路由。
//
// 参数:
//   - router: gin 引擎
//   - h: AgentHandler 实例
//   - jwtManager: JWT 校验器
//   - orgPort: agent → organization 的端口,用于 OrgContextMiddleware
//   - log: 日志
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
		agentsBase.POST("/:id/secret/rotate", h.RotateSecret)
		agentsBase.GET("/:id/health", h.GetHealth)

		// Method 管理
		agentsBase.GET("/:id/methods", h.ListMethods)
		agentsBase.POST("/:id/methods", h.CreateMethod)
		agentsBase.PATCH("/:id/methods/:method_id", h.UpdateMethod)
		agentsBase.DELETE("/:id/methods/:method_id", h.DeleteMethod)
	}

	// ─── 取消 invocation ─────
	invocationsBase := router.Group("/api/v2/invocations")
	invocationsBase.Use(middleware.JWTAuth(jwtManager))
	{
		invocationsBase.DELETE("/:invocation_id", h.CancelInvocation)
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
		orgCtx.POST("/agent-publishes/:id/ban", PermissionMiddleware("agent.ban", log), h.BanPublish)

		// 调用网关 ★
		orgCtx.POST("/agents/:owner_uid/:agent_slug/invoke",
			PermissionMiddleware("agent.invoke", log),
			h.InvokeAgent,
		)

		// 审计
		orgCtx.GET("/audits", PermissionMiddleware("audit.read", log), h.ListAudits)
		orgCtx.GET("/audits/mine", PermissionMiddleware("audit.read.self", log), h.ListMyAudits)
		orgCtx.GET("/audits/:invocation_id", h.GetAuditDetail)
	}
}
