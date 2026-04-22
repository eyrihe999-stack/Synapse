// group_service.go 权限组管理 service。
//
// 职责:
//   - org 内权限组 CRUD(任何成员可建组、组 owner 可改名/删组/管成员)
//   - 列出某 org 的所有组、列出"我加入的组"
//   - 组成员的加入 / 移除 / 列表
//
// M1 阶段不接 RBAC 权限位检查 —— 任何 org 成员均可建组。组 owner-only 操作
// (改名 / 删组 / 加减成员)由 service 层硬规则校验。
//
// 关键硬规则:
//   - 组 owner 不能被踢出自己的组(必须先转让或删组;M1 不支持转让 → 只能删组)
//   - 加成员时目标 user 必须是该 org 的成员(跨模块校验,通过 OrgMembershipChecker)
//   - 同 org 内组名唯一;同组内 user 不重复
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/permission"
	"github.com/eyrihe999-stack/Synapse/internal/permission/dto"
	"github.com/eyrihe999-stack/Synapse/internal/permission/model"
	"github.com/eyrihe999-stack/Synapse/internal/permission/repository"
	"gorm.io/gorm"
)

// GroupService 定义权限组管理的业务操作。
//
//sayso-lint:ignore interface-pollution
type GroupService interface {
	// CreateGroup 创建一个权限组(callerUserID 自动成为组 owner 并自动加入成员)。
	//
	// 可能的错误:
	//   - ErrGroupNameInvalid:name 为空 / 仅空白 / 超长
	//   - ErrGroupNameTaken:同 org 已有同名组
	//   - ErrMaxGroupsReached:超出单 org 组数上限
	//   - ErrPermInternal:数据库操作失败
	CreateGroup(ctx context.Context, orgID, callerUserID uint64, req dto.CreateGroupRequest) (*dto.GroupResponse, error)

	// GetGroup 查单个组(必须属于该 org)。
	//
	// 可能的错误:
	//   - ErrGroupNotFound:组不存在或不属于该 org
	//   - ErrPermInternal:数据库操作失败
	GetGroup(ctx context.Context, orgID, groupID uint64) (*dto.GroupResponse, error)

	// ListGroups 分页列出某 org 下的所有组。
	ListGroups(ctx context.Context, orgID uint64, page, size int) (*dto.ListGroupsResponse, error)

	// ListMyGroups 列出某 user 在某 org 中加入的所有组(不分页)。
	ListMyGroups(ctx context.Context, orgID, userID uint64) ([]dto.GroupResponse, error)

	// UpdateGroup 改组(目前仅支持改名)。仅组 owner 可调用。
	//
	// 可能的错误:
	//   - ErrGroupNotFound / ErrPermForbidden / ErrGroupNameInvalid /
	//     ErrGroupNameTaken / ErrPermInternal
	UpdateGroup(ctx context.Context, orgID, groupID, callerUserID uint64, req dto.UpdateGroupRequest) (*dto.GroupResponse, error)

	// DeleteGroup 删组(级联删成员)。仅组 owner 可调用。
	//
	// 可能的错误:
	//   - ErrGroupNotFound / ErrPermForbidden / ErrPermInternal
	DeleteGroup(ctx context.Context, orgID, groupID, callerUserID uint64) error

	// AddMember 把目标 user 加入组。仅组 owner 可调用;目标 user 必须是 org 成员。
	//
	// 可能的错误:
	//   - ErrGroupNotFound / ErrPermForbidden / ErrUserNotOrgMember /
	//     ErrGroupMemberExists / ErrMaxMembersInGroup / ErrPermInternal
	AddMember(ctx context.Context, orgID, groupID, callerUserID, targetUserID uint64) error

	// RemoveMember 把目标 user 从组中移除。
	// 调用方:组 owner(可踢任何人,除了自己) 或 用户本人(自己退出);
	// 不允许把组 owner 移出。
	//
	// 可能的错误:
	//   - ErrGroupNotFound / ErrPermForbidden / ErrCannotRemoveGroupOwner /
	//     ErrGroupMemberNotFound / ErrPermInternal
	RemoveMember(ctx context.Context, orgID, groupID, callerUserID, targetUserID uint64) error

	// ListMembers 分页列出某组的成员。任何 org 成员可查。
	ListMembers(ctx context.Context, orgID, groupID uint64, page, size int) (*dto.ListGroupMembersResponse, error)
}

