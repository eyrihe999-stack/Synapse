// role_service.go 组织角色管理 service。
//
// 职责:
//   - 列出 org 所有角色(系统 + 自定义)
//   - 创建 / 修改 / 删除自定义角色
//   - 改成员的角色
//
// 现阶段所有操作只做"是 org 成员"这一层校验,不分 owner/admin/member 权限 ——
// owner/admin/member 只是身份标签。权限系统后续单独设计。
//
// 关键硬规则(权限无关,即使以后加了权限也不变):
//   - 系统角色(owner/admin/member)不可删除、不可改 slug、不可改 display_name
//   - 删除自定义角色前须把所有挂该角色的成员迁走(service 拒绝)
//   - 不能通过"改成员角色"接口给成员分配 owner 角色(转让走独立未来接口)
//   - 不能通过"改成员角色"接口修改当前 owner(Org.OwnerUserID)的角色
package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/organization"
	"github.com/eyrihe999-stack/Synapse/internal/organization/dto"
	"github.com/eyrihe999-stack/Synapse/internal/organization/model"
	"github.com/eyrihe999-stack/Synapse/internal/organization/repository"
	"github.com/eyrihe999-stack/Synapse/internal/permission"
	"gorm.io/gorm"
)

// RoleService 定义角色管理的业务操作。
//
// M5:create / update / assign 都加了 callerPerms 参数 —— caller 在该 org 的当前 permissions,
// 用于权限上限校验。handler 从 ctx(PermContextMiddleware 缓存)取后传入。
//
//sayso-lint:ignore interface-pollution
type RoleService interface {
	// ListRoles 返回某 org 的所有角色(系统 + 自定义),系统角色在前。
	ListRoles(ctx context.Context, orgID uint64) ([]dto.RoleResponse, error)

	// CreateCustomRole 创建一个自定义角色。
	// req.Permissions 提供时必须是 callerPerms 的子集(权限上限规则)。
	CreateCustomRole(ctx context.Context, orgID uint64, callerPerms []string, req dto.CreateRoleRequest) (*dto.RoleResponse, error)

	// UpdateCustomRole 修改自定义角色的 display_name 和/或 permissions(slug 不可改)。系统角色拒绝。
	// 提供 permissions 时同样走权限上限校验。
	UpdateCustomRole(ctx context.Context, orgID uint64, slug string, callerPerms []string, req dto.UpdateRoleRequest) (*dto.RoleResponse, error)

	// DeleteCustomRole 删除自定义角色。系统角色拒绝;有成员挂在该角色时也拒绝。
	DeleteCustomRole(ctx context.Context, orgID uint64, slug string) error

	// UpdateRolePermissions 替换任意 role(含系统角色)的 permissions 字段。
	// 由独立 endpoint 走 role.manage_system 权限,通常 owner 才有。
	// 仍走权限上限校验(owner 自身有所有 perm,所以总能通过)。
	// 系统角色 owner slug 的 permissions 也允许改吗?设计上允许 —— owner 角色的 perm 集合
	// 只有 owner 能改;若 owner 把自己的角色改残了,只能靠 DB 直接改回(罕见)。
	UpdateRolePermissions(ctx context.Context, orgID uint64, slug string, callerPerms []string, req dto.UpdateRolePermissionsRequest) (*dto.RoleResponse, error)

	// AssignRoleToMember 修改 (org_id, targetUserID) 成员的角色。
	// 不能分配 owner 角色,不能修改 owner member 的角色。
	// callerPerms:caller 自身权限,用于"被分配的 role.permissions 必须是 caller perms 的子集"校验。
	AssignRoleToMember(ctx context.Context, orgID, targetUserID uint64, callerPerms []string, req dto.AssignRoleRequest) (*dto.RoleResponse, error)
}

// ─── 实现 ────────────────────────────────────────────────────────────────────

type roleService struct {
	repo   repository.Repository
	logger logger.LoggerInterface
}

// NewRoleService 构造一个 RoleService 实例。
func NewRoleService(repo repository.Repository, log logger.LoggerInterface) RoleService {
	return &roleService{repo: repo, logger: log}
}

// roleSlugRegexp 预编译的 role slug 校验正则。
var roleSlugRegexp = regexp.MustCompile(organization.RoleSlugPattern)

