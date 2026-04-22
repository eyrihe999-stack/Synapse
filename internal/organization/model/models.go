// models.go 组织模块数据模型定义。
//
// 四张表:
//   - Org:           组织主表
//   - OrgRole:       组织角色(per-org,系统内置 + 自定义)
//   - OrgMember:     成员归属(关联 OrgRole)
//   - OrgInvitation: 邮件邀请(pending/accepted/revoked/expired 状态机)
package model

import "time"

// ─── 表名常量(同步于 internal/organization/const.go) ────────────────────────
//
// 这里重复定义表名字符串而不是 import 根包,是为了避免 root → model 的循环依赖:
// model 是底层包,不应该 import 业务常量包。

const (
	tableOrgs        = "orgs"
	tableOrgRoles    = "org_roles"
	tableOrgMembers  = "org_members"
	tableInvitations = "org_invitations"
)

// ─── 邀请状态 ────────────────────────────────────────────────────────────────
//
// 状态机:pending → {accepted | revoked | rejected | expired}
// 终态不可互转,也不可回 pending。
//   - revoked  : inviter 主动撤销(org 内成员操作)
//   - rejected : invitee 主动拒绝(被邀请人自己在收件箱里拒)
//   - expired  : 懒过期,Accept/Preview/List/ListMine 任一读路径发现过期就就地改
const (
	InvitationStatusPending  = "pending"
	InvitationStatusAccepted = "accepted"
	InvitationStatusRevoked  = "revoked"
	InvitationStatusRejected = "rejected"
	InvitationStatusExpired  = "expired"
)

// ─── 审计 action / target 常量 ────────────────────────────────────────────────
//
// M4:成员变更同事务写一条 permission_audit_log 行,与权限组 / source ACL 共表。
// 各模块拥有自己的 action 命名空间,前缀 "member." 锁定 org 模块。
const (
	// AuditActionMemberAdd 成员加入 org(创建 org 的 owner / 接受邀请的 user / 系统添加)
	AuditActionMemberAdd = "member.add"
	// AuditActionMemberRemove 成员被移除(踢人 / 自我退出 / 解散级联,actor==target 判 self)
	AuditActionMemberRemove = "member.remove"
	// AuditActionMemberRoleChange 成员的 role_id 变更
	AuditActionMemberRoleChange = "member.role_change"

	// AuditActionRoleCreate 创建自定义角色
	AuditActionRoleCreate = "role.create"
	// AuditActionRoleUpdate 改 display_name
	AuditActionRoleUpdate = "role.update"
	// AuditActionRolePermissionsChange 改 permissions(自定义 / 系统都用此 action,
	// 系统/自定义靠 metadata.is_system 区分)
	AuditActionRolePermissionsChange = "role.permissions_change"
	// AuditActionRoleDelete 删除自定义角色
	AuditActionRoleDelete = "role.delete"
)

// AuditTargetMember 审计目标:org_members 表行;target_id 取 OrgMember.ID。
const AuditTargetMember = "org_member"

// AuditTargetRole 审计目标:org_roles 表行;target_id 取 OrgRole.ID。
const AuditTargetRole = "role"

// ─── 系统角色 slug ────────────────────────────────────────────────────────────
//
// 每个 org 在创建时自动 seed 这三条 OrgRole。slug 保留,不能用于自定义角色。
// IsSystem=true 的行禁止删改 slug/display_name。
const (
	SystemRoleSlugOwner  = "owner"
	SystemRoleSlugAdmin  = "admin"
	SystemRoleSlugMember = "member"
)

// ─── 模型级常量 ───────────────────────────────────────────────────────────────

const (
	// OrgStatusActive 组织正常状态
	OrgStatusActive = "active"
	// OrgStatusDissolved 组织已解散
	OrgStatusDissolved = "dissolved"
)

// ─── Org 组织主表 ─────────────────────────────────────────────────────────────

