// chat_handler.go 对话网关 + SSE 流式 handler。
package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/agent/dto"
	"github.com/eyrihe999-stack/Synapse/internal/agent/service"
	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
	"github.com/gin-gonic/gin"
)

// Chat 处理对话请求,根据 stream 字段分发到流式或同步处理。
// POST /api/v2/orgs/:slug/agents/:owner_uid/:agent_slug/chat
func (h *AgentHandler) Chat(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	membership, ok := GetMembership(c)
	if !ok {
		response.InternalServerError(c, "Missing membership context", "")
		return
	}
	ownerUID, ok := parseUintParam(c, "owner_uid")
	if !ok {
		return
	}
	agentSlug := c.Param("agent_slug")
	if agentSlug == "" {
		response.BadRequest(c, "Missing agent_slug", "")
		return
	}

	var req dto.ChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid body", err.Error())
		return
	}

	svcReq := service.ChatServiceRequest{
		OrgID:        org.ID,
		OrgSlug:      org.Slug,
		CallerUserID: userID,
		CallerRole:   membership.RoleName,
		OwnerUID:     ownerUID,
		AgentSlug:    agentSlug,
		Message:      req.Message,
		SessionID:    req.SessionID,
		Stream:       req.Stream,
	}

	if req.Stream {
		h.handleStreamChat(c, svcReq)
		return
	} else {
		h.handleSyncChat(c, svcReq)
		return
	}
}

// handleSyncChat 处理非流式对话请求,返回完整 JSON 响应。
func (h *AgentHandler) handleSyncChat(c *gin.Context, req service.ChatServiceRequest) {
	resp, err := h.chatSvc.Chat(c.Request.Context(), req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}

// handleStreamChat 处理流式对话请求,通过 SSE 推送事件。
func (h *AgentHandler) handleStreamChat(c *gin.Context, req service.ChatServiceRequest) {
	// 设置 SSE headers
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	// SSE 长连接:清掉 http.Server.WriteTimeout 对此连接的限制,
	// 超时由 agent.TimeoutSeconds(service 层的 context.WithTimeout)独立控制。
	if rc := http.NewResponseController(c.Writer); rc != nil {
		if err := rc.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
			h.logger.WarnCtx(c.Request.Context(), "clear SSE write deadline failed", map[string]any{"error": err.Error()})
		}
	}

	writer := &ginSSEWriter{c: c}
	//sayso-lint:ignore err-swallow
	_, err := h.chatSvc.ChatStream(c.Request.Context(), req, writer)
	if err != nil {
		// 用 json.Marshal 而不是 sprintf,避免 err.Error() 含 " 或换行时 SSE event 变成非法 JSON。
		payload, mErr := json.Marshal(map[string]string{"message": err.Error()})
		if mErr != nil {
			payload = []byte(`{"message":"internal error"}`)
		}
		//sayso-lint:ignore err-swallow
		writer.WriteEvent("error", string(payload))
		writer.Flush()
		h.logger.WarnCtx(c.Request.Context(), "stream chat error", map[string]any{"error": err.Error()})
	}
}

// ginSSEWriter 将 SSE 事件写入 gin.ResponseWriter,实现 SSEWriter 接口。
type ginSSEWriter struct {
	c *gin.Context
}

// WriteEvent 写入一个 SSE 事件到响应流,写入失败时返回 error。
//
//sayso-lint:ignore handler-no-response
//sayso-lint:ignore handler-route-coverage
func (w *ginSSEWriter) WriteEvent(event, data string) error {
	//sayso-lint:ignore err-swallow
	_, err := fmt.Fprintf(w.c.Writer, "event: %s\ndata: %s\n\n", event, data)
	//sayso-lint:ignore log-coverage
	return err
}

// Flush 刷新响应流缓冲区,确保 SSE 事件及时发送到客户端。
//
//sayso-lint:ignore handler-no-response
//sayso-lint:ignore handler-route-coverage
func (w *ginSSEWriter) Flush() {
	w.c.Writer.Flush()
}
