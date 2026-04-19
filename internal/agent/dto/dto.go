// dto.go agent 模块请求/响应类型定义。
package dto

// ─── Agent CRUD ─────────────────────────────────────────────────────────────

// CreateAgentRequest 创建 agent 请求。
type CreateAgentRequest struct {
	Slug             string   `json:"slug" binding:"required"`
	DisplayName      string   `json:"display_name" binding:"required"`
	Description      string   `json:"description"`
	AgentType        string   `json:"agent_type"`
	Version          string   `json:"version"`
	EndpointURL      string   `json:"endpoint_url" binding:"required"`
	ContextMode      string   `json:"context_mode"`
	MaxContextRounds int      `json:"max_context_rounds"`
	AuthToken        string   `json:"auth_token"`
	TimeoutSeconds   int      `json:"timeout_seconds"`
	IconURL          string   `json:"icon_url"`
	Tags             []string `json:"tags"`
	// DataSources 仅 agent_type=knowledge 有效;每项是数据源类型(如 "documents")。
	DataSources []string `json:"data_sources,omitempty"`
}

// UpdateAgentRequest 部分更新 agent 请求。
type UpdateAgentRequest struct {
	DisplayName      *string  `json:"display_name,omitempty"`
	Description      *string  `json:"description,omitempty"`
	Version          *string  `json:"version,omitempty"`
	AgentType        *string  `json:"agent_type,omitempty"`
	EndpointURL      *string  `json:"endpoint_url,omitempty"`
	ContextMode      *string  `json:"context_mode,omitempty"`
	MaxContextRounds *int     `json:"max_context_rounds,omitempty"`
	AuthToken        *string  `json:"auth_token,omitempty"`
	TimeoutSeconds   *int     `json:"timeout_seconds,omitempty"`
	IconURL          *string  `json:"icon_url,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	// DataSources 支持整列表替换。传 nil/不传 = 不改;传空数组 = 清空。
	// 仅 knowledge 类型接受非空值;其它类型传非空会被校验拒绝。
	DataSources *[]string `json:"data_sources,omitempty"`
}

// AgentResponse agent 响应。
type AgentResponse struct {
	ID               uint64   `json:"id,string"`
	OwnerUserID      uint64   `json:"owner_user_id,string"`
	Slug             string   `json:"slug"`
	DisplayName      string   `json:"display_name"`
	Description      string   `json:"description,omitempty"`
	AgentType        string   `json:"agent_type"`
	EndpointURL      string   `json:"endpoint_url"`
	ContextMode      string   `json:"context_mode"`
	MaxContextRounds int      `json:"max_context_rounds"`
	HasAuthToken     bool     `json:"has_auth_token"`
	TimeoutSeconds   int      `json:"timeout_seconds"`
	IconURL          string   `json:"icon_url,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	DataSources      []string `json:"data_sources,omitempty"`
	Version          string   `json:"version"`
	Status           string   `json:"status"`
	CreatedAt        int64    `json:"created_at"`
	UpdatedAt        int64    `json:"updated_at"`
}

// ─── Chat ───────────────────────────────────────────────────────────────────

// ChatRequest 对话请求。
type ChatRequest struct {
	Message   string `json:"message" binding:"required,max=32000"`
	SessionID string `json:"session_id"`
	Stream    bool   `json:"stream"`
}

// ChatResponse 非流式对话响应。
type ChatResponse struct {
	SessionID string `json:"session_id"`
	Message   ChatMessage `json:"message"`
}

// ChatMessage 对话消息。
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ─── Session ────────────────────────────────────────────────────────────────

// SessionResponse session 响应。
type SessionResponse struct {
	SessionID   string `json:"session_id"`
	AgentID     uint64 `json:"agent_id,string"`
	Title       string `json:"title,omitempty"`
	ContextMode string `json:"context_mode"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// MessageResponse 消息响应。
type MessageResponse struct {
	ID        uint64 `json:"id,string"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt int64  `json:"created_at"`
}

// ─── Publish ────────────────────────────────────────────────────────────────

// PublishAgentRequest 提交发布请求。
type PublishAgentRequest struct {
	AgentID uint64 `json:"agent_id,string" binding:"required"`
	Note    string `json:"note"`
}

// ReviewPublishRequest 审核请求。
type ReviewPublishRequest struct {
	Note string `json:"note"`
}

// PublishResponse publish 响应。
type PublishResponse struct {
	ID                uint64 `json:"id,string"`
	AgentID           uint64 `json:"agent_id,string"`
	OrgID             uint64 `json:"org_id,string"`
	SubmittedByUserID uint64 `json:"submitted_by_user_id,string"`
	Status            string `json:"status"`
	ReviewedByUserID  uint64 `json:"reviewed_by_user_id,string,omitempty"`
	ReviewedAt        int64  `json:"reviewed_at,omitempty"`
	ReviewNote        string `json:"review_note,omitempty"`
	RevokedAt         int64  `json:"revoked_at,omitempty"`
	RevokedReason     string `json:"revoked_reason,omitempty"`
	CreatedAt         int64  `json:"created_at"`
	UpdatedAt         int64  `json:"updated_at"`

	// 用户冗余字段，方便前端展示
	SubmittedByDisplayName string `json:"submitted_by_display_name,omitempty"`
	ReviewedByDisplayName  string `json:"reviewed_by_display_name,omitempty"`

	// Agent 冗余字段，方便前端展示与跳转
	AgentSlug        string   `json:"agent_slug,omitempty"`
	AgentDisplayName string   `json:"agent_display_name,omitempty"`
	AgentOwnerUID    uint64   `json:"agent_owner_uid,string,omitempty"`
	AgentType        string   `json:"agent_type,omitempty"`
	AgentDescription string   `json:"agent_description,omitempty"`
	AgentIconURL     string   `json:"agent_icon_url,omitempty"`
	AgentContextMode string   `json:"agent_context_mode,omitempty"`
	AgentTags        []string `json:"agent_tags,omitempty"`
	AgentVersion     string   `json:"agent_version,omitempty"`
	AgentUpdatedAt   int64    `json:"agent_updated_at,omitempty"`
}

// ─── 分页 ───────────────────────────────────────────────────────────────────

// PageResponse 分页响应包装。
type PageResponse struct {
	Items any   `json:"items"`
	Total int64 `json:"total"`
	Page  int   `json:"page"`
	Size  int   `json:"size"`
}
