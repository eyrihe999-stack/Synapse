// const.go 权限模块常量定义。
//
// 本模块负责:
//   - 权限组(Group)及组成员关系
//   - 权限变更审计日志(PermissionAuditLog)
//
// 权限组是 per-org 的"用户集合",用于资源级 ACL 的授权目标。M1 阶段只建组,
// 不接 ACL 检查;ACL 表与判定算法在 M3 引入。
package permission

// ─── 表名常量 ─────────────────────────────────────────────────────────────────

const (
	// TableGroups 权限组主表
	TableGroups = "perm_groups"
	// TableGroupMembers 权限组成员表
	TableGroupMembers = "perm_group_members"
	// TablePermissionAuditLog 权限变更审计日志表
	TablePermissionAuditLog = "permission_audit_log"
	// TableResourceACL 资源 ACL 表(M3:目前只挂 source 类型)
	TableResourceACL = "resource_acl"
)

// ─── 默认值与上限 ─────────────────────────────────────────────────────────────

const (
	// DefaultPageSize 列表接口默认分页大小
	DefaultPageSize = 20
	// MaxPageSize 列表接口最大分页大小
	MaxPageSize = 100

	// MaxGroupNameLength 权限组名称最大长度
	MaxGroupNameLength = 64
	// MaxGroupsPerOrg 单个 org 内最多允许的权限组数量
	MaxGroupsPerOrg = 200
	// MaxMembersPerGroup 单个权限组最多允许的成员数量(防意外炸表;远超 org 上限的硬墙)
	MaxMembersPerGroup = 5000
)

// 审计 action / target_type 常量在 model 包(model.AuditAction*, model.AuditTarget*),
// 因为它们与 PermissionAuditLog 模型字段绑定;repo 层写入 audit 时直接复用 model 包常量,
// 无需反向 import 根包导致循环依赖。
