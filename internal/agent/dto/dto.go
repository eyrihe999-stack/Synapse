// dto.go agent 模块 HTTP 请求/响应 DTO 定义。
//
// 约定:
//   - Request 后缀表示请求体,Response 后缀表示响应体
//   - 不直接暴露 gorm model
//   - 时间字段统一用 int64 时间戳(秒或毫秒),避免 time.Time 序列化歧义
package dto

// ─── Agent CRUD ───────────────────────────────────────────────────────────────

// CreateAgentRequest 注册一个新 agent。
// Methods 必须至少有 1 条。
type CreateAgentRequest struct {
	Slug         string                 `json:"slug" binding:"required"`
	DisplayName  string                 `json:"display_name" binding:"required"`
	Description  string                 `json:"description"`
	EndpointURL  string                 `json:"endpoint_url" binding:"required"`
	IconURL      string                 `json:"icon_url"`
	Tags         []string               `json:"tags"`
	HomepageURL  string                 `json:"homepage_url"`
	PriceTag     string                 `json:"price_tag"`
	Developer    string                 `json:"developer_contact"`
	Version      string                 `json:"version"`
	TimeoutSec   int                    `json:"timeout_seconds"`
	RatePerMin   int                    `json:"rate_limit_per_minute"`
	MaxConcur    int                    `json:"max_concurrent"`
	Methods      []CreateMethodRequest  `json:"methods" binding:"required"`
}