// ListRoles 返回某 org 的所有角色。
//
// 可能的错误:
//   - ErrOrgInternal:repo 查询失败
func (s *roleService) ListRoles(ctx context.Context, orgID uint64) ([]dto.RoleResponse, error) {
	roles, err := s.repo.ListRolesByOrg(ctx, orgID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "列出角色失败", err, map[string]any{"org_id": orgID})
		return nil, fmt.Errorf("list roles: %w: %w", err, organization.ErrOrgInternal)
	}
	out := make([]dto.RoleResponse, 0, len(roles))
	for _, r := range roles {
		out = append(out, roleToDTO(r))
	}
	return out, nil
}

// CreateCustomRole 创建一个自定义角色。
//
// 可能的错误:
//   - ErrRoleSlugInvalid:slug 格式非法
//   - ErrRoleSlugReserved:slug 和系统保留 slug 冲突
//   - ErrRoleDisplayNameInvalid:display_name 为空或超长
//   - ErrRoleSlugTaken:该 org 内已有同名 slug
//   - ErrMaxCustomRolesReached:超出自定义角色上限
//   - ErrRolePermissionInvalid:permissions 包含未知 perm
//   - ErrRolePermissionCeilingExceeded:permissions 超出 caller 上限
//   - ErrOrgInternal:数据库操作失败
func (s *roleService) CreateCustomRole(ctx context.Context, orgID uint64, callerPerms []string, req dto.CreateRoleRequest) (*dto.RoleResponse, error) {
	if !roleSlugRegexp.MatchString(req.Slug) {
		s.logger.WarnCtx(ctx, "role slug 格式非法", map[string]any{"org_id": orgID, "slug": req.Slug})
		return nil, fmt.Errorf("invalid role slug: %w", organization.ErrRoleSlugInvalid)
	}
	if organization.IsSystemRoleSlug(req.Slug) {
		s.logger.WarnCtx(ctx, "role slug 占用系统保留 slug", map[string]any{"org_id": orgID, "slug": req.Slug})
		return nil, fmt.Errorf("role slug reserved: %w", organization.ErrRoleSlugReserved)
	}
	if req.DisplayName == "" || len(req.DisplayName) > organization.MaxRoleDisplayNameLength {
		s.logger.WarnCtx(ctx, "role display_name 非法", map[string]any{"org_id": orgID, "len": len(req.DisplayName)})
		return nil, fmt.Errorf("invalid role display name: %w", organization.ErrRoleDisplayNameInvalid)
	}
	if err := validateRolePermissions(ctx, s.logger, req.Permissions, callerPerms); err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}

	// 自定义角色数量上限
	count, err := s.repo.CountCustomRolesByOrg(ctx, orgID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "统计自定义角色失败", err, map[string]any{"org_id": orgID})
		return nil, fmt.Errorf("count custom roles: %w: %w", err, organization.ErrOrgInternal)
	}
	if int(count) >= organization.MaxCustomRolesPerOrg {
		s.logger.WarnCtx(ctx, "超出自定义角色上限", map[string]any{"org_id": orgID, "count": count, "max": organization.MaxCustomRolesPerOrg})
		return nil, fmt.Errorf("max custom roles reached: %w", organization.ErrMaxCustomRolesReached)
	}

	// slug 重复预检(DB 唯一索引是最终保证,这里先查一次返回友好错误)
	if existing, findErr := s.repo.FindRoleByOrgAndSlug(ctx, orgID, req.Slug); findErr == nil && existing != nil {
		s.logger.WarnCtx(ctx, "role slug 已被占用", map[string]any{"org_id": orgID, "slug": req.Slug})
		return nil, fmt.Errorf("role slug taken: %w", organization.ErrRoleSlugTaken)
	} else if findErr != nil && !errors.Is(findErr, gorm.ErrRecordNotFound) {
		s.logger.ErrorCtx(ctx, "查 role slug 失败", findErr, map[string]any{"org_id": orgID, "slug": req.Slug})
		return nil, fmt.Errorf("check role slug: %w: %w", findErr, organization.ErrOrgInternal)
	}

	role := &model.OrgRole{
		OrgID:       orgID,
		Slug:        req.Slug,
		DisplayName: req.DisplayName,
		IsSystem:    false,
		Permissions: model.PermissionSet(req.Permissions),
	}
	if err := s.repo.CreateRole(ctx, role); err != nil {
		s.logger.ErrorCtx(ctx, "创建角色失败", err, map[string]any{"org_id": orgID, "slug": req.Slug})
		return nil, fmt.Errorf("create role: %w: %w", err, organization.ErrOrgInternal)
	}
	s.logger.InfoCtx(ctx, "自定义角色创建成功", map[string]any{
		"org_id": orgID, "role_id": role.ID, "slug": role.Slug, "n_perms": len(req.Permissions),
	})
	resp := roleToDTO(role)
	return &resp, nil
}

