// router.go user 模块路由注册。
package handler

import (
	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/user"
	"github.com/eyrihe999-stack/Synapse/pkg/utils"
	"github.com/gin-gonic/gin"
)

// RegisterRoutes 注册 user 模块所有路由。
//
// 路由分组:
//   - /api/v1/auth — 注册/登录/刷新(无需鉴权)
//   - /api/v1/users — 个人资料与 session 管理(需 JWT + session 校验)
func RegisterRoutes(r *gin.Engine, h *Handler, jwtManager *utils.JWTManager, sessionStore user.SessionStore) {
	auth := r.Group("/api/v1/auth")
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
