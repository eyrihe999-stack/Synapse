// dto.go 组织模块 HTTP 请求/响应 DTO 定义。
//
// 约定:
//   - Request 后缀表示请求体
//   - Response 后缀表示响应体
//   - 不直接暴露 gorm model,service 层负责转换
//   - 时间字段统一用 int64 时间戳(秒)或 RFC3339 字符串,避免 time.Time 的序列化歧义
package dto

// ─── Org ──────────────────────────────────────────────────────────────────────

// CreateOrgRequest 创建 org 的请求。
type CreateOrgRequest struct {
	Slug        string `json:"slug" binding:"required"`
	DisplayName string `json:"display_name" binding:"required"`
	Description string `json:"description"`
}

// UpdateOrgRequest 更新 org 基础信息(display_name / description)的请求。
// 使用指针以区分"不更新"和"清空"。
type UpdateOrgRequest struct {
	DisplayName *string `json:"display_name,omitempty"`
	Description *string `json:"description,omitempty"`
}

// UpdateOrgSettingsRequest 更新 org 设置的请求。
type UpdateOrgSettingsRequest struct {
	RequireAgentReview *bool `json:"require_agent_review,omitempty"`
	RecordFullPayload  *bool `json:"record_full_payload,omitempty"`
}

// TransferOrgOwnershipRequest 发起所有权转让。
// 被转让人必须已经是 org 的成员。
type TransferOrgOwnershipRequest struct {
	TargetUserID uint64 `json:"target_user_id,string" binding:"required"`
}

// OrgResponse org 的展示 DTO。
type OrgResponse struct {
	ID                 uint64 `json:"id,string"`
	Slug               string `json:"slug"`
	DisplayName        string `json:"display_name"`
	Description        string `json:"description,omitempty"`
	OwnerUserID        uint64 `json:"owner_user_id,string"`
	Status             string `json:"status"`
	RequireAgentReview bool   `json:"require_agent_review"`
	RecordFullPayload  bool   `json:"record_full_payload"`
	CreatedAt          int64  `json:"created_at"`
	UpdatedAt          int64  `json:"updated_at"`
}

// OrgWithMyRoleResponse 列出"我的 org"时每条记录的形状,附带当前用户在该 org 内的角色。
type OrgWithMyRoleResponse struct {
	Org     OrgResponse `json:"org"`
	MyRole  RoleSummary `json:"my_role"`
	JoinedAt int64      `json:"joined_at"`
}

// ─── Role ─────────────────────────────────────────────────────────────────────

// RoleSummary 在列表场景下返回的简短角色信息。
type RoleSummary struct {
	ID          uint64   `json:"id,string"`
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	IsPreset    bool     `json:"is_preset"`
	Permissions []string `json:"permissions"`
}

// RoleResponse 角色详情。
type RoleResponse struct {
	ID          uint64   `json:"id,string"`
	OrgID       uint64   `json:"org_id,string"`
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	IsPreset    bool     `json:"is_preset"`
	Permissions []string `json:"permissions"`
	CreatedAt   int64    `json:"created_at"`
	UpdatedAt   int64    `json:"updated_at"`
}

// CreateCustomRoleRequest 创建自定义角色。
type CreateCustomRoleRequest struct {
	Name        string   `json:"name" binding:"required"`
	DisplayName string   `json:"display_name" binding:"required"`
	Permissions []string `json:"permissions" binding:"required"`
}

// UpdateCustomRoleRequest 更新自定义角色。指针语义同 UpdateOrgRequest。
type UpdateCustomRoleRequest struct {
	DisplayName *string   `json:"display_name,omitempty"`
	Permissions *[]string `json:"permissions,omitempty"`
}

// PermissionsResponse 权限点清单响应(系统常量,供前端构建选择面板)。
type PermissionsResponse struct {
	All               []string `json:"all"`
	OwnerOnly         []string `json:"owner_only"`
}

// ─── Member ───────────────────────────────────────────────────────────────────

// MemberResponse 成员展示 DTO,包含角色和用户基础信息。
type MemberResponse struct {
	UserID      uint64      `json:"user_id,string"`
	DisplayName string      `json:"display_name,omitempty"`
	AvatarURL   string      `json:"avatar_url,omitempty"`
	Role        RoleSummary `json:"role"`
	JoinedAt    int64       `json:"joined_at"`
}

// ListMembersResponse 分页列成员响应。
type ListMembersResponse struct {
	Items []MemberResponse `json:"items"`
	Total int64            `json:"total"`
	Page  int              `json:"page"`
	Size  int              `json:"size"`
}

// AssignMemberRoleRequest 变更成员角色。
type AssignMemberRoleRequest struct {
	RoleID uint64 `json:"role_id,string" binding:"required"`
}

// ─── Invitation ───────────────────────────────────────────────────────────────

// InviteQueryType 候选人查找方式的枚举。
type InviteQueryType string

const (
	// InviteQueryByUserID 按 user_id 精确查找
	InviteQueryByUserID InviteQueryType = "user_id"
	// InviteQueryByNickname 按昵称查找(返回候选列表,昵称不唯一)
	InviteQueryByNickname InviteQueryType = "nickname"
	// InviteQueryByEmail 按邮箱查找
	InviteQueryByEmail InviteQueryType = "email"
)

// SearchInviteesRequest 候选人查询请求。
type SearchInviteesRequest struct {
	QueryType   string `json:"query_type" binding:"required"`
	UserID      uint64 `json:"user_id,omitempty,string"`
	Nickname    string `json:"nickname,omitempty"`
	Email       string `json:"email,omitempty"`
}

// InviteeCandidate 候选用户信息(脱敏)。
type InviteeCandidate struct {
	UserID      uint64 `json:"user_id,string"`
	DisplayName string `json:"display_name,omitempty"`
	AvatarURL   string `json:"avatar_url,omitempty"`
	MaskedEmail string `json:"masked_email,omitempty"`
}

// SearchInviteesResponse 候选人查询响应。
type SearchInviteesResponse struct {
	Candidates []InviteeCandidate `json:"candidates"`
}

// CreateInvitationRequest 创建邀请请求。
// 必须指定 InviteeUserID(前端先走 search-invitees 拿到 user_id,再调此接口)。
type CreateInvitationRequest struct {
	InviteeUserID uint64 `json:"invitee_user_id,string" binding:"required"`
	RoleID        uint64 `json:"role_id,string" binding:"required"`
}

// InvitationResponse 邀请展示 DTO。
type InvitationResponse struct {
	ID             uint64       `json:"id,string"`
	OrgID          uint64       `json:"org_id,string"`
	OrgSlug        string       `json:"org_slug,omitempty"`
	OrgDisplayName string       `json:"org_display_name,omitempty"`
	InviterUserID  uint64       `json:"inviter_user_id,string"`
	InviteeUserID  uint64       `json:"invitee_user_id,string"`
	Role           *RoleSummary `json:"role,omitempty"`
	Type           string       `json:"type"`
	Status         string       `json:"status"`
	ExpiresAt      int64        `json:"expires_at"`
	CreatedAt      int64        `json:"created_at"`
}

// ListInvitationsResponse 列表响应。
type ListInvitationsResponse struct {
	Items []InvitationResponse `json:"items"`
	Total int64                `json:"total"`
	Page  int                  `json:"page"`
	Size  int                  `json:"size"`
}