// UpdateCustomRole 修改自定义角色的 display_name 和/或 permissions。
//
// 可能的错误:
//   - ErrRoleNotFound:该 org 下无此 slug 的角色
//   - ErrRoleIsSystem:目标是系统角色(走 UpdateRolePermissions 走系统角色路径)
//   - ErrRoleDisplayNameInvalid:display_name 非法
//   - ErrRolePermissionInvalid / ErrRolePermissionCeilingExceeded:permissions 校验失败
//   - ErrOrgInternal:数据库操作失败
func (s *roleService) UpdateCustomRole(ctx context.Context, orgID uint64, slug string, callerPerms []string, req dto.UpdateRoleRequest) (*dto.RoleResponse, error) {
	role, err := s.loadRoleBySlug(ctx, orgID, slug)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	if role.IsSystem {
		s.logger.WarnCtx(ctx, "不能通过 UpdateCustomRole 修改系统角色,走 /permissions 端点", map[string]any{"org_id": orgID, "slug": slug})
		return nil, fmt.Errorf("role is system: %w", organization.ErrRoleIsSystem)
	}
	// 都没传 → no-op,直接返回当前
	if req.DisplayName == nil && req.Permissions == nil {
		resp := roleToDTO(role)
		return &resp, nil
	}
	if req.DisplayName != nil {
		if *req.DisplayName == "" || len(*req.DisplayName) > organization.MaxRoleDisplayNameLength {
			s.logger.WarnCtx(ctx, "role display_name 非法", map[string]any{"org_id": orgID, "slug": slug})
			return nil, fmt.Errorf("invalid role display name: %w", organization.ErrRoleDisplayNameInvalid)
		}
		if err := s.repo.UpdateRoleDisplayName(ctx, role.ID, *req.DisplayName); err != nil {
			s.logger.ErrorCtx(ctx, "更新角色 display_name 失败", err, map[string]any{"org_id": orgID, "role_id": role.ID})
			return nil, fmt.Errorf("update role display name: %w: %w", err, organization.ErrOrgInternal)
		}
		role.DisplayName = *req.DisplayName
	}
	if req.Permissions != nil {
		if err := validateRolePermissions(ctx, s.logger, *req.Permissions, callerPerms); err != nil {
			//sayso-lint:ignore sentinel-wrap
			return nil, err
		}
		if err := s.repo.UpdateRolePermissions(ctx, role.ID, *req.Permissions); err != nil {
			s.logger.ErrorCtx(ctx, "更新角色 permissions 失败", err, map[string]any{"org_id": orgID, "role_id": role.ID})
			return nil, fmt.Errorf("update role permissions: %w: %w", err, organization.ErrOrgInternal)
		}
		role.Permissions = model.PermissionSet(*req.Permissions)
	}
	s.logger.InfoCtx(ctx, "自定义角色已更新", map[string]any{"org_id": orgID, "role_id": role.ID, "slug": role.Slug})
	resp := roleToDTO(role)
	return &resp, nil
}

// UpdateRolePermissions 见接口注释。
//
// 接受系统角色 + 自定义角色;系统/自定义只用于 audit metadata 区分。
// 仍走 ceiling 校验:role.manage_system 默认只 owner 有,owner 拥有所有 perm,所以总能过;
// 但若 owner 把自己的 permissions 改残,后续可能权限不足 —— 由调用方自负责任(罕见)。
func (s *roleService) UpdateRolePermissions(ctx context.Context, orgID uint64, slug string, callerPerms []string, req dto.UpdateRolePermissionsRequest) (*dto.RoleResponse, error) {
	role, err := s.loadRoleBySlug(ctx, orgID, slug)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	if err := validateRolePermissions(ctx, s.logger, req.Permissions, callerPerms); err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	if err := s.repo.UpdateRolePermissions(ctx, role.ID, req.Permissions); err != nil {
		s.logger.ErrorCtx(ctx, "更新角色 permissions 失败", err, map[string]any{"org_id": orgID, "role_id": role.ID, "is_system": role.IsSystem})
		return nil, fmt.Errorf("update role permissions: %w: %w", err, organization.ErrOrgInternal)
	}
	role.Permissions = model.PermissionSet(req.Permissions)
	s.logger.InfoCtx(ctx, "角色 permissions 已更新", map[string]any{
		"org_id": orgID, "role_id": role.ID, "slug": role.Slug, "is_system": role.IsSystem, "n_perms": len(req.Permissions),
	})
	resp := roleToDTO(role)
	return &resp, nil
}

