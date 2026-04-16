// role_service.go 角色管理与权限判断 service。
//
// 职责:
//   - 自定义角色 CRUD(owner 独占)
//   - 预设角色只读查询
//   - 权限判断:HasPermission 和 GetMembership,供 agent 模块通过 service 接口调用
//   - 权限点清单查询(前端构建选择面板用)
package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/organization"
	"github.com/eyrihe999-stack/Synapse/internal/organization/dto"
	"github.com/eyrihe999-stack/Synapse/internal/organization/model"
	"github.com/eyrihe999-stack/Synapse/internal/organization/repository"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"gorm.io/gorm"
)

// RoleService 定义角色相关的业务操作。
//sayso-lint:ignore interface-pollution
type RoleService interface {
	// ListRoles 列出某 org 的所有角色。成员即可调用。
	ListRoles(ctx context.Context, orgID uint64) ([]dto.RoleResponse, error)

	// GetRole 按 ID 查角色(必须属于指定 org)。
	GetRole(ctx context.Context, orgID, roleID uint64) (*dto.RoleResponse, error)

	// CreateCustomRole 创建自定义角色。调用方须持有 PermRoleManage(owner 独占)。
	// 权限列表不能包含 OwnerOnlyPermissions。
	CreateCustomRole(ctx context.Context, orgID uint64, req dto.CreateCustomRoleRequest) (*dto.RoleResponse, error)

	// UpdateCustomRole 更新自定义角色(预设角色拒绝)。
	UpdateCustomRole(ctx context.Context, orgID, roleID uint64, req dto.UpdateCustomRoleRequest) (*dto.RoleResponse, error)

	// DeleteCustomRole 删除自定义角色(预设拒绝;有成员引用时返回 ErrRoleInUse)。
	DeleteCustomRole(ctx context.Context, orgID, roleID uint64) error

	// ListPermissions 列出全部权限点清单(系统常量)。
	ListPermissions(ctx context.Context) dto.PermissionsResponse

	// ─── 给 agent 模块(或 handler 中间件)调用的权限查询接口 ──

	// GetMembership 返回用户在指定 org 内的成员关系 + 角色 + 权限快照。
	// 不是成员返回 ErrOrgNotMember;org 不存在返回 ErrOrgNotFound。
	GetMembership(ctx context.Context, orgID, userID uint64) (*Membership, error)

	// HasPermission 判断用户在指定 org 内是否持有某权限点。
	// 不是成员直接返回 (false, ErrOrgNotMember)。
	HasPermission(ctx context.Context, orgID, userID uint64, permission string) (bool, error)
}

// Membership 是 GetMembership 的返回值:成员在 org 内的角色和权限快照。
type Membership struct {
	OrgID       uint64
	UserID      uint64
	RoleID      uint64
	RoleName    string
	IsPreset    bool
	Permissions map[string]struct{}
}

// Has 返回是否持有某权限点。
func (m *Membership) Has(perm string) bool {
	if m == nil {
		return false
	}
	_, ok := m.Permissions[perm]
	return ok
}

// IsOwner 判断当前成员是否是 owner 角色。
func (m *Membership) IsOwner() bool {
	return m != nil && m.RoleName == organization.RoleOwner
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

// ListRoles 列出 org 的所有角色,预设优先、自增 ID 其次。查询失败返回 ErrOrgInternal。
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

// GetRole 按 ID 查询角色并校验属于指定 org。不存在返回 ErrRoleNotFound,内部错误返回 ErrOrgInternal。
func (s *roleService) GetRole(ctx context.Context, orgID, roleID uint64) (*dto.RoleResponse, error) {
	role, err := s.repo.FindRoleByID(ctx, roleID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "角色不存在", map[string]any{"role_id": roleID})
			return nil, fmt.Errorf("find role: %w", organization.ErrRoleNotFound)
		}
		s.logger.ErrorCtx(ctx, "查询角色失败", err, map[string]any{"role_id": roleID})
		return nil, fmt.Errorf("find role: %w: %w", err, organization.ErrOrgInternal)
	}
	if role.OrgID != orgID {
		s.logger.WarnCtx(ctx, "角色不属于指定 org", map[string]any{"role_id": roleID, "org_id": orgID, "role_org": role.OrgID})
		return nil, fmt.Errorf("role not in org: %w", organization.ErrRoleNotFound)
	}
	resp := roleToDTO(role)
	return &resp, nil
}