// ─── 实现 ────────────────────────────────────────────────────────────────────

type groupService struct {
	repo       repository.Repository
	orgChecker OrgMembershipChecker // nil 时跳过跨模块校验
	logger     logger.LoggerInterface
}

// NewGroupService 构造一个 GroupService 实例。
func NewGroupService(repo repository.Repository, orgChecker OrgMembershipChecker, log logger.LoggerInterface) GroupService {
	return &groupService{repo: repo, orgChecker: orgChecker, logger: log}
}

// CreateGroup 创建一个权限组,callerUserID 自动成为 owner 且自动加入成员表。
func (s *groupService) CreateGroup(ctx context.Context, orgID, callerUserID uint64, req dto.CreateGroupRequest) (*dto.GroupResponse, error) {
	name, err := normalizeGroupName(req.Name)
	if err != nil {
		s.logger.WarnCtx(ctx, "组名非法", map[string]any{"org_id": orgID, "name": req.Name})
		return nil, err
	}

	// 上限校验
	count, err := s.repo.CountGroupsByOrg(ctx, orgID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "统计组数失败", err, map[string]any{"org_id": orgID})
		return nil, fmt.Errorf("count groups: %w: %w", err, permission.ErrPermInternal)
	}
	if int(count) >= permission.MaxGroupsPerOrg {
		s.logger.WarnCtx(ctx, "超出单 org 组数上限", map[string]any{"org_id": orgID, "count": count, "max": permission.MaxGroupsPerOrg})
		return nil, fmt.Errorf("max groups reached: %w", permission.ErrMaxGroupsReached)
	}

	// 重名预检(DB 唯一索引兜底)
	if existing, findErr := s.repo.FindGroupByOrgAndName(ctx, orgID, name); findErr == nil && existing != nil {
		s.logger.WarnCtx(ctx, "组名已被占用", map[string]any{"org_id": orgID, "name": name})
		return nil, fmt.Errorf("group name taken: %w", permission.ErrGroupNameTaken)
	} else if findErr != nil && !errors.Is(findErr, gorm.ErrRecordNotFound) {
		s.logger.ErrorCtx(ctx, "查组名失败", findErr, map[string]any{"org_id": orgID, "name": name})
		return nil, fmt.Errorf("check group name: %w: %w", findErr, permission.ErrPermInternal)
	}

	g := &model.Group{
		OrgID:       orgID,
		Name:        name,
		OwnerUserID: callerUserID,
	}

	// 在事务里建组 + 自动加 owner 自己进成员表(用 WithTx 让两段共享同一 tx 与 audit)
	err = s.repo.WithTx(ctx, func(tx repository.Repository) error {
		if err := tx.CreateGroup(ctx, g); err != nil {
			return err
		}
		if err := tx.AddGroupMember(ctx, g.ID, callerUserID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		s.logger.ErrorCtx(ctx, "创建组失败", err, map[string]any{"org_id": orgID, "owner": callerUserID})
		return nil, fmt.Errorf("create group: %w: %w", err, permission.ErrPermInternal)
	}
	s.logger.InfoCtx(ctx, "权限组创建成功", map[string]any{"org_id": orgID, "group_id": g.ID, "name": g.Name})

	resp := groupToDTO(g, 1) // owner 已加入,member_count = 1
	return &resp, nil
}

// GetGroup 查单个组。
func (s *groupService) GetGroup(ctx context.Context, orgID, groupID uint64) (*dto.GroupResponse, error) {
	g, err := s.loadGroup(ctx, orgID, groupID)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	count, err := s.repo.CountGroupMembers(ctx, groupID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "统计组成员数失败", err, map[string]any{"group_id": groupID})
		return nil, fmt.Errorf("count members: %w: %w", err, permission.ErrPermInternal)
	}
	resp := groupToDTO(g, count)
	return &resp, nil
}

