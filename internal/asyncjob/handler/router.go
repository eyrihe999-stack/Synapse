// router.go asyncjob 路由注册。只有通用查询端点 —— 触发端点按业务模块自己挂。
package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/pkg/utils"
)

// RegisterRoutes 挂 /api/v2/async-jobs/:id,需要 JWT。
// 权限校验在 handler 内做 (owner = current_user)。
func RegisterRoutes(r *gin.Engine, h *Handler, jwtManager *utils.JWTManager) {
	g := r.Group("/api/v2/async-jobs")
	g.Use(middleware.JWTAuth(jwtManager))
	{
		g.GET("", h.ListJobs)
		g.GET("/:id", h.GetJob)
	}
}
