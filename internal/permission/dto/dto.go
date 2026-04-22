// dto.go 权限模块 HTTP 请求/响应 DTO 定义。
package dto

// ─── Group ────────────────────────────────────────────────────────────────────

// CreateGroupRequest 创建权限组的请求体。
type CreateGroupRequest struct {
	Name string `json:"name" binding:"required"`
}

// UpdateGroupRequest 修改权限组的请求体(目前仅支持改名)。
type UpdateGroupRequest struct {
	Name *string `json:"name,omitempty"`
}

// GroupResponse 权限组的展示 DTO。
type GroupResponse struct {
	ID          uint64 `json:"id,string"`
	OrgID       uint64 `json:"org_id,string"`
	Name        string `json:"name"`
	OwnerUserID uint64 `json:"owner_user_id,string"`
	MemberCount int64  `json:"member_count"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// ListGroupsResponse 分页列组响应。
type ListGroupsResponse struct {
	Items []GroupResponse `json:"items"`
	Total int64           `json:"total"`
	Page  int             `json:"page"`
	Size  int             `json:"size"`
}

// ─── Group Member ─────────────────────────────────────────────────────────────

// AddGroupMemberRequest 加成员请求。
type AddGroupMemberRequest struct {
	UserID uint64 `json:"user_id,string" binding:"required"`
}

// GroupMemberResponse 组成员展示 DTO。
type GroupMemberResponse struct {
	UserID   uint64 `json:"user_id,string"`
	JoinedAt int64  `json:"joined_at"`
}

// ListGroupMembersResponse 分页列成员响应。
type ListGroupMembersResponse struct {
	Items []GroupMemberResponse `json:"items"`
	Total int64                 `json:"total"`
	Page  int                   `json:"page"`
	Size  int                   `json:"size"`
}
