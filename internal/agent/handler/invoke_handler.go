// invoke_handler.go invoke 网关 handler + 取消接口。
package handler

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/eyrihe999-stack/Synapse/internal/agent"
	"github.com/eyrihe999-stack/Synapse/internal/agent/service"
	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
	"github.com/gin-gonic/gin"
)

// InvokeAgent POST /api/v2/orgs/:slug/agents/:owner_uid/:agent_slug/invoke [agent.invoke]
//
// 根据 method.transport 分发到 http 同步 / sse 流式路径。
// 为了确定 transport,需要先解析 body 抽 method,查 agent + method,再决定路径。
// 这里把决策交给 GatewayService:InvokeHTTP 先试一次,transport 不匹配时 fallback
// 到 InvokeSSE(因为调用方不知道 transport,取决于服务端 method 注册)。
func (h *AgentHandler) InvokeAgent(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
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
	membership, _ := GetMembership(c)
	if membership == nil {
		response.InternalServerError(c, "Missing membership", "")
		return
	}
	ownerUID, ok := parseUintParam(c, "owner_uid")
	if !ok {
		return
	}
	agentSlug := c.Param("agent_slug")
	if agentSlug == "" {
		response.BadRequest(c, "Missing agent slug", "")
		return
	}
	// 读取 body(上限控制由 gin / middleware 做)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		response.BadRequest(c, "Read body failed", err.Error())
		return
	}
	req := service.InvokeRequest{
		OrgID:        org.ID,
		OrgSlug:      org.Slug,
		CallerUserID: userID,
		CallerRole:   membership.RoleName,
		OwnerUID:     ownerUID,
		AgentSlug:    agentSlug,
		Body:         body,
		ClientIP:     c.ClientIP(),
		UserAgent:    c.Request.UserAgent(),
		TraceID:      c.GetHeader("X-Trace-ID"),
	}

	// 预判 transport:先试 HTTP,若返回 transport mismatch 则转 SSE
	result, err := h.gatewaySvc.InvokeHTTP(c.Request.Context(), req)
	if err == nil {
		c.Data(result.StatusCode, "application/json", result.RespBody)
		return
	}
	// 若是 transport 不匹配,切到 sse 路径
	if isSSETransport(err) {
		// 重置 body 重新传(InvokeRequest 里已拷贝,不影响)
		req.Body = body
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")
		c.Writer.WriteHeader(http.StatusOK)
		writer := &ginSSEWriter{w: c.Writer}
		//sayso-lint:ignore err-swallow
		if _, sseErr := h.gatewaySvc.InvokeSSE(c.Request.Context(), req, writer); sseErr != nil { // invocation_id 返回值此路径不需要
			h.logger.WarnCtx(c.Request.Context(), "sse invoke failed", map[string]any{"error": sseErr.Error()})
		}
		//sayso-lint:ignore handler-no-response
		return // SSE 已通过 c.Writer/writer 直接写入流式响应
	}
	h.handleServiceError(c, err)
}

// isSSETransport 判断错误是否表示需要走 SSE 路径。
func isSSETransport(err error) bool {
	// 简化:含有 transport mismatch 关键字即切换
	if err == nil {
		return false
	}
	return containsAny(err.Error(), []string{"transport mismatch"})
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if len(s) >= len(sub) && indexOf(s, sub) >= 0 {
			return true
		}
	}
	return false
}

// indexOf 简单子串查找(避免 import strings 的冗余)
func indexOf(s, sub string) int {
	if sub == "" {
		return 0
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// ginSSEWriter 把 gin.ResponseWriter 包装成 service.SSEWriter。
type ginSSEWriter struct {
	w gin.ResponseWriter
}

// WriteEvent 写一条 SSE 事件。
//
// 错误:底层 ResponseWriter 写入失败时透传原始 IO 错误。
//sayso-lint:ignore handler-route-coverage
func (g *ginSSEWriter) WriteEvent(eventType string, data []byte) error {
	buf := bytes.NewBuffer(nil)
	fmt.Fprintf(buf, "event: %s\ndata: ", eventType)
	buf.Write(data)
	buf.WriteString("\n\n")
	//sayso-lint:ignore err-swallow
	_, err := g.w.Write(buf.Bytes()) // 字节数无需关心
	//sayso-lint:ignore log-coverage
	return err
}

// Flush 把缓冲写入底层。
//sayso-lint:ignore handler-route-coverage
func (g *ginSSEWriter) Flush() {
	g.w.Flush()
}

// CancelInvocation DELETE /api/v2/invocations/:invocation_id
//
// 调用方校验由 service 层做(必须是发起人或有 audit.read 权限)。
// 这里简单做:查 invocation,若调用者是 caller_user_id 即允许。
func (h *AgentHandler) CancelInvocation(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	invID := c.Param("invocation_id")
	if invID == "" {
		response.BadRequest(c, "Missing invocation id", "")
		return
	}
	//sayso-lint:ignore err-swallow
	inv, _, err := h.auditSvc.GetByInvocationID(c.Request.Context(), invID, false) // payload 此路径不需要
	if err != nil {
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeInvocationNotFound, Message: "Invocation not found"})
		return
	}
	if inv.CallerUserID != userID {
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentPermissionDenied, Message: "Not the invocation caller"})
		return
	}
	if err := h.cancels.RequestCancel(c.Request.Context(), invID); err != nil {
		h.logger.ErrorCtx(c.Request.Context(), "request cancel failed", err, map[string]any{"invocation_id": invID})
		response.InternalServerError(c, "Internal server error", "")
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok"})
}

// 避免 unused import:强引用 strconv(其它地方可能已用到)
var _ = strconv.Itoa
