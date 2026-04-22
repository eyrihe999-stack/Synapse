// permission_service.go 权限判定 service:回答"用户对资源有什么权限"。
//
// 集中式判定入口 —— document / source / 未来其他模块都通过 PermissionService 拿权限,
// 而不是各自重复实现 owner-check / visibility-check / ACL-check 逻辑。
//
// M3 阶段只覆盖 source 资源(三档:none/read/write):
//   - owner       → write(隐式)
//   - org 可见    → read(对所有 org 成员)
//   - group 可见  → 按 ACL 表查;ACL 命中级别决定 read/write
//   - private     → none(除 owner 外)
//
// 跨模块依赖通过 SourceLookup 接口注入,避免直接 import source 包。
package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/permission"
	"github.com/eyrihe999-stack/Synapse/internal/permission/model"
	"github.com/eyrihe999-stack/Synapse/internal/permission/repository"
	"gorm.io/gorm"
)

// SourceLookup 跨模块读接口:permission 判定 service 通过它读 source 元信息。
//
// main.go 用 source 模块的 repo 做适配器实现注入。nil 注入则 PermOnSource / VisibleSourceIDsInOrg
// 不可用(用 panic 兜也行,但这里返 ErrPermInternal,让上层吞错)。
type SourceLookup interface {
	// GetSource 取 source 用于判定,只取四个关键字段(避免传输无关数据)。
	GetSource(ctx context.Context, sourceID uint64) (*SourceInfo, error)

	// ListSourceIDsByOwner 列某 user 持有的 source id(全部 owned 隐式 write)。
	ListSourceIDsByOwner(ctx context.Context, orgID, ownerUserID uint64) ([]uint64, error)

	// ListSourceIDsByVisibility 列某 visibility 的 source id(用于"全 org 可见")。
	ListSourceIDsByVisibility(ctx context.Context, orgID uint64, visibility string) ([]uint64, error)
}

// OrgRoleLookup 跨模块读接口:permission 判定 service 通过它读"某 user 在某 org 的角色 + 权限位"。
//
// main.go 用 org 模块的 repo 做适配器注入。
type OrgRoleLookup interface {
	// GetMemberRole 返回某 user 在某 org 的角色信息(含 permissions)。
	// 不是成员 → 返 (nil, nil)(error 也为 nil),调用方按 nil 当 forbidden 处理。
	GetMemberRole(ctx context.Context, orgID, userID uint64) (*MemberRoleInfo, error)
}

// MemberRoleInfo permission 判定需要的 (member→role) 元信息。
type MemberRoleInfo struct {
	RoleID      uint64
	Slug        string
	IsSystem    bool
	Permissions []string
}

// SourceInfo permission 判定需要的 source 元信息。
type SourceInfo struct {
	ID          uint64
	OrgID       uint64
	OwnerUserID uint64
	Visibility  string // 'org' | 'group' | 'private'
}

// Source visibility 字符串常量 —— 与 source/model 包同名常量保持一致;
// permission 模块自带一份避免反向 import source 包。
// 调用方(source 模块的 SourceLookup adapter)负责保证两侧值一致。
const (
	SourceVisibilityOrg     = "org"
	SourceVisibilityGroup   = "group"
	SourceVisibilityPrivate = "private"
)

// PermissionService 权限判定的对外门面。
//
//sayso-lint:ignore interface-pollution
type PermissionService interface {
	// PermOnSource 返回 user 对 source 的权限级别("write" / "read" / "none")。
	//
	// 可能的错误:
	//   - ErrPermForbidden:source 不属于 user 所在的 org(跨 org 越界 → 视作 forbidden)
	//   - ErrPermInternal:DB 失败 / SourceLookup 报错
	PermOnSource(ctx context.Context, userID, sourceID uint64) (string, error)

	// VisibleSourceIDsInOrg 返回 user 在某 org 内可见(满足 minPerm)的所有 source id。
	//
	// minPerm:'read' = 任何可见的 source(owner / org-visible / ACL hit);
	//          'write' = 只算 owner + ACL write 行(visibility='org' 不给非 owner 写权)。
	//
	// 返回 distinct 列表;空表示该 user 在该 org 看不到任何 source(空列表也是合法答案)。
	VisibleSourceIDsInOrg(ctx context.Context, orgID, userID uint64, minPerm string) ([]uint64, error)

	// GroupsOfUser 返回 user 在某 org 加入的所有 group id。
	// PermContextMiddleware 用,塞 ctx 后续判定零打 DB。
	GroupsOfUser(ctx context.Context, orgID, userID uint64) ([]uint64, error)

	// ─── M4 操作权限(RBAC) ─────────────────────────────────────────────────

	// HasOrgPermission 判断 user 在某 org 是否拥有某 perm。
	// 走 user→member→role→permissions 链。
	//
	// 优先从 ctx 取(PermContextMiddleware 已缓存),没有再打 DB。
	//
	// 不是 org 成员 → 返 (false, nil)。
	HasOrgPermission(ctx context.Context, orgID, userID uint64, perm string) (bool, error)

	// PermissionsOfUser 返回 user 在某 org 的权限位列表(按 role 取)。
	// PermContextMiddleware 用,塞 ctx 后续 RequirePerm 零打 DB。
	// 不是成员 → 返 (nil, nil)。
	PermissionsOfUser(ctx context.Context, orgID, userID uint64) ([]string, error)
}

