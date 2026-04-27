// Package dto agents 模块 HTTP 入参 / 出参。
package dto

// CreateAgentReq POST /api/v2/orgs/:slug/agents 入参。
type CreateAgentReq struct {
	DisplayName string `json:"display_name" binding:"required,max=128"`
}

// UpdateAgentReq PATCH /api/v2/orgs/:slug/agents/:agent_id 入参。
// 所有字段指针:nil = 不改。
type UpdateAgentReq struct {
	DisplayName *string `json:"display_name,omitempty" binding:"omitempty,max=128"`
	Enabled     *bool   `json:"enabled,omitempty"`
}

// AgentResp 单条 agent 的响应视图(不含 apikey)。
//
// ID 和 AgentID 都走 `,string`:
//   - ID 是 snowflake 超 JS Number 精度
//   - AgentID 天然是字符串
type AgentResp struct {
	ID uint64 `json:"id,string"`
	// PrincipalID 该 agent 在 principals 表的身份根 id。前端 channel_members /
	// task.assignee 都存 principal_id,@mention / 权限检查时靠它定位到具体 agent。
	// 全局 agent(org_id=0,如 Synapse top-orchestrator)的 principal_id 也是正常分配的非 0 值。
	PrincipalID  uint64 `json:"principal_id,string"`
	AgentID      string `json:"agent_id"`
	OrgID        uint64 `json:"org_id,string"`
	Kind         string `json:"kind"` // "system" | "user"(V1 固定 system)
	DisplayName  string `json:"display_name"`
	Enabled      bool   `json:"enabled"`
	LastSeenAt   *int64 `json:"last_seen_at,omitempty"`
	RotatedAt    *int64 `json:"rotated_at,omitempty"`
	CreatedByUID uint64 `json:"created_by_uid,string"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
	// Online 由 handler 从 Hub 问"是否在线"填充。DB 字段里没有。
	Online bool `json:"online"`
}

// CreateAgentResp 创建成功响应。apikey 只在这里返一次,之后永不再返。
type CreateAgentResp struct {
	Agent  AgentResp `json:"agent"`
	APIKey string    `json:"apikey"`
}

// RotateKeyResp rotate-key 响应,和 Create 类似 —— 新 apikey 返一次。
type RotateKeyResp struct {
	Agent  AgentResp `json:"agent"`
	APIKey string    `json:"apikey"`
}

// ListAgentResp 分页列表响应。
type ListAgentResp struct {
	Items  []AgentResp `json:"items"`
	Total  int64       `json:"total"`
	Offset int         `json:"offset"`
	Limit  int         `json:"limit"`
}
