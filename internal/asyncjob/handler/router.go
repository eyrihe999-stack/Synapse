// router.go asyncjob 路由注册。只暴露单条轮询端点 —— 触发端点按业务模块自己挂,
// 列表端点(ListJobs)前端未用已下线,需要时再按需补回。
package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/user"
	"github.com/eyrihe999-stack/Synapse/internal/common/jwt"
)

// RegisterRoutes 挂 /api/v2/async-jobs/:id,需要 JWT + session。
// 权限校验在 handler 内做 (owner = current_user)。
// sessionStore 用于 web JWT 路径的 Redis session 校验(踢设备/注销即时生效)。
func RegisterRoutes(r *gin.Engine, h *Handler, jwtManager *jwt.JWTManager, sessionStore user.SessionStore) {
	g := r.Group("/api/v2/async-jobs")
	g.Use(middleware.JWTAuthWithSession(jwtManager, sessionStore))
	{
		g.GET("/:id", h.GetJob)
	}
}
