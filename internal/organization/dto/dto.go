// dto.go 组织模块 HTTP 请求/响应 DTO 定义。
package dto

// ─── Org ──────────────────────────────────────────────────────────────────────

// CreateOrgRequest 创建 org 的请求。
type CreateOrgRequest struct {
	Slug        string `json:"slug" binding:"required"`
	DisplayName string `json:"display_name" binding:"required"`
	Description string `json:"description"`
}

// CheckSlugResponse slug 可用性预检响应。
type CheckSlugResponse struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// UpdateOrgRequest 更新 org 基础信息的请求。
type UpdateOrgRequest struct {
	DisplayName *string `json:"display_name,omitempty"`
	Description *string `json:"description,omitempty"`
}

// OrgResponse org 的展示 DTO。
type OrgResponse struct {
	ID          uint64 `json:"id,string"`
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Description string `json:"description,omitempty"`
	OwnerUserID uint64 `json:"owner_user_id,string"`
	Status      string `json:"status"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// OrgWithMembershipResponse 列出"我的 org"时每条记录的形状。
type OrgWithMembershipResponse struct {
	Org      OrgResponse `json:"org"`
	JoinedAt int64       `json:"joined_at"`
}

// ─── Member ───────────────────────────────────────────────────────────────────

// MemberResponse 成员展示 DTO。
// Email / DisplayName / AvatarURL / Status / EmailVerifiedAt / LastLoginAt 由 repo JOIN
// users 表回填;users 记录缺失(极端情况)时为空串 / 0 / nil,前端降级展示。Role 是该成员
// 在本 org 的角色摘要。
//
// EmailVerifiedAt / LastLoginAt 用指针 —— 未验证 / 从未登录时明确为 null,前端据此展示
// "未验证"徽章或省略"最近活跃"字段,而不是误展示 1970-01-01。
type MemberResponse struct {
	UserID          uint64      `json:"user_id,string"`
	Email           string      `json:"email,omitempty"`
	DisplayName     string      `json:"display_name,omitempty"`
	AvatarURL       string      `json:"avatar_url,omitempty"`
	// Status 对应 user.Status:0=待验证 / 1=active / 2=banned / 3=deleted。
	// 不加 omitempty —— 0 也是有意义的值。
	Status          int32       `json:"status"`
	EmailVerifiedAt *int64      `json:"email_verified_at,omitempty"`
	LastLoginAt     *int64      `json:"last_login_at,omitempty"`
	JoinedAt        int64       `json:"joined_at"`
	Role            RoleSummary `json:"role"`
}

// ListMembersResponse 分页列成员响应。
type ListMembersResponse struct {
	Items []MemberResponse `json:"items"`
	Total int64            `json:"total"`
	Page  int              `json:"page"`
	Size  int              `json:"size"`
}

// ─── Role ─────────────────────────────────────────────────────────────────────

// RoleSummary 成员响应内嵌的精简角色信息。Slug 可能为空串(JOIN 缺失极端情况)。
type RoleSummary struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	IsSystem    bool   `json:"is_system"`
}

// RoleResponse 角色的完整展示 DTO(列表 / 创建 / 修改接口返回)。
//
// M5:Permissions 字段非空数组,系统角色由 migration 默认填,custom role 由调用方传入或后续 PATCH。
type RoleResponse struct {
	Slug        string   `json:"slug"`
	DisplayName string   `json:"display_name"`
	IsSystem    bool     `json:"is_system"`
	Permissions []string `json:"permissions"`
	CreatedAt   int64    `json:"created_at"`
	UpdatedAt   int64    `json:"updated_at"`
}

// CreateRoleRequest 创建自定义角色请求。
//
// M5:permissions 可选;为 nil/缺省则空集(后续可 PATCH 加)。
// 提供时必须是 caller 自身 permissions 的子集(权限上限规则)。
type CreateRoleRequest struct {
	Slug        string   `json:"slug" binding:"required"`
	DisplayName string   `json:"display_name" binding:"required"`
	Permissions []string `json:"permissions,omitempty"`
}

// UpdateRoleRequest 修改自定义角色请求(slug 不可改)。
//
// M5:可改 display_name 和 permissions。两者都用 *T 区分"未传"和"传了空值"。
//   - DisplayName: nil = 不动;非 nil = 替换
//   - Permissions: nil = 不动;非 nil(包括空 slice [])= 替换为该集合
type UpdateRoleRequest struct {
	DisplayName *string   `json:"display_name,omitempty"`
	Permissions *[]string `json:"permissions,omitempty"`
}

// UpdateRolePermissionsRequest 改任意 role(含系统角色)的 permissions 字段的请求。
//
// 由独立 endpoint /roles/:slug/permissions 使用,需要 role.manage_system 权限(owner 专属)。
// 用单独 endpoint(而不是复用 UpdateRoleRequest 的 Permissions 字段)是为了让 perm 中间件
// 一目了然 —— role.manage_system 跟 role.manage 是不同 perm。
type UpdateRolePermissionsRequest struct {
	Permissions []string `json:"permissions" binding:"required"`
}

// AssignRoleRequest 修改成员角色请求。
type AssignRoleRequest struct {
	RoleSlug string `json:"role_slug" binding:"required"`
}

// ─── Invitation ───────────────────────────────────────────────────────────────

// CreateInvitationRequest 创建邀请请求。
type CreateInvitationRequest struct {
	Email    string `json:"email" binding:"required"`
	RoleSlug string `json:"role_slug" binding:"required"`
}

// InvitationResponse 邀请的完整展示 DTO。
//
// Token 不会出现在响应里 —— raw token 只通过邮件送达收件人,列表/详情 API 绝不返回。
type InvitationResponse struct {
	ID             uint64      `json:"id,string"`
	Email          string      `json:"email"`
	Role           RoleSummary `json:"role"`
	Status         string      `json:"status"`
	InviterUserID  uint64      `json:"inviter_user_id,string"`
	ExpiresAt      int64       `json:"expires_at"`
	AcceptedAt     int64       `json:"accepted_at,omitempty"`
	AcceptedUserID uint64      `json:"accepted_user_id,string,omitempty"`
	CreatedAt      int64       `json:"created_at"`
	UpdatedAt      int64       `json:"updated_at"`
}

// ListInvitationsResponse 分页列邀请响应。
type ListInvitationsResponse struct {
	Items []InvitationResponse `json:"items"`
	Total int64                `json:"total"`
	Page  int                  `json:"page"`
	Size  int                  `json:"size"`
}

// AcceptInvitationRequest 接受邀请请求。需要已登录态。
type AcceptInvitationRequest struct {
	Token string `json:"token" binding:"required"`
}

// AcceptInvitationResult 接受成功后的返回。前端据此跳到 org 详情页。
type AcceptInvitationResult struct {
	OrgID       uint64 `json:"org_id,string"`
	OrgSlug     string `json:"org_slug"`
	DisplayName string `json:"display_name"`
}

// InvitationPreviewResponse 未登录场景下的邀请摘要(前端落地页渲染)。
// 不包含 token / token_hash,前端自己持有 URL 里的 raw token。
type InvitationPreviewResponse struct {
	OrgSlug        string      `json:"org_slug"`
	OrgDisplayName string      `json:"org_display_name"`
	InviterName    string      `json:"inviter_name"`
	Email          string      `json:"email"`
	Role           RoleSummary `json:"role"`
	Status         string      `json:"status"`
	ExpiresAt      int64       `json:"expires_at"`
}

// InviteCandidateResponse 邀请候选人搜索返回的单条用户摘要。
//
// IsMember / HasPendingInvite 由后端直接打标,前端据此把不可点的条目灰掉。
// UserID 走 ,string 走 snowflake ID 精度安全。
type InviteCandidateResponse struct {
	UserID           uint64 `json:"user_id,string"`
	Email            string `json:"email"`
	DisplayName      string `json:"display_name"`
	AvatarURL        string `json:"avatar_url,omitempty"`
	IsMember         bool   `json:"is_member"`
	HasPendingInvite bool   `json:"has_pending_invite"`
}

// SearchCandidatesResponse 候选人搜索响应。
// 精确模式最多 1 条、模糊模式最多 10 条,不分页。
type SearchCandidatesResponse struct {
	Items []InviteCandidateResponse `json:"items"`
}

// MyInvitationResponse 被邀请人收件箱里单条邀请的展示 DTO。
//
// 和 InvitationPreviewResponse 相比多了 id(站内接受/拒绝要用),少了 email
// (email 永远是登录用户自己)。Status 带上以便前端区分 pending / 已处理。
// InviterName 由 service 层通过 UserLookup 查出来的 inviter 展示名,空串兜底用邮箱。
type MyInvitationResponse struct {
	ID             uint64      `json:"id,string"`
	OrgSlug        string      `json:"org_slug"`
	OrgDisplayName string      `json:"org_display_name"`
	InviterName    string      `json:"inviter_name"`
	Role           RoleSummary `json:"role"`
	Status         string      `json:"status"`
	ExpiresAt      int64       `json:"expires_at"`
	CreatedAt      int64       `json:"created_at"`
}

// ListMyInvitationsResponse 收件箱响应。
// status 可通过 query 过滤;懒过期只在包含 pending 的请求里做。
// 不分页 —— 单用户邀请量天然小。
type ListMyInvitationsResponse struct {
	Items []MyInvitationResponse `json:"items"`
}

// SentInvitationResponse 发件箱里单条邀请的展示 DTO(当前登录用户作为 inviter)。
//
// 和 MyInvitationResponse 相比显式带 Email(发给谁);不带 InviterName(永远是登录用户自己)。
type SentInvitationResponse struct {
	ID             uint64      `json:"id,string"`
	OrgSlug        string      `json:"org_slug"`
	OrgDisplayName string      `json:"org_display_name"`
	Email          string      `json:"email"`
	Role           RoleSummary `json:"role"`
	Status         string      `json:"status"`
	ExpiresAt      int64       `json:"expires_at"`
	CreatedAt      int64       `json:"created_at"`
}

// ListSentInvitationsResponse 发件箱响应。不分页;可按 status 过滤。
type ListSentInvitationsResponse struct {
	Items []SentInvitationResponse `json:"items"`
}
