// router.go task 模块路由注册。
//
// 路由分组(全部需要 JWT 登录):
//
//	/api/v2/tasks
//	  POST   /                       — 创建 task(body.channel_id)
//	  GET    /:id                    — 获取 task 详情(含 reviewers / submissions / reviews)
//	  POST   /:id/claim              — 认领(open/未指派 → in_progress)
//	  POST   /:id/submit             — 提交产物(assignee)
//	  POST   /:id/review             — 审批(reviewer 白名单内)
//	  POST   /:id/cancel             — 取消(creator 或 assignee)
//
//	/api/v2/channels/:id/tasks       — 列出 channel 下的 task
//	/api/v2/users/me/tasks           — 列出指派给我的 task
//
// 权限在 service 层统一做。
package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/common/jwt"
	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/user"
)

// RegisterRoutes 把 task 模块的所有 endpoint 挂到 gin.Engine。
func RegisterRoutes(
	router *gin.Engine,
	h *Handler,
	jwtManager *jwt.JWTManager,
	sessionStore user.SessionStore,
) {
	tasks := router.Group("/api/v2/tasks")
	tasks.Use(middleware.JWTAuthWithSession(jwtManager, sessionStore))
	{
		tasks.POST("", h.CreateTask)
		tasks.GET("/:id", h.GetTask)
		tasks.POST("/:id/claim", h.ClaimTask)
		tasks.POST("/:id/submit", h.SubmitTask)
		tasks.POST("/:id/review", h.ReviewTask)
		tasks.POST("/:id/cancel", h.CancelTask)
		// 变更 assignee / reviewers(方案 A):权限 creator 或 channel owner。
		// assignee:任何非终态可改;reviewers:只在 submitted 之前可改。
		tasks.PATCH("/:id/assignee", h.UpdateAssignee)
		tasks.PATCH("/:id/reviewers", h.UpdateReviewers)
	}

	// channel 维度列 task
	channelTasks := router.Group("/api/v2/channels")
	channelTasks.Use(middleware.JWTAuthWithSession(jwtManager, sessionStore))
	{
		channelTasks.GET("/:id/tasks", h.ListTasksByChannel)
	}

	// "我的" 视图
	myTasks := router.Group("/api/v2/users/me")
	myTasks.Use(middleware.JWTAuthWithSession(jwtManager, sessionStore))
	{
		myTasks.GET("/tasks", h.ListMyTasks)
	}
}