// CreateCustomRole 创建自定义角色(owner 独占)。校验名称合法、权限点合法、排除 owner 独占权限。
// 可能返回:ErrRoleNameInvalid / ErrRolePermissionEmpty / ErrRolePermissionInvalid / ErrRoleNameTaken / ErrOrgInternal
func (s *roleService) CreateCustomRole(ctx context.Context, orgID uint64, req dto.CreateCustomRoleRequest) (*dto.RoleResponse, error) {
	if err := validateCustomRoleName(req.Name); err != nil {
		s.logger.WarnCtx(ctx, "角色名非法", map[string]any{"name": req.Name, "error": err.Error()})
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	if len(req.Permissions) == 0 {
		s.logger.WarnCtx(ctx, "自定义角色权限为空", map[string]any{"org_id": orgID})
		return nil, fmt.Errorf("role permissions empty: %w", organization.ErrRolePermissionEmpty)
	}
	if err := validateCustomRolePermissions(req.Permissions); err != nil {
		s.logger.WarnCtx(ctx, "角色权限非法", map[string]any{"org_id": orgID, "error": err.Error()})
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}

	// name 冲突检查
	existing, err := s.repo.FindRoleByOrgName(ctx, orgID, req.Name)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		s.logger.ErrorCtx(ctx, "检查角色名冲突失败", err, map[string]any{"org_id": orgID, "name": req.Name})
		return nil, fmt.Errorf("check role name: %w: %w", err, organization.ErrOrgInternal)
	}
	if existing != nil {
		s.logger.WarnCtx(ctx, "角色名已被占用", map[string]any{"org_id": orgID, "name": req.Name})
		return nil, fmt.Errorf("role name taken: %w", organization.ErrRoleNameTaken)
	}

	permsJSON, err := marshalPermissions(req.Permissions)
	if err != nil {
		s.logger.ErrorCtx(ctx, "序列化权限失败", err, map[string]any{"org_id": orgID})
		return nil, fmt.Errorf("marshal perms: %w: %w", err, organization.ErrOrgInternal)
	}

	role := &model.OrgRole{
		OrgID:       orgID,
		Name:        req.Name,
		DisplayName: req.DisplayName,
		IsPreset:    false,
		Permissions: permsJSON,
	}
	if err := s.repo.CreateRole(ctx, role); err != nil {
		s.logger.ErrorCtx(ctx, "创建角色失败", err, map[string]any{"org_id": orgID, "name": req.Name})
		return nil, fmt.Errorf("create role: %w: %w", err, organization.ErrOrgInternal)
	}
	s.logger.InfoCtx(ctx, "自定义角色已创建", map[string]any{"org_id": orgID, "role_id": role.ID, "name": req.Name})
	resp := roleToDTO(role)
	return &resp, nil
}

