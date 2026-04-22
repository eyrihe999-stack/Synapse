// perms.go 集中定义所有操作权限位常量及系统角色默认权限集。
//
// 设计:权限位是字符串(命名空间.动作 形如 "org.update"),业务模块通过
// permission.PermXxx 常量引用,统一来源避免漂移。
//
// 系统角色的默认 permission 集由 SystemRoleDefaultPermissions 返回,
// org module 的 migration 用它 seed `org_roles.permissions` 字段。
package permission

// ─── 权限位常量 ─────────────────────────────────────────────────────────────

const (
	// ─── org 操作 ───
	PermOrgTransfer = "org.transfer" // 转让 owner(M5+ 才有端点)
	PermOrgDissolve = "org.dissolve" // 解散 org(DELETE /orgs/:slug)
	PermOrgUpdate   = "org.update"   // 改 org 基础信息(PATCH /orgs/:slug)

	// ─── 成员管理 ───
	PermMemberInvite     = "member.invite"      // 创建/列/撤销邀请
	PermMemberRemove     = "member.remove"      // 踢人(自我退出不算)
	PermMemberRoleAssign = "member.role_assign" // 改成员角色

	// ─── 角色管理 ───
	PermRoleManage       = "role.manage"        // 建/改/删 custom role
	PermRoleManageSystem = "role.manage_system" // 改系统角色 permissions(owner 专属)

	// ─── 知识源 ───
	PermSourceCreate    = "source.create"     // 创建 source(M2 只 lazy 创建,占位常量)
	PermSourceDeleteAny = "source.delete_any" // 删除别人的 source(M5+)

	// ─── 权限组 ───
	PermGroupCreate    = "group.create"     // 创建权限组
	PermGroupDeleteAny = "group.delete_any" // 删除别人建的组(M5+)

	// ─── 审计 ───
	PermAuditReadAll = "audit.read_all" // 查看全 org 的审计日志(member 默认无,只能看自己作为 actor 的)
)

// AllPermissions 返回当前定义的全部权限位(顺序固定,owner 默认集复用此列表)。
//
// 加新 perm 时按字典序插入,owner 默认就拿到。
func AllPermissions() []string {
	return []string{
		PermOrgTransfer, PermOrgDissolve, PermOrgUpdate,
		PermMemberInvite, PermMemberRemove, PermMemberRoleAssign,
		PermRoleManage, PermRoleManageSystem,
		PermSourceCreate, PermSourceDeleteAny,
		PermGroupCreate, PermGroupDeleteAny,
		PermAuditReadAll,
	}
}

// SystemRoleDefaultPermissions 返回某 slug 的系统角色默认 permission 集。
//
// 用于 org 模块的 migration seed:首次跑或老库刚加 permissions 列时,
// 把 owner/admin/member 三条系统角色行的 permissions 字段填上对应默认。
//
// 不在列表内的 slug(包括自定义 role)返 nil — custom role 默认 permissions 为空,
// admin 通过 role.manage 接口手动配(M5)。
//
// 设计点:owner 拿全部权限;admin 拿"管理"类(无 transfer/dissolve/role.manage_system);
// member 拿"建造"类(创建 source/group)。详见 docs/architecture-permission.md。
func SystemRoleDefaultPermissions(slug string) []string {
	switch slug {
	case "owner":
		return AllPermissions()
	case "admin":
		return []string{
			PermOrgUpdate,
			PermMemberInvite, PermMemberRemove, PermMemberRoleAssign,
			PermRoleManage,
			PermSourceCreate, PermSourceDeleteAny,
			PermGroupCreate, PermGroupDeleteAny,
			PermAuditReadAll,
		}
	case "member":
		return []string{
			PermSourceCreate,
			PermGroupCreate,
		}
	}
	return nil
}
