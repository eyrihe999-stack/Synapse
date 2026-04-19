// mcp_handler.go agent MCP 代理 HTTP 入口。
//
// 路由:POST /api/v2/agents/:owner_uid/:agent_slug/mcp
// 鉴权:OAuth access token(middleware 把 claims 注入 gin context)
//
// 这是一个"透传代理":handler 只做 body 读 / 写 + OAuth claims 解析;
// 业务校验(agent 类型 / publish 状态 / 超时)都在 service 层。
package handler

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/agent"
	"github.com/eyrihe999-stack/Synapse/internal/agent/service"
	oauthmw "github.com/eyrihe999-stack/Synapse/internal/oauth/middleware"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
)

// maxMCPRequestBodyBytes 单次 MCP 请求 body 上限。MCP 工具调用可能带较长参数(代码片段 / 长 prompt),
// 1 MB 对大多数 tool call 够用;真超了先 413,别拖慢整个 request chain。
const maxMCPRequestBodyBytes = 1 << 20 // 1 MB

// MCPHandler agent MCP 代理入口。
type MCPHandler struct {
	svc service.MCPProxyService
	log logger.LoggerInterface
}

// NewMCPHandler 构造。
func NewMCPHandler(svc service.MCPProxyService, log logger.LoggerInterface) *MCPHandler {
	if svc == nil || log == nil {
		panic("agent mcp handler: svc and log required")
	}
	return &MCPHandler{svc: svc, log: log}
}

// Invoke 处理一次 MCP JSON-RPC 调用转发。
func (h *MCPHandler) Invoke(c *gin.Context) {
	// 1) 解析 claims(orgID 从 token 来,不信任 URL 任何 orgID/slug hint)
	claims, ok := oauthmw.ClaimsFromContext(c)
	if !ok || claims == nil || claims.OrgID == 0 {
		c.JSON(http.StatusUnauthorized, jsonRPCError(nil, -32000, "missing oauth claims"))
		return
	}

	// 2) 解析 path 参数
	ownerUID, err := strconv.ParseUint(c.Param("owner_uid"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, jsonRPCError(nil, -32602, "invalid owner_uid"))
		return
	}
	agentSlug := c.Param("agent_slug")
	if agentSlug == "" {
		c.JSON(http.StatusBadRequest, jsonRPCError(nil, -32602, "missing agent_slug"))
		return
	}

	// 3) 读 body(带大小限制,LimitReader 保险起见再套一层)
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxMCPRequestBodyBytes))
	if err != nil {
		c.JSON(http.StatusRequestEntityTooLarge, jsonRPCError(nil, -32600, "request body too large or read error"))
		return
	}

	// 4) 转发
	resp, err := h.svc.Invoke(c.Request.Context(), claims.OrgID, ownerUID, agentSlug, body)
	if err != nil {
		h.writeErr(c, err)
		return
	}

	// 5) 原样返 agent 响应(service 已确认是 JSON Content-Type)
	c.Data(http.StatusOK, "application/json", resp)
}

// writeErr 把 service 层 sentinel 错误映射到 JSON-RPC / HTTP 响应。
// - agent 不存在 / 未发布 / 类型不对 → HTTP 404,JSON-RPC error
// - 超时 / 不可达          → HTTP 502,JSON-RPC error(让 LLM 知道是上游问题,可重试)
// - 参数错误              → HTTP 400
// - 内部错误              → HTTP 500
func (h *MCPHandler) writeErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, agent.ErrAgentNotFound):
		c.JSON(http.StatusNotFound, jsonRPCError(nil, -32601, err.Error()))
	case errors.Is(err, agent.ErrAgentInvalidRequest):
		c.JSON(http.StatusBadRequest, jsonRPCError(nil, -32602, err.Error()))
	case errors.Is(err, agent.ErrChatUpstreamTimeout):
		c.JSON(http.StatusBadGateway, jsonRPCError(nil, -32001, "agent timeout"))
	case errors.Is(err, agent.ErrChatUpstreamUnreachable):
		c.JSON(http.StatusBadGateway, jsonRPCError(nil, -32002, "agent unreachable"))
	default:
		h.log.ErrorCtx(c.Request.Context(), "mcp proxy: unexpected error", err, nil)
		c.JSON(http.StatusInternalServerError, jsonRPCError(nil, -32603, "internal error"))
	}
}

// jsonRPCError 最简单的 JSON-RPC 2.0 error 响应。id 为 nil 时客户端可能识别不了,但协议本身允许。
// MCP 客户端遇到 HTTP 非 2xx 通常会按传输层错误处理,不是走 JSON-RPC error 分支;这个 body 是
// 给主动读 body 的客户端看的。
func jsonRPCError(id any, code int, msg string) gin.H {
	return gin.H{
		"jsonrpc": "2.0",
		"id":      id,
		"error": gin.H{
			"code":    code,
			"message": msg,
		},
	}
}
