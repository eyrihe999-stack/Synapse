// models.go 组织模块数据模型定义。
//
// 包含 5 张表:
//   - Org: 组织主表
//   - OrgMember: 成员关系
//   - OrgRole: 角色(预设 + 自定义,per-org 存储)
//   - OrgInvitation: 邀请(待确认模型)
//   - OrgMemberRoleHistory: 角色变更审计
package model

import (
	"time"

	"gorm.io/datatypes"
)

// ─── 表名常量(同步于 internal/organization/const.go) ────────────────────────
//
// 这里重复定义表名字符串而不是 import 根包,是为了避免 root → model 的循环依赖:
// model 是底层包,不应该 import 业务常量包。

const (
	tableOrgs                 = "orgs"
	tableOrgMembers           = "org_members"
	tableOrgRoles             = "org_roles"
	tableOrgInvitations       = "org_invitations"
	tableOrgMemberRoleHistory = "org_member_role_history"
)

// ─── 模型级常量 ───────────────────────────────────────────────────────────────
// 与数据模型紧密绑定的枚举值,允许在 model 包内定义(V2 规范例外条款)。

const (
	// OrgStatusActive 组织正常状态
	OrgStatusActive = "active"
	// OrgStatusDissolved 组织已解散
	OrgStatusDissolved = "dissolved"
)

const (
	// InvitationTypeMember 普通成员邀请
	InvitationTypeMember = "member"
	// InvitationTypeOwnershipTransfer 所有权转让邀请
	InvitationTypeOwnershipTransfer = "ownership_transfer"
)

const (
	// InvitationStatusPending 待处理
	InvitationStatusPending = "pending"
	// InvitationStatusAccepted 已接受
	InvitationStatusAccepted = "accepted"
	// InvitationStatusRejected 已拒绝
	InvitationStatusRejected = "rejected"
	// InvitationStatusExpired 已过期
	InvitationStatusExpired = "expired"
	// InvitationStatusRevoked 被撤销
	InvitationStatusRevoked = "revoked"
)

// ─── Org 组织主表 ─────────────────────────────────────────────────────────────