// ListGroups 分页列 org 下所有组。member_count 通过 N+1 查询填充
// (单 org 默认上限 200 组,可接受;有性能问题再换 GROUP BY 单 SQL)。
func (s *groupService) ListGroups(ctx context.Context, orgID uint64, page, size int) (*dto.ListGroupsResponse, error) {
	page, size = normalizePaging(page, size)
	items, total, err := s.repo.ListGroupsByOrg(ctx, orgID, page, size)
	if err != nil {
		s.logger.ErrorCtx(ctx, "列出 org 组失败", err, map[string]any{"org_id": orgID})
		return nil, fmt.Errorf("list groups: %w: %w", err, permission.ErrPermInternal)
	}
	out := make([]dto.GroupResponse, 0, len(items))
	for _, g := range items {
		count, err := s.repo.CountGroupMembers(ctx, g.ID)
		if err != nil {
			s.logger.ErrorCtx(ctx, "统计组成员数失败", err, map[string]any{"group_id": g.ID})
			return nil, fmt.Errorf("count members: %w: %w", err, permission.ErrPermInternal)
		}
		out = append(out, groupToDTO(g, count))
	}
	return &dto.ListGroupsResponse{
		Items: out,
		Total: total,
		Page:  page,
		Size:  size,
	}, nil
}

// ListMyGroups 列出某 user 在某 org 中加入的所有组。
func (s *groupService) ListMyGroups(ctx context.Context, orgID, userID uint64) ([]dto.GroupResponse, error) {
	items, err := s.repo.ListGroupsByUser(ctx, orgID, userID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "列出我加入的组失败", err, map[string]any{"org_id": orgID, "user_id": userID})
		return nil, fmt.Errorf("list my groups: %w: %w", err, permission.ErrPermInternal)
	}
	out := make([]dto.GroupResponse, 0, len(items))
	for _, g := range items {
		count, err := s.repo.CountGroupMembers(ctx, g.ID)
		if err != nil {
			s.logger.ErrorCtx(ctx, "统计组成员数失败", err, map[string]any{"group_id": g.ID})
			return nil, fmt.Errorf("count members: %w: %w", err, permission.ErrPermInternal)
		}
		out = append(out, groupToDTO(g, count))
	}
	return out, nil
}

// UpdateGroup 改组名。仅组 owner 可调用。
func (s *groupService) UpdateGroup(ctx context.Context, orgID, groupID, callerUserID uint64, req dto.UpdateGroupRequest) (*dto.GroupResponse, error) {
	g, err := s.loadGroup(ctx, orgID, groupID)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	if g.OwnerUserID != callerUserID {
		s.logger.WarnCtx(ctx, "非组 owner 尝试修改组", map[string]any{"group_id": groupID, "caller": callerUserID, "owner": g.OwnerUserID})
		return nil, fmt.Errorf("only group owner can update: %w", permission.ErrPermForbidden)
	}

	if req.Name != nil {
		newName, err := normalizeGroupName(*req.Name)
		if err != nil {
			s.logger.WarnCtx(ctx, "新组名非法", map[string]any{"group_id": groupID})
			return nil, err
		}
		if newName != g.Name {
			// 重名预检
			if existing, findErr := s.repo.FindGroupByOrgAndName(ctx, orgID, newName); findErr == nil && existing != nil && existing.ID != groupID {
				s.logger.WarnCtx(ctx, "新组名已被占用", map[string]any{"org_id": orgID, "name": newName})
				return nil, fmt.Errorf("group name taken: %w", permission.ErrGroupNameTaken)
			} else if findErr != nil && !errors.Is(findErr, gorm.ErrRecordNotFound) {
				s.logger.ErrorCtx(ctx, "查组名失败", findErr, map[string]any{"org_id": orgID, "name": newName})
				return nil, fmt.Errorf("check group name: %w: %w", findErr, permission.ErrPermInternal)
			}
			if err := s.repo.UpdateGroupName(ctx, groupID, newName); err != nil {
				s.logger.ErrorCtx(ctx, "更新组名失败", err, map[string]any{"group_id": groupID})
				return nil, fmt.Errorf("update group name: %w: %w", err, permission.ErrPermInternal)
			}
			g.Name = newName
			s.logger.InfoCtx(ctx, "权限组改名成功", map[string]any{"group_id": groupID, "new_name": newName})
		}
	}

	count, err := s.repo.CountGroupMembers(ctx, groupID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "统计组成员数失败", err, map[string]any{"group_id": groupID})
		return nil, fmt.Errorf("count members: %w: %w", err, permission.ErrPermInternal)
	}
	resp := groupToDTO(g, count)
	return &resp, nil
}

