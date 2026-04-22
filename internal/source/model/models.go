// models.go source 模块数据模型定义。
//
// 一张表:Source(知识源)。每个 doc 必属于一个 source,权限判定走 source 不走 doc。
package model

import "time"

// ─── 表名常量(同步于 internal/source/const.go) ─────────────────────────────
const (
	tableSources = "sources"
)

// ─── Kind 枚举 ────────────────────────────────────────────────────────────────
//
// 当前两类:
//   - manual_upload:用户手动上传的默认收件箱,每用户每 org 一条(lazy 创建)
//   - custom:      用户自建的命名"数据源",每用户可建多条,靠 Name 区分
//
// 后续扩展:
//   - gitlab_repo:GitLab 仓库同步源,external_ref = repo_id
//   - feishu_space:飞书空间同步源,external_ref = space_token
const (
	// KindManualUpload 用户默认收件箱:每用户每 org 一条,external_ref 为空
	KindManualUpload = "manual_upload"
	// KindCustom 用户自建的命名数据源:Name 必填,external_ref 为空;
	// 唯一性由 (org_id, owner_user_id, name) 保证,owner 可以建任意多条。
	KindCustom = "custom"

	// DefaultManualUploadName manual_upload 的回显名,migration 把历史行补成这个;
	// 新建 manual_upload(EnsureManualUpload)也用它初始化。
	DefaultManualUploadName = "我的上传"
)

// ─── Visibility 枚举 ──────────────────────────────────────────────────────────
//
// 默认 org —— 等价 M2 之前的"全 org 可见"行为。M3 引入 ACL 表后:
//   - org    : 全 org 可读;owner 可写;ACL 表无需有行
//   - group  : 必须配合 resource_acl 表使用,否则除 owner 外所有人无权限
//   - private: 仅 owner 可见(ACL 表无效)
const (
	VisibilityOrg     = "org"
	VisibilityGroup   = "group"
	VisibilityPrivate = "private"
)

// IsValidVisibility 判断 visibility 取值是否合法。
func IsValidVisibility(v string) bool {
	switch v {
	case VisibilityOrg, VisibilityGroup, VisibilityPrivate:
		return true
	}
	return false
}

// IsValidKind 判断 kind 取值是否合法。
func IsValidKind(k string) bool {
	switch k {
	case KindManualUpload, KindCustom:
		return true
	}
	return false
}

// ─── Source 知识源 ────────────────────────────────────────────────────────────

// Source 表示一个知识源(权限承载者)。
//
// 设计要点:
//   - per-org:每个 source 属于唯一 org,跨 org 不共享
//   - OwnerUserID:对于 manual_upload 是该用户;未来 gitlab_repo 是同步发起人
//   - ExternalRef:外部资源标识符;manual_upload 留空字符串(NOT NULL DEFAULT '')
//   - Visibility:权限可见性;M2 默认 org,M3 接入 ACL 表后真正生效
//   - 唯一约束 (org_id, kind, owner_user_id, external_ref):
//       - manual_upload:external_ref='' → 退化为 (org_id, kind, owner_user_id) 唯一
//         即每用户每 org 只有一条 manual_upload source(repo lazy-create 用此约束兜底)
//       - 未来 gitlab_repo:同 org 同 repo 同发起人不重复
type Source struct {
	ID          uint64 `gorm:"primaryKey;autoIncrement"`
	OrgID       uint64 `gorm:"not null;uniqueIndex:uk_sources_full,priority:1;index:idx_sources_org"`
	Kind        string `gorm:"size:32;not null;uniqueIndex:uk_sources_full,priority:2;index:idx_sources_kind"`
	OwnerUserID uint64 `gorm:"not null;uniqueIndex:uk_sources_full,priority:3;index:idx_sources_owner"`
	ExternalRef string `gorm:"size:255;not null;default:'';uniqueIndex:uk_sources_full,priority:4"`
	// Name 用户可读名称。manual_upload 由 migration 回填为 "我的上传";custom 由用户创建时指定。
	// 唯一性由额外的 uk_sources_owner_name 约束(org_id+owner_user_id+name)保证,允许不同人重名。
	Name       string `gorm:"size:128;not null;default:''"`
	Visibility string `gorm:"size:16;not null;default:org;index:idx_sources_visibility"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// TableName 返回 source 表名。
func (Source) TableName() string { return tableSources }

// ─── 审计 action 常量 ─────────────────────────────────────────────────────────
//
// 写入 permission_audit_log 时的 action 字段;前缀 "source." 避免和 group.* 冲突。
const (
	// AuditActionSourceCreate 创建 source(包括 lazy 创建的 manual_upload)
	AuditActionSourceCreate = "source.create"
	// AuditActionSourceVisibilityChange 改 visibility
	AuditActionSourceVisibilityChange = "source.visibility_change"
	// AuditActionSourceDelete 删除 source(M2 不开放,M3+ 才用)
	AuditActionSourceDelete = "source.delete"

	// AuditActionSourceACLGrant 添加一条 source ACL 授权
	AuditActionSourceACLGrant = "source.acl_grant"
	// AuditActionSourceACLRevoke 撤销一条 source ACL 授权
	AuditActionSourceACLRevoke = "source.acl_revoke"
	// AuditActionSourceACLUpdate 改 ACL 的 permission(read↔write)
	AuditActionSourceACLUpdate = "source.acl_update"
)

// AuditTargetSourceACL 审计目标:source 的 ACL 行;target_id 取 acl 行 id,
// metadata 带 source_id / subject_type / subject_id / permission 等上下文。
const AuditTargetSourceACL = "source_acl"

// AuditTargetSource 审计目标:source 本体。
const AuditTargetSource = "source"