// Org 表示一个企业版组织。
// Slug 全局唯一,作为 URL 和 API 路径的一部分,创建后不可改。
// DisplayName 是展示用的名字,可重复、支持任意字符。
// OwnerUserID 指向当前所有者,所有权转让时更新此字段。
// RequireAgentReview 控制 agent 发布是否需要审核(默认 false,owner 可开启)。
// RecordFullPayload 控制调用审计是否记录完整 payload(默认 false,owner 可开启)。
// Status 为 "dissolved" 的 org 被视为已软删除,所有调用拒绝。
type Org struct {
	ID                 uint64    `gorm:"primaryKey;autoIncrement"`
	Slug               string    `gorm:"size:32;not null;uniqueIndex:uk_orgs_slug"`
	DisplayName        string    `gorm:"size:64;not null"`
	Description        string    `gorm:"size:500"`
	OwnerUserID        uint64    `gorm:"not null;index:idx_orgs_owner"`
	Status             string    `gorm:"size:16;not null;default:active;index:idx_orgs_status"`
	RequireAgentReview bool      `gorm:"not null;default:false"`
	RecordFullPayload  bool      `gorm:"not null;default:false"`
	DissolvedAt        *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// TableName 返回组织表名。
func (Org) TableName() string { return tableOrgs }

// ─── OrgRole 角色表 ───────────────────────────────────────────────────────────

// OrgRole 定义一个角色及其权限集合。
// 角色是 per-org 存储的:每个 org 创建时自动插入 3 行预设角色(owner/admin/member),
// owner 可额外创建自定义角色。
// IsPreset 为 true 的角色不可删除、不可修改权限集。
// Permissions 是 JSON 数组,存储权限字符串列表,对应 const.go 的 PermXxx 常量。
type OrgRole struct {
	ID          uint64         `gorm:"primaryKey;autoIncrement"`
	OrgID       uint64         `gorm:"not null;index:idx_roles_org;uniqueIndex:uk_roles_org_name,priority:1"`
	Name        string         `gorm:"size:32;not null;uniqueIndex:uk_roles_org_name,priority:2"`
	DisplayName string         `gorm:"size:64;not null"`
	IsPreset    bool           `gorm:"not null;default:false"`
	Permissions datatypes.JSON `gorm:"type:json;not null"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// TableName 返回角色表名。
func (OrgRole) TableName() string { return tableOrgRoles }

// ─── OrgMember 成员关系 ──────────────────────────────────────────────────────

// OrgMember 表示一个用户加入了某个 org。
// 每个成员在一个 org 内只持有一个角色(单角色模型,第一版不做多角色合并)。
// RoleID 指向 org_roles.id,变更角色即 UPDATE 该字段。
// JoinedAt 是接受邀请的时间(或 org 创建时 owner 自动加入的时间)。
type OrgMember struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement"`
	OrgID     uint64    `gorm:"not null;uniqueIndex:uk_members_org_user,priority:1;index:idx_members_org"`
	UserID    uint64    `gorm:"not null;uniqueIndex:uk_members_org_user,priority:2;index:idx_members_user"`
	RoleID    uint64    `gorm:"not null;index:idx_members_role"`
	JoinedAt  time.Time `gorm:"not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName 返回成员表名。
func (OrgMember) TableName() string { return tableOrgMembers }

// ─── OrgInvitation 邀请 ──────────────────────────────────────────────────────

// OrgInvitation 表示一条发出的邀请(待确认模型)。
// 被邀请人必须已注册(InviteeUserID 必须是合法的 user_id)。
// RoleID 是接受后将被赋予的角色。
// Type 区分普通成员邀请和所有权转让邀请(ownership_transfer 接受后 owner 交接)。
// Status 流转:pending → accepted / rejected / expired / revoked。
// 同 org + 同 invitee + status=pending 只能存在一条(由 service 层事务保证)。
type OrgInvitation struct {
	ID             uint64 `gorm:"primaryKey;autoIncrement"`
	OrgID          uint64 `gorm:"not null;index:idx_invitations_org_status,priority:1"`
	InviterUserID  uint64 `gorm:"not null"`
	InviteeUserID  uint64 `gorm:"not null;index:idx_invitations_invitee_status,priority:1"`
	RoleID         uint64 `gorm:"not null"`
	Type           string `gorm:"size:32;not null;default:member"`
	Status         string `gorm:"size:16;not null;default:pending;index:idx_invitations_org_status,priority:2;index:idx_invitations_invitee_status,priority:2"`
	ExpiresAt      time.Time `gorm:"not null;index:idx_invitations_expires"`
	RespondedAt    *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// TableName 返回邀请表名。
func (OrgInvitation) TableName() string { return tableOrgInvitations }

// ─── OrgMemberRoleHistory 角色变更审计 ───────────────────────────────────────

// OrgMemberRoleHistory 记录某成员在某 org 内的角色变更历史。
// FromRoleID 为 nil 表示首次加入(RoleChangeReasonJoin)。
// Reason 使用 const.go 的 RoleChangeReasonXxx 枚举。
type OrgMemberRoleHistory struct {
	ID              uint64  `gorm:"primaryKey;autoIncrement"`
	OrgID           uint64  `gorm:"not null;index:idx_history_org_user,priority:1"`
	UserID          uint64  `gorm:"not null;index:idx_history_org_user,priority:2"`
	FromRoleID      *uint64 // nil 表示首次加入
	ToRoleID        uint64  `gorm:"not null"`
	ChangedByUserID uint64  `gorm:"not null"`
	Reason          string  `gorm:"size:128;not null"`
	CreatedAt       time.Time `gorm:"index:idx_history_org_user,priority:3"`
}

// TableName 返回角色历史表名。
func (OrgMemberRoleHistory) TableName() string { return tableOrgMemberRoleHistory }