// DeleteGroup 删组。仅组 owner 可调用。
func (s *groupService) DeleteGroup(ctx context.Context, orgID, groupID, callerUserID uint64) error {
	g, err := s.loadGroup(ctx, orgID, groupID)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return err
	}
	if g.OwnerUserID != callerUserID {
		s.logger.WarnCtx(ctx, "非组 owner 尝试删组", map[string]any{"group_id": groupID, "caller": callerUserID, "owner": g.OwnerUserID})
		return fmt.Errorf("only group owner can delete: %w", permission.ErrPermForbidden)
	}
	if err := s.repo.DeleteGroup(ctx, groupID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("group not found: %w", permission.ErrGroupNotFound)
		}
		s.logger.ErrorCtx(ctx, "删除权限组失败", err, map[string]any{"group_id": groupID})
		return fmt.Errorf("delete group: %w: %w", err, permission.ErrPermInternal)
	}
	s.logger.InfoCtx(ctx, "权限组已删除", map[string]any{"group_id": groupID, "name": g.Name})
	return nil
}

// AddMember 把目标 user 加入组。仅组 owner 可调用;目标 user 必须是 org 成员。
func (s *groupService) AddMember(ctx context.Context, orgID, groupID, callerUserID, targetUserID uint64) error {
	g, err := s.loadGroup(ctx, orgID, groupID)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return err
	}
	if g.OwnerUserID != callerUserID {
		s.logger.WarnCtx(ctx, "非组 owner 尝试加成员", map[string]any{"group_id": groupID, "caller": callerUserID, "owner": g.OwnerUserID})
		return fmt.Errorf("only group owner can add members: %w", permission.ErrPermForbidden)
	}

	// 跨模块校验:目标 user 必须是该 org 成员
	if s.orgChecker != nil {
		ok, err := s.orgChecker.IsMember(ctx, orgID, targetUserID)
		if err != nil {
			s.logger.ErrorCtx(ctx, "校验 org 成员关系失败", err, map[string]any{"org_id": orgID, "target": targetUserID})
			return fmt.Errorf("check org membership: %w: %w", err, permission.ErrPermInternal)
		}
		if !ok {
			s.logger.WarnCtx(ctx, "目标不是 org 成员,不可加入组", map[string]any{"org_id": orgID, "target": targetUserID})
			return fmt.Errorf("target user not org member: %w", permission.ErrUserNotOrgMember)
		}
	}

	// 上限校验
	count, err := s.repo.CountGroupMembers(ctx, groupID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "统计组成员数失败", err, map[string]any{"group_id": groupID})
		return fmt.Errorf("count members: %w: %w", err, permission.ErrPermInternal)
	}
	if int(count) >= permission.MaxMembersPerGroup {
		s.logger.WarnCtx(ctx, "超出组成员上限", map[string]any{"group_id": groupID, "count": count, "max": permission.MaxMembersPerGroup})
		return fmt.Errorf("max members in group: %w", permission.ErrMaxMembersInGroup)
	}

	// 重复预检(主键冲突兜底)
	exists, err := s.repo.IsGroupMember(ctx, groupID, targetUserID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "查组成员失败", err, map[string]any{"group_id": groupID, "target": targetUserID})
		return fmt.Errorf("check group member: %w: %w", err, permission.ErrPermInternal)
	}
	if exists {
		s.logger.WarnCtx(ctx, "用户已在组中", map[string]any{"group_id": groupID, "target": targetUserID})
		return fmt.Errorf("member exists: %w", permission.ErrGroupMemberExists)
	}

	if err := s.repo.AddGroupMember(ctx, groupID, targetUserID); err != nil {
		s.logger.ErrorCtx(ctx, "加成员失败", err, map[string]any{"group_id": groupID, "target": targetUserID})
		return fmt.Errorf("add member: %w: %w", err, permission.ErrPermInternal)
	}
	s.logger.InfoCtx(ctx, "成员已加入组", map[string]any{"group_id": groupID, "user_id": targetUserID})
	return nil
}