// permissionService PermissionService 的默认实现。
type permissionService struct {
	repo       repository.Repository
	sourceLook SourceLookup
	roleLook   OrgRoleLookup
	logger     logger.LoggerInterface
}

// NewPermissionService 构造一个 PermissionService。sourceLookup / roleLookup 都必填。
func NewPermissionService(
	repo repository.Repository,
	sourceLookup SourceLookup,
	roleLookup OrgRoleLookup,
	log logger.LoggerInterface,
) PermissionService {
	return &permissionService{
		repo:       repo,
		sourceLook: sourceLookup,
		roleLook:   roleLookup,
		logger:     log,
	}
}

// PermOnSource 对单个 source 做权限判定。
func (s *permissionService) PermOnSource(ctx context.Context, userID, sourceID uint64) (string, error) {
	src, err := s.sourceLook.GetSource(ctx, sourceID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "source 不存在,perm=none", map[string]any{"source_id": sourceID, "user_id": userID})
			return PermNone, nil
		}
		s.logger.ErrorCtx(ctx, "查 source 失败", err, map[string]any{"source_id": sourceID})
		return PermNone, fmt.Errorf("lookup source: %w: %w", err, permission.ErrPermInternal)
	}

	// owner 隐式 write
	if src.OwnerUserID == userID {
		return PermWrite, nil
	}

	switch src.Visibility {
	case SourceVisibilityOrg:
		return PermRead, nil
	case SourceVisibilityPrivate:
		return PermNone, nil
	case SourceVisibilityGroup:
		// 走 ACL 表;命中最高 permission
		groupIDs, err := s.GroupsOfUser(ctx, src.OrgID, userID)
		if err != nil {
			//sayso-lint:ignore sentinel-wrap
			return PermNone, err
		}
		subjects := buildSubjects(groupIDs, userID)
		ids, err := s.repo.ListVisibleResourceIDsBySubjects(ctx, src.OrgID, model.ACLResourceTypeSource, subjects, model.ACLPermWrite)
		if err != nil {
			s.logger.ErrorCtx(ctx, "查 ACL write 失败", err, map[string]any{"source_id": sourceID})
			return PermNone, fmt.Errorf("acl write check: %w: %w", err, permission.ErrPermInternal)
		}
		if containsID(ids, sourceID) {
			return PermWrite, nil
		}
		// write 没命中,降级查 read
		readIDs, err := s.repo.ListVisibleResourceIDsBySubjects(ctx, src.OrgID, model.ACLResourceTypeSource, subjects, model.ACLPermRead)
		if err != nil {
			s.logger.ErrorCtx(ctx, "查 ACL read 失败", err, map[string]any{"source_id": sourceID})
			return PermNone, fmt.Errorf("acl read check: %w: %w", err, permission.ErrPermInternal)
		}
		if containsID(readIDs, sourceID) {
			return PermRead, nil
		}
		return PermNone, nil
	default:
		s.logger.WarnCtx(ctx, "source 出现未知 visibility,按 none 处理", map[string]any{"source_id": sourceID, "visibility": src.Visibility})
		return PermNone, nil
	}
}

