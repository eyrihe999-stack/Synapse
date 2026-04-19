// filter.go AgentFilter 类型 + JSON Schema(给 MCP tool input_schema 用)。
package catalog

import "encoding/json"

// AgentFilter search_agent 工具的过滤条件。
//
// 只 approved + 未 revoked 的 agent 会参与查询,这一层不暴露给调用方(catalog 在 data source 层自动施加)。
type AgentFilter struct {
	Tags      []string `json:"tags,omitempty"`       // AND 语义:全命中才保留
	AgentType string   `json:"agent_type,omitempty"` // "chat" | "tool" | "mcp",空 = 不过滤
	OwnerUIDs []uint64 `json:"owner_uids,omitempty"` // 限定作者,空 = 不过滤
}

func agentFilterSchema() json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"tags": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Agent must have ALL these tags (AND). Use to narrow by capability, e.g. [\"code\",\"review\"].",
			},
			"agent_type": map[string]any{
				"type":        "string",
				"enum":        []string{"chat", "tool", "mcp"},
				"description": "Filter by agent type. \"mcp\" agents speak MCP protocol — you can invoke their tools as if your own.",
			},
			"owner_uids": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "integer"},
				"description": "Restrict to agents owned by these user IDs.",
			},
		},
	})
	return b
}