// UpdateCustomRole 更新自定义角色(预设角色拒绝)。可以修改展示名或权限集合。
// 可能返回:ErrRoleNotFound / ErrRoleNotCustom / ErrRolePermissionInvalid / ErrRolePermissionEmpty / ErrOrgInternal
func (s *roleService) UpdateCustomRole(ctx context.Context, orgID, roleID uint64, req dto.UpdateCustomRoleRequest) (*dto.RoleResponse, error) {
	role, err := s.repo.FindRoleByID(ctx, roleID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "角色不存在", map[string]any{"role_id": roleID})
			return nil, fmt.Errorf("find role: %w", organization.ErrRoleNotFound)
		}
		s.logger.ErrorCtx(ctx, "查询角色失败", err, map[string]any{"role_id": roleID})
		return nil, fmt.Errorf("find role: %w: %w", err, organization.ErrOrgInternal)
	}
	if role.OrgID != orgID {
		s.logger.WarnCtx(ctx, "角色不属于 org", map[string]any{"role_id": roleID, "org_id": orgID})
		return nil, fmt.Errorf("role not in org: %w", organization.ErrRoleNotFound)
	}
	if role.IsPreset {
		s.logger.WarnCtx(ctx, "预设角色不可修改", map[string]any{"role_id": roleID, "name": role.Name})
		return nil, fmt.Errorf("role is preset: %w", organization.ErrRoleNotCustom)
	}

	updates := map[string]any{}
	if req.DisplayName != nil {
		updates["display_name"] = *req.DisplayName
	}
	if req.Permissions != nil {
		if len(*req.Permissions) == 0 {
			s.logger.WarnCtx(ctx, "更新:权限为空", map[string]any{"role_id": roleID})
			return nil, fmt.Errorf("role permissions empty: %w", organization.ErrRolePermissionEmpty)
		}
		if err := validateCustomRolePermissions(*req.Permissions); err != nil {
			s.logger.WarnCtx(ctx, "更新:权限非法", map[string]any{"role_id": roleID, "error": err.Error()})
			//sayso-lint:ignore sentinel-wrap
			return nil, err
		}
		permsJSON, marshalErr := marshalPermissions(*req.Permissions)
		if marshalErr != nil {
			s.logger.ErrorCtx(ctx, "序列化权限失败", marshalErr, map[string]any{"role_id": roleID})
			return nil, fmt.Errorf("marshal perms: %w: %w", marshalErr, organization.ErrOrgInternal)
		}
		updates["permissions"] = permsJSON
	}
	if len(updates) == 0 {
		resp := roleToDTO(role)
		return &resp, nil
	}

	if err := s.repo.UpdateRoleFields(ctx, roleID, updates); err != nil {
		s.logger.ErrorCtx(ctx, "更新角色失败", err, map[string]any{"role_id": roleID})
		return nil, fmt.Errorf("update role: %w: %w", err, organization.ErrOrgInternal)
	}

	// 重新拉取返回最新状态
	updated, err := s.repo.FindRoleByID(ctx, roleID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "重新加载角色失败", err, map[string]any{"role_id": roleID})
		return nil, fmt.Errorf("reload role: %w: %w", err, organization.ErrOrgInternal)
	}
	resp := roleToDTO(updated)
	return &resp, nil
}

// DeleteCustomRole 删除自定义角色。预设角色拒绝,被成员引用的角色拒绝。
// 可能返回:ErrRoleNotFound / ErrRoleNotCustom / ErrRoleInUse / ErrOrgInternal
func (s *roleService) DeleteCustomRole(ctx context.Context, orgID, roleID uint64) error {
	role, err := s.repo.FindRoleByID(ctx, roleID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "角色不存在", map[string]any{"role_id": roleID})
			return fmt.Errorf("find role: %w", organization.ErrRoleNotFound)
		}
		s.logger.ErrorCtx(ctx, "查询角色失败", err, map[string]any{"role_id": roleID})
		return fmt.Errorf("find role: %w: %w", err, organization.ErrOrgInternal)
	}
	if role.OrgID != orgID {
		s.logger.WarnCtx(ctx, "角色不属于 org", map[string]any{"role_id": roleID, "org_id": orgID})
		return fmt.Errorf("role not in org: %w", organization.ErrRoleNotFound)
	}
	if role.IsPreset {
		s.logger.WarnCtx(ctx, "预设角色不可删除", map[string]any{"role_id": roleID, "name": role.Name})
		return fmt.Errorf("role is preset: %w", organization.ErrRoleNotCustom)
	}
	count, err := s.repo.CountMembersByRoleID(ctx, roleID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "统计成员失败", err, map[string]any{"role_id": roleID})
		return fmt.Errorf("count members: %w: %w", err, organization.ErrOrgInternal)
	}
	if count > 0 {
		s.logger.WarnCtx(ctx, "角色正在被使用,拒绝删除", map[string]any{"role_id": roleID, "member_count": count})
		return fmt.Errorf("role in use: %w", organization.ErrRoleInUse)
	}
	if err := s.repo.DeleteRole(ctx, roleID); err != nil {
		s.logger.ErrorCtx(ctx, "删除角色失败", err, map[string]any{"role_id": roleID})
		return fmt.Errorf("delete role: %w: %w", err, organization.ErrOrgInternal)
	}
	s.logger.InfoCtx(ctx, "自定义角色已删除", map[string]any{"role_id": roleID, "org_id": orgID})
	return nil
}

