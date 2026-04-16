// chat_handler.go 对话网关 + SSE 流式 handler。
package handler

import (
	"fmt"
	"net/http"

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

	writer := &ginSSEWriter{c: c}
	//sayso-lint:ignore err-swallow
	_, err := h.chatSvc.ChatStream(c.Request.Context(), req, writer)
	if err != nil {
		// 如果还没开始写 SSE,可以返回 JSON 错误
		// 如果已经写了 SSE header,通过 error event 通知客户端
		//sayso-lint:ignore err-swallow
		writer.WriteEvent("error", fmt.Sprintf(`{"message":"%s"}`, err.Error()))
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
