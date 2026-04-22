// dto.go source 模块 HTTP 请求/响应 DTO 定义。
package dto

// ─── Source ───────────────────────────────────────────────────────────────────

// SourceResponse 知识源的展示 DTO。
type SourceResponse struct {
	ID          uint64 `json:"id,string"`
	OrgID       uint64 `json:"org_id,string"`
	Kind        string `json:"kind"`
	OwnerUserID uint64 `json:"owner_user_id,string"`
	ExternalRef string `json:"external_ref,omitempty"`
	Name        string `json:"name"`
	Visibility  string `json:"visibility"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// ListSourcesResponse 分页列 source 响应。
type ListSourcesResponse struct {
	Items []SourceResponse `json:"items"`
	Total int64            `json:"total"`
	Page  int              `json:"page"`
	Size  int              `json:"size"`
}

// UpdateVisibilityRequest 改 visibility 的请求体。
type UpdateVisibilityRequest struct {
	Visibility string `json:"visibility" binding:"required"`
}

// CreateSourceRequest 用户自建一个 custom 数据源。
// visibility 省略 → 默认 org;name 必填,2-128 字符(前后空白会被 trim 后校验)。
type CreateSourceRequest struct {
	Name       string `json:"name" binding:"required"`
	Visibility string `json:"visibility,omitempty"`
}

// ─── Source ACL ───────────────────────────────────────────────────────────────

// GrantSourceACLRequest 添加一条 source ACL 授权。
type GrantSourceACLRequest struct {
	SubjectType string `json:"subject_type" binding:"required"` // "group" | "user"
	SubjectID   uint64 `json:"subject_id,string" binding:"required"`
	Permission  string `json:"permission" binding:"required"` // "read" | "write"
}

// UpdateSourceACLRequest 改一条 ACL 的 permission(只能改 permission,subject 不可变)。
type UpdateSourceACLRequest struct {
	Permission string `json:"permission" binding:"required"` // "read" | "write"
}

// SourceACLResponse ACL 行的展示 DTO。
type SourceACLResponse struct {
	ID          uint64 `json:"id,string"`
	SourceID    uint64 `json:"source_id,string"`
	SubjectType string `json:"subject_type"`
	SubjectID   uint64 `json:"subject_id,string"`
	Permission  string `json:"permission"`
	GrantedBy   uint64 `json:"granted_by,string"`
	CreatedAt   int64  `json:"created_at"`
}

// ListSourceACLResponse list ACL 响应(不分页,单 source 的 ACL 行天然量级小)。
type ListSourceACLResponse struct {
	Items []SourceACLResponse `json:"items"`
}