// VisibleSourceIDsInOrg 列出 user 在某 org 中可见的所有 source id(满足 minPerm)。
//
// 实现:三段 SQL 取并集去重。
//  1. user 持有的 source(owner 隐式 write)
//  2. 仅 minPerm=read 时:visibility='org' 的所有 source
//  3. ACL 命中:subjects = (groups_of_U + (user, U)),按 minPerm 过滤
func (s *permissionService) VisibleSourceIDsInOrg(ctx context.Context, orgID, userID uint64, minPerm string) ([]uint64, error) {
	if !model.IsValidACLPermission(minPerm) {
		s.logger.WarnCtx(ctx, "VisibleSourceIDs minPerm 非法", map[string]any{"min": minPerm})
		return nil, fmt.Errorf("invalid min permission: %w", permission.ErrACLInvalidPermission)
	}

	idSet := make(map[uint64]struct{})

	// 1. owner-implicit
	ownedIDs, err := s.sourceLook.ListSourceIDsByOwner(ctx, orgID, userID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "查 owned source ids 失败", err, map[string]any{"org_id": orgID, "user_id": userID})
		return nil, fmt.Errorf("list owned: %w: %w", err, permission.ErrPermInternal)
	}
	for _, id := range ownedIDs {
		idSet[id] = struct{}{}
	}

	// 2. visibility='org' 仅对 read 算可见
	if minPerm == model.ACLPermRead {
		orgVisIDs, err := s.sourceLook.ListSourceIDsByVisibility(ctx, orgID, SourceVisibilityOrg)
		if err != nil {
			s.logger.ErrorCtx(ctx, "查 org-visible source ids 失败", err, map[string]any{"org_id": orgID})
			return nil, fmt.Errorf("list org-visible: %w: %w", err, permission.ErrPermInternal)
		}
		for _, id := range orgVisIDs {
			idSet[id] = struct{}{}
		}
	}

	// 3. ACL 命中
	groupIDs, err := s.GroupsOfUser(ctx, orgID, userID)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	subjects := buildSubjects(groupIDs, userID)
	if len(subjects) > 0 {
		aclIDs, err := s.repo.ListVisibleResourceIDsBySubjects(ctx, orgID, model.ACLResourceTypeSource, subjects, minPerm)
		if err != nil {
			s.logger.ErrorCtx(ctx, "查 ACL visible source ids 失败", err, map[string]any{"org_id": orgID, "user_id": userID})
			return nil, fmt.Errorf("list acl visible: %w: %w", err, permission.ErrPermInternal)
		}
		for _, id := range aclIDs {
			idSet[id] = struct{}{}
		}
	}

	out := make([]uint64, 0, len(idSet))
	for id := range idSet {
		out = append(out, id)
	}
	return out, nil
}

// GroupsOfUser 优先从 ctx 取(PermContextMiddleware 已缓存),没有再打 DB。
func (s *permissionService) GroupsOfUser(ctx context.Context, orgID, userID uint64) ([]uint64, error) {
	if cached, ok := groupsFromContext(ctx, orgID); ok {
		return cached, nil
	}
	ids, err := s.repo.ListGroupIDsByUser(ctx, orgID, userID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "查 user 的 group ids 失败", err, map[string]any{"org_id": orgID, "user_id": userID})
		return nil, fmt.Errorf("list group ids: %w: %w", err, permission.ErrPermInternal)
	}
	return ids, nil
}

// HasOrgPermission 见接口注释。
func (s *permissionService) HasOrgPermission(ctx context.Context, orgID, userID uint64, perm string) (bool, error) {
	perms, err := s.PermissionsOfUser(ctx, orgID, userID)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return false, err
	}
	for _, p := range perms {
		if p == perm {
			return true, nil
		}
	}
	return false, nil
}

// PermissionsOfUser 优先从 ctx 取(PermContextMiddleware 缓存的 role permissions),
// 没有再打 DB(走 OrgRoleLookup 拿 role+perms)。
func (s *permissionService) PermissionsOfUser(ctx context.Context, orgID, userID uint64) ([]string, error) {
	if cached, ok := permissionsFromContext(ctx, orgID); ok {
		return cached, nil
	}
	info, err := s.roleLook.GetMemberRole(ctx, orgID, userID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "查 user 的 org role 失败", err, map[string]any{"org_id": orgID, "user_id": userID})
		return nil, fmt.Errorf("lookup org role: %w: %w", err, permission.ErrPermInternal)
	}
	if info == nil {
		return nil, nil
	}
	return info.Permissions, nil
}

// ─── 内部工具 ────────────────────────────────────────────────────────────────

// buildSubjects 把 (user, groups) 摊成 ACL subject 列表。
func buildSubjects(groupIDs []uint64, userID uint64) []repository.ACLSubject {
	out := make([]repository.ACLSubject, 0, len(groupIDs)+1)
	for _, gid := range groupIDs {
		out = append(out, repository.ACLSubject{Type: model.ACLSubjectTypeGroup, ID: gid})
	}
	if userID != 0 {
		out = append(out, repository.ACLSubject{Type: model.ACLSubjectTypeUser, ID: userID})
	}
	return out
}

// containsID 在小切片里线性查 id。源列表通常 < 100,无需 map 化。
func containsID(ids []uint64, id uint64) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// ─── 权限级别常量 ────────────────────────────────────────────────────────────
// 与 model.ACLPerm* 含义一致,这里多复制一份字符串避免 service 调用方反向 import model
// 包(常用法是字符串比较,常量位于 service 包更顺手)。

const (
	PermNone  = ""
	PermRead  = "read"
	PermWrite = "write"
)
