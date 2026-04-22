// models.go 权限模块数据模型定义。
//
// 三张表:
//   - Group:               权限组主表(per-org)
//   - GroupMember:         权限组成员关系
//   - PermissionAuditLog:  权限变更审计日志(append-only)
package model

import (
	"time"

	"gorm.io/datatypes"
)

// ─── 表名常量(同步于 internal/permission/const.go) ──────────────────────────
//
// 这里重复定义表名字符串而不是 import 根包,是为了避免 root → model 的循环依赖:
// model 是底层包,不应该 import 业务常量包。
const (
	tableGroups       = "perm_groups"
	tableGroupMembers = "perm_group_members"
	tableAuditLog     = "permission_audit_log"
	tableResourceACL  = "resource_acl"
)

// ─── ACL subject_type / permission / resource_type 枚举 ────────────────────────
//
// resource_type 当前只 'source';未来 doc-level 覆盖等扩展走同表加新值。
// subject_type 区分 ACL 授权对象:'group'(组)或 'user'(直授)。
// permission 区分授权级别:'read'(只读)或 'write'(读+写)。
const (
	ACLResourceTypeSource = "source"

	ACLSubjectTypeGroup = "group"
	ACLSubjectTypeUser  = "user"

	ACLPermRead  = "read"
	ACLPermWrite = "write"
)

// IsValidACLSubjectType 判断 subject_type 取值是否合法。
func IsValidACLSubjectType(t string) bool {
	switch t {
	case ACLSubjectTypeGroup, ACLSubjectTypeUser:
		return true
	}
	return false
}

// IsValidACLPermission 判断 permission 取值是否合法。
func IsValidACLPermission(p string) bool {
	switch p {
	case ACLPermRead, ACLPermWrite:
		return true
	}
	return false
}

// IsValidACLResourceType 判断 resource_type 取值是否合法(M3 仅 source)。
func IsValidACLResourceType(t string) bool {
	switch t {
	case ACLResourceTypeSource:
		return true
	}
	return false
}

// ─── ResourceACL 资源 ACL ─────────────────────────────────────────────────────

// ResourceACL 记录"某 subject 对某 resource 拥有什么 permission"。
//
// 设计要点:
//   - 一个 (resource_type, resource_id, subject_type, subject_id) 至多一条 ACL 行
//     —— "这个主体对这个资源的权限",permission 字段是当前授权级别;改 read↔write 走 UPDATE。
//   - resource owner 隐式拥有 admin 级,不在表里写行(避免冗余 + 解耦语义)。
//   - subject_type='user' 用于"直授给单个 user"(不通过 group);'group' 是常态。
//   - 删除资源时,业务侧负责级联清 ACL(M3 不开放删 source 接口,先不实现 cascade)。
//   - GrantedBy 记录授权人,审计追溯用;不参与判定。
type ResourceACL struct {
	ID           uint64    `gorm:"primaryKey;autoIncrement"`
	OrgID        uint64    `gorm:"not null;index:idx_acl_org_subject,priority:1"`
	ResourceType string    `gorm:"size:32;not null;uniqueIndex:uk_acl_resource_subject,priority:1;index:idx_acl_resource,priority:1"`
	ResourceID   uint64    `gorm:"not null;uniqueIndex:uk_acl_resource_subject,priority:2;index:idx_acl_resource,priority:2"`
	SubjectType  string    `gorm:"size:16;not null;uniqueIndex:uk_acl_resource_subject,priority:3;index:idx_acl_org_subject,priority:2"`
	SubjectID    uint64    `gorm:"not null;uniqueIndex:uk_acl_resource_subject,priority:4;index:idx_acl_org_subject,priority:3"`
	Permission   string    `gorm:"size:16;not null"`
	GrantedBy    uint64    `gorm:"not null;default:0"`
	CreatedAt    time.Time
}

// TableName 返回 ACL 表名。
func (ResourceACL) TableName() string { return tableResourceACL }

// ─── 审计 action 常量 ─────────────────────────────────────────────────────────
//
// audit log 的 action 字段枚举。M1 只接入 group.* 系列;
// M3+ 引入 ACL 时补 source.acl_*,M4+ 引入 RBAC 时补 member.* / role.*。
//
// 放在 model 包是因为 action / target_type 常量与 PermissionAuditLog 字段绑定,
// repo 层写入 audit 时直接引用,避免反向 import 根包。
const (
	// AuditActionGroupCreate 创建权限组
	AuditActionGroupCreate = "group.create"
	// AuditActionGroupRename 重命名权限组
	AuditActionGroupRename = "group.rename"
	// AuditActionGroupDelete 删除权限组(连同所有成员关系)
	AuditActionGroupDelete = "group.delete"
	// AuditActionGroupMemberAdd 把用户加入权限组
	AuditActionGroupMemberAdd = "group.member_add"
	// AuditActionGroupMemberRemove 把用户从权限组移除
	AuditActionGroupMemberRemove = "group.member_remove"
)

