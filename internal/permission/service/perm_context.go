// perm_context.go 把 user 的 group_ids 缓存到 request ctx,避免单请求内多次重复打 DB。
//
// 流程:
//
//	JWTAuth → OrgContextMiddleware(注入 org)→ PermContextMiddleware(注入 user_groups)→ handler
//
// PermissionService 的 GroupsOfUser 优先读 ctx;ctx 缺失再打 DB(单测/未走中间件兜底)。
package service

import "context"

// permCtxKey 用类型化 key 防止跨包冲突。
type permCtxKey struct{}

// permCtx 是塞进 ctx 的载荷:per (orgID) 缓存的 group ids + 权限位。
//
// 同请求内只会针对一个 org context(OrgContextMiddleware 锁定),
// 所以单 org_id 就够,不必 map[orgID]...。
//
// 字段:
//   - groupIDs:   user 加入的所有 group id(给 ACL 判定用)
//   - permissions:user 当前 role 的 permissions(给 RequirePerm 用)
//   - permsLoaded:true 表示已经查过 permissions(可能是 nil 即"非成员"或"无 perm 配置")
type permCtx struct {
	orgID       uint64
	groupIDs    []uint64
	permissions []string
	permsLoaded bool
}

// WithUserGroups 把 (orgID, groupIDs) 注入 ctx。PermContextMiddleware 调。
//
// 注意:此函数会重置 permissions 部分;通常和 WithPermissions 在同一中间件里成对调用。
func WithUserGroups(ctx context.Context, orgID uint64, groupIDs []uint64) context.Context {
	pc := loadOrInit(ctx, orgID)
	pc.groupIDs = groupIDs
	return context.WithValue(ctx, permCtxKey{}, pc)
}

// WithPermissions 把 (orgID, permissions) 注入 ctx。
func WithPermissions(ctx context.Context, orgID uint64, permissions []string) context.Context {
	pc := loadOrInit(ctx, orgID)
	pc.permissions = permissions
	pc.permsLoaded = true
	return context.WithValue(ctx, permCtxKey{}, pc)
}

// loadOrInit 复用 ctx 里已有的 permCtx(若 orgID 匹配),否则新建。
//
// 设计上同请求 orgID 不会变;不匹配时按"上下文出错"清空重置(保守起见)。
func loadOrInit(ctx context.Context, orgID uint64) *permCtx {
	if v := ctx.Value(permCtxKey{}); v != nil {
		if pc, ok := v.(*permCtx); ok && pc != nil && pc.orgID == orgID {
			return pc
		}
	}
	return &permCtx{orgID: orgID}
}

// groupsFromContext 取出 ctx 缓存的 group ids;
// 仅当 orgID 与缓存的 orgID 匹配时返回(防止串味)。
func groupsFromContext(ctx context.Context, orgID uint64) ([]uint64, bool) {
	v := ctx.Value(permCtxKey{})
	if v == nil {
		return nil, false
	}
	pc, ok := v.(*permCtx)
	if !ok || pc == nil {
		return nil, false
	}
	if pc.orgID != orgID {
		return nil, false
	}
	return pc.groupIDs, true
}

// permissionsFromContext 取出 ctx 缓存的 user permissions;
// 仅当 orgID 匹配且确实加载过(permsLoaded=true)才返 ok。
func permissionsFromContext(ctx context.Context, orgID uint64) ([]string, bool) {
	return PermissionsFromContext(ctx, orgID)
}

// PermissionsFromContext 是 permissionsFromContext 的导出版本,供 handler 包(中间件)读 ctx 用。
func PermissionsFromContext(ctx context.Context, orgID uint64) ([]string, bool) {
	v := ctx.Value(permCtxKey{})
	if v == nil {
		return nil, false
	}
	pc, ok := v.(*permCtx)
	if !ok || pc == nil {
		return nil, false
	}
	if pc.orgID != orgID || !pc.permsLoaded {
		return nil, false
	}
	return pc.permissions, true
}
