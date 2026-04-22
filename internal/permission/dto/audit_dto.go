// audit_dto.go 审计查询相关 DTO(M6)。
package dto

import "encoding/json"

// AuditLogRow 单条审计日志的展示 DTO。
//
// Before/After/Metadata 透传 jsonb 原始字节,前端按 action 约定解析。
// (相比"展开成典型结构",前端更灵活;后端不必为每个 action 单独建 view 类型。)
type AuditLogRow struct {
	ID          uint64          `json:"id,string"`
	OrgID       uint64          `json:"org_id,string"`
	ActorUserID uint64          `json:"actor_user_id,string"`
	Action      string          `json:"action"`
	TargetType  string          `json:"target_type"`
	TargetID    uint64          `json:"target_id,string"`
	Before      json.RawMessage `json:"before,omitempty"`
	After       json.RawMessage `json:"after,omitempty"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
	CreatedAt   int64           `json:"created_at"`
}

// ListAuditLogResponse 列接口响应。keyset 分页:next_before_id 非零时前端带回查下页。
type ListAuditLogResponse struct {
	Items        []AuditLogRow `json:"items"`
	NextBeforeID uint64        `json:"next_before_id,string,omitempty"`
	Scope        string        `json:"scope"` // "all" 或 "self":前端据此展示提示
}
