// Package invoke 提供 invoke_agent meta-tool。
//
// 接入方式:作为 mcpserver.ExtraToolHandler 注册到 retrieval MCP 端点,不改 retrieval 协议。
// 调用方(Claude.ai 等)的工作流:
//   1. search_agent(...)   → 拿 agent_id + tool name
//   2. invoke_agent(agent_id, tool_name, arguments) → 结果
// 等价于自己开 MCP connector 到 /api/v2/agents/X/Y/mcp 再调,但一条 connector 就够。
//
// 代价:args 以自由 JSON 传入,LLM 看不到目标 tool 的 schema,
// 调用准确率略低于原生 tool use。代价换来"一个 connector 玩所有 agent"的体验。
package invoke

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	agentsvc "github.com/eyrihe999-stack/Synapse/internal/agent/service"
	"github.com/eyrihe999-stack/Synapse/internal/orchestrator/catalog"
	"github.com/eyrihe999-stack/Synapse/internal/retrieval/mcpserver"
)

// Handler 实现 mcpserver.ExtraToolHandler。
type Handler struct {
	proxy agentsvc.MCPProxyService
}

// New 构造。proxy 非 nil。
func New(proxy agentsvc.MCPProxyService) *Handler {
	if proxy == nil {
		panic("invoke: proxy must be non-nil")
	}
	return &Handler{proxy: proxy}
}

func (h *Handler) Name() string { return "invoke_agent" }

func (h *Handler) Description() string {
	return "Invoke a specific tool on an MCP agent discovered via search_agent. " +
		"Use this to delegate work to peer agents (e.g. a code-review agent, a PRD-draft agent). " +
		"Inputs: agent_id (from search_agent hits), tool_name (from the agent's tools list), arguments (JSON object). " +
		"Returns the agent's response content directly. If the agent errors, isError will be true."
}

func (h *Handler) InputSchema() json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"agent_id", "tool_name"},
		"properties": map[string]any{
			"agent_id": map[string]any{
				"type":        "string",
				"description": "Hit ID from search_agent (e.g. \"agent:42\"). Bare numeric string \"42\" also accepted.",
			},
			"tool_name": map[string]any{
				"type":        "string",
				"description": "Name of the tool to invoke on the target agent. Must exist in the agent's tools list from search_agent.",
			},
			"arguments": map[string]any{
				"type":        "object",
				"description": "JSON object passed as the target tool's arguments. Shape depends on the agent tool;看 search_agent.tools[].inputSchema.",
			},
		},
	})
	return b
}

// invokeArgs JSON 解析用。arguments 用 json.RawMessage 避免二次反序列化,透传给 agent。
type invokeArgs struct {
	AgentID   string          `json:"agent_id"`
	ToolName  string          `json:"tool_name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (h *Handler) Invoke(ctx context.Context, orgID uint64, rawArgs json.RawMessage) ([]mcpserver.ContentBlock, bool, error) {
	var args invokeArgs
	if len(rawArgs) > 0 {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return errBlock("parse arguments: " + err.Error()), true, nil
		}
	}
	if args.AgentID == "" || args.ToolName == "" {
		return errBlock("agent_id and tool_name required"), true, nil
	}

	agentID, err := parseAgentID(args.AgentID)
	if err != nil {
		return errBlock(err.Error()), true, nil
	}

	// 组 MCP tools/call body。arguments 为空时填 {} —— 有些 agent 严格解析会拒 null。
	argsField := args.Arguments
	if len(argsField) == 0 {
		argsField = json.RawMessage(`{}`)
	}
	rpcBody, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      "invoke_agent",
		"method":  "tools/call",
		"params": map[string]any{
			"name":      args.ToolName,
			"arguments": argsField,
		},
	})
	if err != nil {
		return errBlock("build rpc: " + err.Error()), true, nil
	}

	respBody, err := h.proxy.InvokeByID(ctx, orgID, agentID, rpcBody)
	if err != nil {
		// 传输层错误(agent 不在线 / 未发布 / 超时等)翻译成 isError=true,让 LLM 能决定 retry 或换 agent
		return errBlock("proxy: " + err.Error()), true, nil
	}

	// 解 agent 的 JSON-RPC 响应。几种形态:
	//   {result: {content: [...], isError: bool}} — MCP tools/call 标准响应
	//   {error: {code, message, data}}           — JSON-RPC error,translate 成 errBlock
	var envelope struct {
		Result *struct {
			Content []mcpserver.ContentBlock `json:"content"`
			IsError bool                     `json:"isError"`
		} `json:"result,omitempty"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		// agent 返的不是合法 JSON-RPC,包成 text 丢给 LLM 看
		return []mcpserver.ContentBlock{{Type: "text", Text: "agent returned non-JSON-RPC: " + truncate(string(respBody), 500)}}, true, nil
	}
	if envelope.Error != nil {
		return errBlock(fmt.Sprintf("agent error %d: %s", envelope.Error.Code, envelope.Error.Message)), true, nil
	}
	if envelope.Result == nil {
		return errBlock("agent response missing result"), true, nil
	}
	return envelope.Result.Content, envelope.Result.IsError, nil
}

// parseAgentID 接受 "agent:42" 和 "42" 两种。catalog 的 FormatID 出来的是前者,但 LLM 偶尔会漏前缀。
func parseAgentID(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("agent_id empty")
	}
	// 先按 catalog 协议解
	if id, err := catalog.ParseID(s); err == nil {
		return id, nil
	}
	// 兜底:纯数字
	return strconv.ParseUint(s, 10, 64)
}

func errBlock(msg string) []mcpserver.ContentBlock {
	return []mcpserver.ContentBlock{{Type: "text", Text: msg}}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
