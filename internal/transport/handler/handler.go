// Package handler transport 模块 HTTP 入口 —— 仅 WebSocket upgrade 端点。
//
// agent 主动拨进来:
//
//	GET /api/v1/agent/ws
//	  X-Agent-ID:  agent-kb-writer-01
//	  X-Agent-Key: <apikey>
//	  Upgrade:     websocket
//
// 鉴权失败返 401 + BaseResponse(走业务码 CodeTransportAuthFailed),不升级 WS。
// 升级成功后 conn 生命周期由 Hub 管,handler 返回即可。
//
// 本 handler 不挂任何业务 method —— 业务订阅走 internal/agents/。
package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	"github.com/eyrihe999-stack/Synapse/internal/transport"
	tsvc "github.com/eyrihe999-stack/Synapse/internal/transport/service"
)

// Handler 仅持 Hub + Authenticator + upgrader,无状态,可被 gin 直接挂。
type Handler struct {
	hub      *tsvc.LocalHub
	auth     transport.Authenticator
	upgrader websocket.Upgrader
	log      logger.LoggerInterface
}

// New 构造 transport HTTP handler。
//
// auth 为 nil → panic(必须显式注入,避免 dev 误用 allow-all 进生产);
// 若需 dev 便利走"允许所有",调用方显式构造 AllowAllAuthenticator 传入。
func New(hub *tsvc.LocalHub, auth transport.Authenticator, log logger.LoggerInterface) *Handler {
	if hub == nil || auth == nil || log == nil {
		panic("transport/handler: hub / auth / log must not be nil")
	}
	return &Handler{
		hub:  hub,
		auth: auth,
		log:  log,
		upgrader: websocket.Upgrader{
			// agent 是内网可信客户端,不做 origin 校验(agent 没有 Origin header)。
			// 将来加 web 用 WS 时要换一个单独的 upgrader 做 origin allowlist。
			CheckOrigin: func(r *http.Request) bool { return true },
			// 读缓冲给 64KB;大 payload(KB 批量搜索结果)走长消息多帧不在这一档。
			ReadBufferSize:  64 * 1024,
			WriteBufferSize: 64 * 1024,
		},
	}
}

// Upgrade GET /api/v1/agent/ws —— WS 升级入口。
//
// 响应:
//   - 101 Switching Protocols:升级成功,随后 WS 帧由 Hub 处理
//   - 400 Bad Request:header 格式错
//   - 401 Unauthorized:鉴权失败
//   - 409 Conflict:同 agent_id 已有活跃连接(V1 单连接策略)
//   - 500 Internal Server Error:其它内部错
func (h *Handler) Upgrade(c *gin.Context) {
	ctx := c.Request.Context()
	// 1. handshake 鉴权 —— 升级前完成,失败直接 HTTP 4xx,不升 WS。
	meta, err := h.auth.Authenticate(ctx, c.Request)
	if err != nil {
		h.handleAuthError(c, err)
		return
	}

	// 2. WS 升级。Upgrade 内部若失败会自己写 HTTP 响应,我们只需 log。
	ws, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		h.log.WarnCtx(ctx, "transport: ws upgrade failed", map[string]any{
			"agent_id": string(meta.AgentID), "err": err.Error(),
		})
		return
	}

	// 3. 移交 Hub 接管连接。attach 失败需关 ws(Upgrade 已写 101,不能再回 HTTP 错)。
	if err := h.hub.Attach(ctx, ws, meta); err != nil {
		h.log.WarnCtx(ctx, "transport: hub attach failed", map[string]any{
			"agent_id": string(meta.AgentID), "err": err.Error(),
		})
		// 发业务码给 agent 便于 SDK 决策(重试?reset?),再关连接。
		closeCode := websocket.CloseInternalServerErr
		closeMsg := err.Error()
		switch {
		case errors.Is(err, transport.ErrDuplicateAgentID):
			closeCode = websocket.ClosePolicyViolation
		case errors.Is(err, transport.ErrHubClosed):
			closeCode = websocket.CloseGoingAway
		}
		_ = ws.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(closeCode, closeMsg),
			deadlineSoon(),
		)
		_ = ws.Close()
		return
	}
	// ws 所有权已交 Hub,这里直接返回。
}

// handleAuthError 把 sentinel error 映射到 HTTP 响应 + BaseResponse。
func (h *Handler) handleAuthError(c *gin.Context, err error) {
	ctx := c.Request.Context()
	switch {
	case errors.Is(err, transport.ErrInvalidHandshake):
		h.log.WarnCtx(ctx, "transport: invalid handshake", map[string]any{
			"ip": c.ClientIP(), "err": err.Error(),
		})
		c.JSON(http.StatusBadRequest, response.BaseResponse{
			Code:    transport.CodeTransportInvalidHandshake,
			Message: "invalid handshake",
			Error:   err.Error(),
		})
	case errors.Is(err, transport.ErrAuthFailed):
		h.log.WarnCtx(ctx, "transport: auth failed", map[string]any{
			"ip": c.ClientIP(), "err": err.Error(),
		})
		c.JSON(http.StatusUnauthorized, response.BaseResponse{
			Code:    transport.CodeTransportAuthFailed,
			Message: "auth failed",
			Error:   err.Error(),
		})
	default:
		h.log.ErrorCtx(ctx, "transport: handshake internal error", err, map[string]any{
			"ip": c.ClientIP(),
		})
		c.JSON(http.StatusInternalServerError, response.BaseResponse{
			Code:    transport.CodeTransportInternal,
			Message: "internal error",
			Error:   err.Error(),
		})
	}
}