// DeleteCustomRole 删除一个自定义角色。
//
// 可能的错误:
//   - ErrRoleNotFound:该 org 下无此 slug 的角色
//   - ErrRoleIsSystem:系统角色不可删
//   - ErrRoleHasMembers:仍有成员挂在该角色上
//   - ErrOrgInternal:数据库操作失败
func (s *roleService) DeleteCustomRole(ctx context.Context, orgID uint64, slug string) error {
	role, err := s.loadRoleBySlug(ctx, orgID, slug)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return err
	}
	if role.IsSystem {
		s.logger.WarnCtx(ctx, "不能删除系统角色", map[string]any{"org_id": orgID, "slug": slug})
		return fmt.Errorf("role is system: %w", organization.ErrRoleIsSystem)
	}
	count, err := s.repo.CountMembersByRole(ctx, role.ID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "统计角色下成员失败", err, map[string]any{"org_id": orgID, "role_id": role.ID})
		return fmt.Errorf("count members by role: %w: %w", err, organization.ErrOrgInternal)
	}
	if count > 0 {
		s.logger.WarnCtx(ctx, "角色下仍有成员,不可删除", map[string]any{"org_id": orgID, "role_id": role.ID, "members": count})
		return fmt.Errorf("role has members: %w", organization.ErrRoleHasMembers)
	}
	if err := s.repo.DeleteRole(ctx, role.ID); err != nil {
		s.logger.ErrorCtx(ctx, "删除角色失败", err, map[string]any{"org_id": orgID, "role_id": role.ID})
		return fmt.Errorf("delete role: %w: %w", err, organization.ErrOrgInternal)
	}
	s.logger.InfoCtx(ctx, "自定义角色已删除", map[string]any{"org_id": orgID, "role_id": role.ID, "slug": role.Slug})
	return nil
}

// AssignRoleToMember 修改指定成员的角色。
//
// M5:加权限上限 —— 被分配的 role 的 permissions 必须是 callerPerms 的子集
// (防止 admin 把成员升到带 admin 没有的 perm 的 custom role,实质提权)。
//
// 可能的错误:
//   - ErrOrgNotFound:org 不存在
//   - ErrMemberNotFound:目标不是该 org 成员
//   - ErrRoleNotFound:目标角色不存在
//   - ErrCannotAssignOwnerRole:目标角色是 owner
//   - ErrCannotChangeOwnerRole:目标成员是当前 owner
//   - ErrRolePermissionCeilingExceeded:目标 role permissions 超出 caller 上限
//   - ErrOrgInternal:数据库操作失败
func (s *roleService) AssignRoleToMember(ctx context.Context, orgID, targetUserID uint64, callerPerms []string, req dto.AssignRoleRequest) (*dto.RoleResponse, error) {
	org, err := s.repo.FindOrgByID(ctx, orgID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "org 不存在", map[string]any{"org_id": orgID})
			return nil, fmt.Errorf("find org: %w", organization.ErrOrgNotFound)
		}
		s.logger.ErrorCtx(ctx, "查询 org 失败", err, map[string]any{"org_id": orgID})
		return nil, fmt.Errorf("find org: %w: %w", err, organization.ErrOrgInternal)
	}
	if org.OwnerUserID == targetUserID {
		s.logger.WarnCtx(ctx, "不能修改 owner 的角色", map[string]any{"org_id": orgID, "target": targetUserID})
		return nil, fmt.Errorf("cannot change owner role: %w", organization.ErrCannotChangeOwnerRole)
	}

	// 成员存在性校验
	//sayso-lint:ignore err-swallow
	if _, err := s.repo.FindMember(ctx, orgID, targetUserID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "目标不是成员", map[string]any{"org_id": orgID, "target": targetUserID})
			return nil, fmt.Errorf("find member: %w", organization.ErrMemberNotFound)
		}
		s.logger.ErrorCtx(ctx, "查询成员失败", err, map[string]any{"org_id": orgID, "target": targetUserID})
		return nil, fmt.Errorf("find member: %w: %w", err, organization.ErrOrgInternal)
	}

	// 目标角色存在性校验
	role, err := s.loadRoleBySlug(ctx, orgID, req.RoleSlug)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	if role.Slug == organization.SystemRoleSlugOwner {
		s.logger.WarnCtx(ctx, "不能通过该接口分配 owner 角色", map[string]any{"org_id": orgID, "target": targetUserID})
		return nil, fmt.Errorf("cannot assign owner role: %w", organization.ErrCannotAssignOwnerRole)
	}

	// M5 ceiling:目标 role.permissions 必须是 callerPerms 的子集
	if missing := subtractSet(role.Permissions, callerPerms); len(missing) > 0 {
		s.logger.WarnCtx(ctx, "分配的 role 含 caller 无的 perm,拒绝", map[string]any{
			"org_id": orgID, "target": targetUserID, "role_slug": role.Slug, "missing": missing,
		})
		return nil, fmt.Errorf("ceiling exceeded: %w", organization.ErrRolePermissionCeilingExceeded)
	}

	if err := s.repo.UpdateMemberRole(ctx, orgID, targetUserID, role.ID); err != nil {
		s.logger.ErrorCtx(ctx, "更新成员角色失败", err, map[string]any{"org_id": orgID, "target": targetUserID, "role_id": role.ID})
		return nil, fmt.Errorf("update member role: %w: %w", err, organization.ErrOrgInternal)
	}
	s.logger.InfoCtx(ctx, "成员角色已更新", map[string]any{"org_id": orgID, "target": targetUserID, "role_id": role.ID, "role_slug": role.Slug})
	resp := roleToDTO(role)
	return &resp, nil
}