// UpdateAgentRequest 部分更新 agent 元信息。
type UpdateAgentRequest struct {
	DisplayName *string  `json:"display_name,omitempty"`
	Description *string  `json:"description,omitempty"`
	EndpointURL *string  `json:"endpoint_url,omitempty"`
	IconURL     *string  `json:"icon_url,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	HomepageURL *string  `json:"homepage_url,omitempty"`
	Developer   *string  `json:"developer_contact,omitempty"`
	Version     *string  `json:"version,omitempty"`
	TimeoutSec  *int     `json:"timeout_seconds,omitempty"`
	RatePerMin  *int     `json:"rate_limit_per_minute,omitempty"`
	MaxConcur   *int     `json:"max_concurrent,omitempty"`
}

// AgentResponse agent 的展示 DTO。
type AgentResponse struct {
	ID                  uint64   `json:"id,string"`
	OwnerUserID         uint64   `json:"owner_user_id,string"`
	Slug                string   `json:"slug"`
	DisplayName         string   `json:"display_name"`
	Description         string   `json:"description,omitempty"`
	Protocol            string   `json:"protocol"`
	EndpointURL         string   `json:"endpoint_url"`
	IconURL             string   `json:"icon_url,omitempty"`
	Tags                []string `json:"tags,omitempty"`
	HomepageURL         string   `json:"homepage_url,omitempty"`
	PriceTag            string   `json:"price_tag,omitempty"`
	Developer           string   `json:"developer_contact,omitempty"`
	Version             string   `json:"version,omitempty"`
	TimeoutSeconds      int      `json:"timeout_seconds"`
	RateLimitPerMinute  int      `json:"rate_limit_per_minute"`
	MaxConcurrent       int      `json:"max_concurrent"`
	Status              string   `json:"status"`
	HealthStatus        string   `json:"health_status"`
	HealthCheckedAt     int64    `json:"health_checked_at,omitempty"`
	CreatedAt           int64    `json:"created_at"`
	UpdatedAt           int64    `json:"updated_at"`
}

// CreateAgentResponse 创建 agent 响应,附带一次性明文 secret。
type CreateAgentResponse struct {
	Agent  AgentResponse `json:"agent"`
	Secret string        `json:"secret"`
	Notice string        `json:"notice"`
}

// RotateSecretResponse rotate secret 的响应。
type RotateSecretResponse struct {
	Secret string `json:"secret"`
	Notice string `json:"notice"`
}

// HealthResponse 健康状态查询响应。
type HealthResponse struct {
	Status          string `json:"status"`
	CheckedAt       int64  `json:"checked_at,omitempty"`
	FailCount       int    `json:"fail_count"`
}

// ─── Method ───────────────────────────────────────────────────────────────────

// CreateMethodRequest 创建 method 请求。
type CreateMethodRequest struct {
	MethodName  string `json:"method_name" binding:"required"`
	DisplayName string `json:"display_name" binding:"required"`
	Description string `json:"description"`
	Transport   string `json:"transport"`
	Visibility  string `json:"visibility"`
}

// UpdateMethodRequest 更新 method 请求。
type UpdateMethodRequest struct {
	DisplayName *string `json:"display_name,omitempty"`
	Description *string `json:"description,omitempty"`
	Transport   *string `json:"transport,omitempty"`
	Visibility  *string `json:"visibility,omitempty"`
}

// MethodResponse method 展示 DTO。
type MethodResponse struct {
	ID          uint64 `json:"id,string"`
	AgentID     uint64 `json:"agent_id,string"`
	MethodName  string `json:"method_name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description,omitempty"`
	Transport   string `json:"transport"`
	Visibility  string `json:"visibility"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// ─── Publish ──────────────────────────────────────────────────────────────────

// PublishAgentRequest 发布 agent 到 org 的请求。
type PublishAgentRequest struct {
	AgentID uint64 `json:"agent_id,string" binding:"required"`
	Note    string `json:"note"`
}

// ReviewPublishRequest 审核发布请求(approve/reject 共用)。
type ReviewPublishRequest struct {
	Note string `json:"note"`
}

// PublishResponse publish 展示 DTO。
type PublishResponse struct {
	ID                uint64 `json:"id,string"`
	AgentID           uint64 `json:"agent_id,string"`
	OrgID             uint64 `json:"org_id,string"`
	SubmittedByUserID uint64 `json:"submitted_by_user_id,string"`
	Status            string `json:"status"`
	ReviewedByUserID  uint64 `json:"reviewed_by_user_id,omitempty,string"`
	ReviewedAt        int64  `json:"reviewed_at,omitempty"`
	ReviewNote        string `json:"review_note,omitempty"`
	RevokedAt         int64  `json:"revoked_at,omitempty"`
	RevokedReason     string `json:"revoked_reason,omitempty"`
	CreatedAt         int64  `json:"created_at"`
	UpdatedAt         int64  `json:"updated_at"`
}

// ─── Invoke / Audit ───────────────────────────────────────────────────────────

// InvocationResponse invocation 审计展示 DTO。
type InvocationResponse struct {
	InvocationID     string `json:"invocation_id"`
	TraceID          string `json:"trace_id,omitempty"`
	OrgID            uint64 `json:"org_id,string"`
	CallerUserID     uint64 `json:"caller_user_id,string"`
	CallerRoleName   string `json:"caller_role_name,omitempty"`
	AgentID          uint64 `json:"agent_id,string"`
	AgentOwnerUserID uint64 `json:"agent_owner_user_id,string"`
	MethodName       string `json:"method_name"`
	Transport        string `json:"transport"`
	StartedAt        int64  `json:"started_at"`
	FinishedAt       int64  `json:"finished_at,omitempty"`
	LatencyMs        int    `json:"latency_ms,omitempty"`
	Status           string `json:"status"`
	ErrorCode        string `json:"error_code,omitempty"`
	ErrorMessage     string `json:"error_message,omitempty"`
	RequestSizeBytes int    `json:"request_size_bytes,omitempty"`
	ResponseSizeBytes int   `json:"response_size_bytes,omitempty"`
	ClientIP         string `json:"client_ip,omitempty"`
}

// InvocationDetailResponse 含 payload 的 invocation 详情。
type InvocationDetailResponse struct {
	Invocation   InvocationResponse `json:"invocation"`
	RequestBody  string             `json:"request_body,omitempty"`
	ResponseBody string             `json:"response_body,omitempty"`
}

// ─── Pagination wrapper ──────────────────────────────────────────────────────

// PageResponse 分页列表通用响应包装。
type PageResponse struct {
	Items any `json:"items"`
	Total int64       `json:"total"`
	Page  int         `json:"page"`
	Size  int         `json:"size"`
}