// RemoveMember 把目标 user 从组中移除。
//
// 调用方授权规则:
//   - 组 owner:可踢任何成员,但不能踢自己
//   - 非 owner 调用者:只能踢自己(自我退出)
func (s *groupService) RemoveMember(ctx context.Context, orgID, groupID, callerUserID, targetUserID uint64) error {
	g, err := s.loadGroup(ctx, orgID, groupID)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return err
	}
	if targetUserID == g.OwnerUserID {
		s.logger.WarnCtx(ctx, "不能把组 owner 移出组", map[string]any{"group_id": groupID, "owner": g.OwnerUserID})
		return fmt.Errorf("cannot remove group owner: %w", permission.ErrCannotRemoveGroupOwner)
	}
	isOwner := g.OwnerUserID == callerUserID
	isSelf := callerUserID == targetUserID
	if !isOwner && !isSelf {
		s.logger.WarnCtx(ctx, "无权移除该成员", map[string]any{"group_id": groupID, "caller": callerUserID, "target": targetUserID})
		return fmt.Errorf("only group owner or self: %w", permission.ErrPermForbidden)
	}

	if err := s.repo.RemoveGroupMember(ctx, groupID, targetUserID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "目标不是组成员", map[string]any{"group_id": groupID, "target": targetUserID})
			return fmt.Errorf("group member not found: %w", permission.ErrGroupMemberNotFound)
		}
		s.logger.ErrorCtx(ctx, "移除成员失败", err, map[string]any{"group_id": groupID, "target": targetUserID})
		return fmt.Errorf("remove member: %w: %w", err, permission.ErrPermInternal)
	}
	s.logger.InfoCtx(ctx, "成员已从组移除", map[string]any{"group_id": groupID, "user_id": targetUserID})
	return nil
}

// ListMembers 分页列出某组的成员。任何 org 成员可查(M1 不限组内可见性)。
func (s *groupService) ListMembers(ctx context.Context, orgID, groupID uint64, page, size int) (*dto.ListGroupMembersResponse, error) {
	if _, err := s.loadGroup(ctx, orgID, groupID); err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	page, size = normalizePaging(page, size)
	items, total, err := s.repo.ListGroupMembers(ctx, groupID, page, size)
	if err != nil {
		s.logger.ErrorCtx(ctx, "列组成员失败", err, map[string]any{"group_id": groupID})
		return nil, fmt.Errorf("list group members: %w: %w", err, permission.ErrPermInternal)
	}
	out := make([]dto.GroupMemberResponse, 0, len(items))
	for _, m := range items {
		out = append(out, memberToDTO(m))
	}
	return &dto.ListGroupMembersResponse{
		Items: out,
		Total: total,
		Page:  page,
		Size:  size,
	}, nil
}

// ─── 内部工具 ────────────────────────────────────────────────────────────────

// loadGroup 按 (org_id, group_id) 加载组,翻译 NotFound 为 ErrGroupNotFound。
// 若组存在但 org_id 不匹配也视为 NotFound(防止跨 org 越界)。
func (s *groupService) loadGroup(ctx context.Context, orgID, groupID uint64) (*model.Group, error) {
	g, err := s.repo.FindGroupByID(ctx, groupID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "权限组不存在", map[string]any{"group_id": groupID})
			return nil, fmt.Errorf("find group: %w", permission.ErrGroupNotFound)
		}
		s.logger.ErrorCtx(ctx, "查权限组失败", err, map[string]any{"group_id": groupID})
		return nil, fmt.Errorf("find group: %w: %w", err, permission.ErrPermInternal)
	}
	if g.OrgID != orgID {
		s.logger.WarnCtx(ctx, "组不属于该 org", map[string]any{"group_id": groupID, "wanted_org": orgID, "actual_org": g.OrgID})
		return nil, fmt.Errorf("cross-org access: %w", permission.ErrGroupNotFound)
	}
	return g, nil
}

// normalizeGroupName 校验并标准化组名(去首尾空白、查长度)。
func normalizeGroupName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", fmt.Errorf("group name empty: %w", permission.ErrGroupNameInvalid)
	}
	if len(name) > permission.MaxGroupNameLength {
		return "", fmt.Errorf("group name too long: %w", permission.ErrGroupNameInvalid)
	}
	return name, nil
}

// normalizePaging 钳位 page / size 到合法区间。
func normalizePaging(page, size int) (int, int) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = permission.DefaultPageSize
	}
	if size > permission.MaxPageSize {
		size = permission.MaxPageSize
	}
	return page, size
}