// ListPermissions 返回系统定义的全部权限点。
func (s *roleService) ListPermissions(_ context.Context) dto.PermissionsResponse {
	return dto.PermissionsResponse{
		All:       append([]string{}, organization.AllPermissions...),
		OwnerOnly: append([]string{}, organization.OwnerOnlyPermissions...),
	}
}

// GetMembership 查询用户在 org 内的成员关系和权限快照。
// 非成员返回 ErrOrgNotMember,数据库错误返回 ErrOrgInternal。
func (s *roleService) GetMembership(ctx context.Context, orgID, userID uint64) (*Membership, error) {
	mwr, err := s.repo.FindMemberWithRole(ctx, orgID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "非成员", map[string]any{"org_id": orgID, "user_id": userID})
			return nil, fmt.Errorf("find membership: %w", organization.ErrOrgNotMember)
		}
		s.logger.ErrorCtx(ctx, "查询成员关系失败", err, map[string]any{"org_id": orgID, "user_id": userID})
		return nil, fmt.Errorf("find membership: %w: %w", err, organization.ErrOrgInternal)
	}
	perms := unmarshalPermissions(mwr.Role.Permissions)
	permsSet := make(map[string]struct{}, len(perms))
	for _, p := range perms {
		permsSet[p] = struct{}{}
	}
	return &Membership{
		OrgID:       orgID,
		UserID:      userID,
		RoleID:      mwr.Role.ID,
		RoleName:    mwr.Role.Name,
		IsPreset:    mwr.Role.IsPreset,
		Permissions: permsSet,
	}, nil
}

// HasPermission 判断用户在指定 org 内是否持有某权限点。
// 非成员时返回 (false, ErrOrgNotMember),内部错误返回 ErrOrgInternal。
func (s *roleService) HasPermission(ctx context.Context, orgID, userID uint64, permission string) (bool, error) {
	m, err := s.GetMembership(ctx, orgID, userID)
	if err != nil {
		// GetMembership 已记录日志并包装 sentinel,此处仅透传
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return false, err
	}
	return m.Has(permission), nil
}

// ─── 内部校验工具 ─────────────────────────────────────────────────────────────

// validateCustomRoleName 校验自定义角色的 name:长度 1-32 且不能与预设名冲突。
// 这是纯参数校验工具,调用方会记录失败日志,此处不重复 log。
func validateCustomRoleName(name string) error {
	if name == "" {
		//sayso-lint:ignore log-coverage
		return fmt.Errorf("empty name: %w", organization.ErrRoleNameInvalid)
	}
	if len(name) > organization.MaxRoleNameLength {
		//sayso-lint:ignore log-coverage
		return fmt.Errorf("name too long: %w", organization.ErrRoleNameInvalid)
	}
	switch name {
	case organization.RoleOwner, organization.RoleAdmin, organization.RoleMember:
		//sayso-lint:ignore log-coverage
		return fmt.Errorf("name conflicts with preset: %w", organization.ErrRoleNameInvalid)
	}
	return nil
}

// validateCustomRolePermissions 校验自定义角色的权限列表必须全部在 AllPermissions 内,
// 且不能包含 OwnerOnlyPermissions。纯参数校验,调用方会记录失败日志。
func validateCustomRolePermissions(perms []string) error {
	allSet := make(map[string]struct{}, len(organization.AllPermissions))
	for _, p := range organization.AllPermissions {
		allSet[p] = struct{}{}
	}
	ownerOnly := make(map[string]struct{}, len(organization.OwnerOnlyPermissions))
	for _, p := range organization.OwnerOnlyPermissions {
		ownerOnly[p] = struct{}{}
	}
	for _, p := range perms {
		if _, ok := allSet[p]; !ok {
			//sayso-lint:ignore log-coverage
			return fmt.Errorf("unknown permission %q: %w", p, organization.ErrRolePermissionInvalid)
		}
		if _, ok := ownerOnly[p]; ok {
			//sayso-lint:ignore log-coverage
			return fmt.Errorf("permission %q is owner-only: %w", p, organization.ErrRolePermissionInvalid)
		}
	}
	return nil
}