// Org 表示一个组织(多租户命名空间)。
// Slug 全局唯一,作为 URL 和 API 路径的一部分,创建后不可改。
// DisplayName 是展示用的名字,可重复、支持任意字符。
// OwnerUserID 指向当前所有者,解散/转让时更新此字段。
// Status 为 "dissolved" 的 org 被视为已软删除,所有调用拒绝。
type Org struct {
	ID          uint64 `gorm:"primaryKey;autoIncrement"`
	Slug        string `gorm:"size:32;not null;uniqueIndex:uk_orgs_slug"`
	DisplayName string `gorm:"size:64;not null"`
	Description string `gorm:"size:500"`
	OwnerUserID uint64 `gorm:"not null;index:idx_orgs_owner"`
	Status      string `gorm:"size:16;not null;default:active;index:idx_orgs_status"`
	DissolvedAt *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// TableName 返回组织表名。
func (Org) TableName() string { return tableOrgs }

// ─── OrgRole 组织角色 ────────────────────────────────────────────────────────

// OrgRole 表示一个 org 的角色。角色是 per-org 的 —— 每个 org 有自己独立的角色表,
// 创建 org 时自动 seed 三条系统角色(owner/admin/member)。
//
// IsSystem=true 的角色:
//   - slug 锁死(SystemRoleSlug* 三个之一)
//   - 不能被删除
//   - display_name 不能被修改
//
// IsSystem=false 的自定义角色:
//   - 由 owner/admin 手动创建
//   - slug 不能和系统角色冲突,org 内唯一
//   - 删除前调用方必须把所有挂在该角色上的成员迁走(service 层校验)
//
// 现阶段没有权限字段 —— 所有角色在业务逻辑上等价,角色只是身份标签。
// 未来加权限时往这张表加 permissions 字段或新开 role_permissions 表。
type OrgRole struct {
	ID          uint64 `gorm:"primaryKey;autoIncrement"`
	OrgID       uint64 `gorm:"not null;uniqueIndex:uk_roles_org_slug,priority:1;index:idx_roles_org"`
	Slug        string `gorm:"size:32;not null;uniqueIndex:uk_roles_org_slug,priority:2"`
	DisplayName string `gorm:"size:64;not null"`
	IsSystem    bool   `gorm:"not null;default:false"`
	// Permissions M4:角色拥有的操作权限位列表。系统角色由 migration 默认值 seed,
	// owner 可以通过 role.manage_system 改;自定义角色由 admin 通过 role.manage 改。
	// 存储为 MySQL JSON 列(自定义类型 PermissionSet,见 permission_set.go)。
	Permissions PermissionSet `gorm:"column:permissions;type:json"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// TableName 返回角色表名。
func (OrgRole) TableName() string { return tableOrgRoles }

// ─── OrgMember 成员关系 ──────────────────────────────────────────────────────

// OrgMember 表示一个用户加入了某个 org,并关联到该 org 的某个角色。
// RoleID 必非零(NOT NULL):
//   - 新创建 org 时 owner 挂 owner 角色
//   - 邀请接受 / 后续改角色 接口写入该字段
//   - AutoMigrate 新加列时 default=0 只为兼容迁移,migration 回填后不会再出现 0
//
// JoinedAt 是 org 创建时 owner 自动加入的时间。
type OrgMember struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement"`
	OrgID     uint64    `gorm:"not null;uniqueIndex:uk_members_org_user,priority:1;index:idx_members_org"`
	UserID    uint64    `gorm:"not null;uniqueIndex:uk_members_org_user,priority:2;index:idx_members_user"`
	RoleID    uint64    `gorm:"not null;default:0;index:idx_members_role"`
	JoinedAt  time.Time `gorm:"not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName 返回成员表名。
func (OrgMember) TableName() string { return tableOrgMembers }

// ─── OrgInvitation 邀请 ──────────────────────────────────────────────────────

// OrgInvitation 表示一封发向某 email 的入组邀请。
//
// 生命周期:
//   - Create:生成 raw token,DB 只存 SHA-256(token) = TokenHash;邮件里带 raw token
//   - Accept:登录用户持 raw token 调 accept,service 取 hash 查此表,校验 status=pending +
//     未过期 + 登录用户 email == Email,事务内写 OrgMember + 把 status 改 accepted
//   - Revoke:inviter/owner 主动撤销,pending → revoked
//   - Expire:懒过期。Accept/Preview/List 时如果 pending 且 ExpiresAt < now,
//     当场把 status 改 expired,操作按终态处理
//
// 唯一约束:
//   - (org_id, email) WHERE status='pending' → 同 org 同 email 最多一条 pending
//     MySQL 不支持"条件唯一索引",所以这个约束用 service 层查重兜底,不依赖 DB 唯一索引
//   - token_hash 全局唯一(sha256 碰撞概率可忽略,但唯一约束防止数据损坏)
//
// Resend 语义:生成新 token → 更新 TokenHash → 发新邮件,老链接自动失效。
type OrgInvitation struct {
	ID             uint64 `gorm:"primaryKey;autoIncrement"`
	OrgID          uint64 `gorm:"not null;index:idx_invites_org_status,priority:1;index:idx_invites_org_email,priority:1"`
	InviterUserID  uint64 `gorm:"not null;index:idx_invites_inviter"`
	Email          string `gorm:"size:255;not null;index:idx_invites_org_email,priority:2;index:idx_invites_email"`
	RoleID         uint64 `gorm:"not null;index:idx_invites_role"`
	TokenHash      string `gorm:"size:64;not null;uniqueIndex:uk_invites_token_hash"`
	Status         string `gorm:"size:16;not null;index:idx_invites_org_status,priority:2;default:pending"`
	ExpiresAt      time.Time  `gorm:"not null;index:idx_invites_expires"`
	AcceptedAt     *time.Time `gorm:"column:accepted_at"`
	AcceptedUserID uint64     `gorm:"not null;default:0"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// TableName 返回邀请表名。
func (OrgInvitation) TableName() string { return tableInvitations }
