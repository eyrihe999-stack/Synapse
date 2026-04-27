// router.go transport 模块路由挂载。
//
// 只有一个端点:GET /api/v1/agent/ws,给 agent 发起 WebSocket upgrade。
// 不挂 JWT 中间件 —— agent 鉴权由 Authenticator 自己处理(handshake header)。
//
// 未来若 web 侧也用 WS 接入,请开一个独立 group(如 /api/v1/user/ws),
// 用单独的 Handler + origin 校验,不要和 agent 端共用。
package handler

import (
	"time"

	"github.com/gin-gonic/gin"
)

// RegisterRoutes 挂 agent WS upgrade 端点。调用点:cmd/synapse/main.go。
func RegisterRoutes(r *gin.Engine, h *Handler) {
	g := r.Group("/api/v1/agent")
	{
		g.GET("/ws", h.Upgrade)
	}
}

// deadlineSoon 给 WriteControl 的关闭帧写一个短超时 —— 关连接时不该等很久。
func deadlineSoon() time.Time {
	return time.Now().Add(2 * time.Second)
}