// ─── 审计 target_type 常量 ────────────────────────────────────────────────────
//
// 成员变更类事件 target_id 取 group_id(以"组"为主体方便聚合查询),
// 被影响的 user_id 落在 metadata 里。
const (
	// AuditTargetGroup 审计目标:权限组本体
	AuditTargetGroup = "group"
	// AuditTargetGroupMember 审计目标:权限组成员关系
	AuditTargetGroupMember = "group_member"
)

// ─── Group 权限组 ─────────────────────────────────────────────────────────────

// Group 表示一个 org 内的权限组(用户的命名集合)。
//
// 设计要点:
//   - per-org:每个组属于唯一一个 org,跨 org 不共享;Name 在 org 内唯一
//   - OwnerUserID:创建者,默认是组的 owner;owner 可改名 / 删组 / 管成员
//   - 任何 org 成员都可以创建组(无权限位检查;M4 RBAC 上线后可加 group.create 权限)
//   - 删组级联:删除 Group 时同事务删除 GroupMember 行
//
// 后续 ACL 表会引用 Group.ID(subject_type='group', subject_id=group_id),
// 但 M1 阶段不引入 ACL 表,组仅作为待用容器存在。
type Group struct {
	ID          uint64    `gorm:"primaryKey;autoIncrement"`
	OrgID       uint64    `gorm:"not null;uniqueIndex:uk_perm_groups_org_name,priority:1;index:idx_perm_groups_org"`
	Name        string    `gorm:"size:64;not null;uniqueIndex:uk_perm_groups_org_name,priority:2"`
	OwnerUserID uint64    `gorm:"not null;index:idx_perm_groups_owner"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// TableName 返回权限组表名。
func (Group) TableName() string { return tableGroups }

// ─── GroupMember 组成员关系 ───────────────────────────────────────────────────

// GroupMember 表示某个 user 加入了某个 Group。
//
// 设计要点:
//   - (group_id, user_id) 唯一,同人不能加两次
//   - 不存 role / permission 字段:M1 组内角色平等(都是普通成员);
//     owner 信息在 Group.OwnerUserID,不在这里冗余
//   - 删除 Group 时同事务清空对应 group_id 行(repo 层负责)
//   - 加入校验"该 user 必须是组所属 org 的成员"在 service 层做
type GroupMember struct {
	GroupID  uint64    `gorm:"primaryKey;autoIncrement:false"`
	UserID   uint64    `gorm:"primaryKey;autoIncrement:false;index:idx_perm_group_members_user"`
	JoinedAt time.Time `gorm:"not null"`
}

// TableName 返回组成员表名。
func (GroupMember) TableName() string { return tableGroupMembers }

// ─── PermissionAuditLog 权限审计 ──────────────────────────────────────────────

// PermissionAuditLog 是权限相关变更的 append-only 审计日志。
//
// 设计要点:
//   - append-only:从不更新、从不删除;查询走索引
//   - 全量快照:Before / After 存变更前后的完整对象 JSON,排查时无需拼接
//     (创建场景 Before 为空 jsonb 'null';删除场景 After 为空 jsonb 'null')
//   - 写入位置:repo 层 mutation 方法在事务内写一行 audit;service 层不直接调
//     (设计选 (a):repo 写入,actor 从 ctx 取)
//   - 多租户隔离:OrgID 必填,所有查询都按 org 过滤
//
// 字段语义:
//   - ActorUserID:执行操作的用户;系统/迁移路径写 0
//   - Action:见 permission.AuditAction* 常量
//   - TargetType / TargetID:被操作对象,见 permission.AuditTarget* 常量
//   - Before / After:操作前后的对象快照;具体形状取决于 Action,前端按 Action 解释
//   - Metadata:补充上下文(如 group_member_add 时记 group_id),非快照
type PermissionAuditLog struct {
	ID           uint64         `gorm:"primaryKey;autoIncrement"`
	OrgID        uint64         `gorm:"not null;index:idx_perm_audit_org_created,priority:1"`
	ActorUserID  uint64         `gorm:"not null;default:0;index:idx_perm_audit_actor"`
	Action       string         `gorm:"size:64;not null;index:idx_perm_audit_action"`
	TargetType   string         `gorm:"size:32;not null;index:idx_perm_audit_target,priority:1"`
	TargetID     uint64         `gorm:"not null;index:idx_perm_audit_target,priority:2"`
	Before       datatypes.JSON `gorm:"type:json"`
	After        datatypes.JSON `gorm:"type:json"`
	Metadata     datatypes.JSON `gorm:"type:json"`
	CreatedAt    time.Time      `gorm:"index:idx_perm_audit_org_created,priority:2,sort:desc"`
}

// TableName 返回审计日志表名。
func (PermissionAuditLog) TableName() string { return tableAuditLog }