// validateRolePermissions 检查 perms 列表合法性 + 是否在 caller 上限内。
//
// nil/空 perms 总是合法(空集是任何集合的子集)。
//
// 失败:
//   - 任一 perm 不在 permission.AllPermissions() → ErrRolePermissionInvalid
//   - 存在 callerPerms 中没有的 perm → ErrRolePermissionCeilingExceeded
func validateRolePermissions(ctx context.Context, log logger.LoggerInterface, perms []string, callerPerms []string) error {
	if len(perms) == 0 {
		return nil
	}
	known := permsToSet(permission.AllPermissions())
	caller := permsToSet(callerPerms)
	for _, p := range perms {
		if _, ok := known[p]; !ok {
			log.WarnCtx(ctx, "未知 permission", map[string]any{"perm": p})
			return fmt.Errorf("unknown perm %q: %w", p, organization.ErrRolePermissionInvalid)
		}
		if _, ok := caller[p]; !ok {
			log.WarnCtx(ctx, "perm 超出 caller 上限", map[string]any{"perm": p, "caller_perms": callerPerms})
			return fmt.Errorf("ceiling exceeded for %q: %w", p, organization.ErrRolePermissionCeilingExceeded)
		}
	}
	return nil
}

// subtractSet 返回 a 中存在但 b 中不存在的元素列表(集合差)。
// 用来挑出"目标 role 拥有但 caller 没有"的 perm,生成 audit / 错误信息。
func subtractSet(a []string, b []string) []string {
	if len(a) == 0 {
		return nil
	}
	bSet := permsToSet(b)
	var missing []string
	for _, x := range a {
		if _, ok := bSet[x]; !ok {
			missing = append(missing, x)
		}
	}
	return missing
}

// permsToSet 把 string slice 转为 set 加速 contains 查询。
func permsToSet(s []string) map[string]struct{} {
	m := make(map[string]struct{}, len(s))
	for _, x := range s {
		m[x] = struct{}{}
	}
	return m
}

// loadRoleBySlug 内部工具:按 (org_id, slug) 加载角色,翻译 NotFound 为 ErrRoleNotFound。
func (s *roleService) loadRoleBySlug(ctx context.Context, orgID uint64, slug string) (*model.OrgRole, error) {
	role, err := s.repo.FindRoleByOrgAndSlug(ctx, orgID, slug)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "角色不存在", map[string]any{"org_id": orgID, "slug": slug})
			return nil, fmt.Errorf("find role: %w", organization.ErrRoleNotFound)
		}
		s.logger.ErrorCtx(ctx, "查询角色失败", err, map[string]any{"org_id": orgID, "slug": slug})
		return nil, fmt.Errorf("find role: %w: %w", err, organization.ErrOrgInternal)
	}
	return role, nil
}
