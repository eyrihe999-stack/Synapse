// router.go user 模块路由注册。
package handler

import (
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/user"
	"github.com/eyrihe999-stack/Synapse/pkg/utils"
	"github.com/gin-gonic/gin"
)

// authIPRateLimitPerMinute 单 IP 每分钟允许的 auth 请求数(注册/登录/刷新合计)。
// 比 chat 的 60/min 严,因为这些是无鉴权入口,主要防爆破。
const authIPRateLimitPerMinute = 30

// RegisterRoutes 注册 user 模块所有路由。
//
// 路由分组:
//   - /api/v1/auth — 注册/登录/刷新(无需鉴权,加 IP 维度兜底限流)
//   - /api/v1/users — 个人资料与 session 管理(需 JWT + session 校验)
func RegisterRoutes(r *gin.Engine, h *Handler, jwtManager *utils.JWTManager, sessionStore user.SessionStore) {
	auth := r.Group("/api/v1/auth", middleware.IPRateLimit(authIPRateLimitPerMinute, time.Minute))
	{
		auth.POST("/register", h.Register)
		auth.POST("/login", h.Login)
		auth.POST("/refresh", h.RefreshToken)
	}

	users := r.Group("/api/v1/users", middleware.JWTAuthWithSession(jwtManager, sessionStore))
	{
		users.GET("/me", h.GetProfile)
		users.PATCH("/me", h.UpdateProfile)
		users.GET("/me/sessions", h.ListSessions)
		users.DELETE("/me/sessions/:device_id", h.KickSession)
		users.POST("/me/sessions/logout-all", h.LogoutAll)
	}
}
