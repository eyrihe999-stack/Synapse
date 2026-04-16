// member_service.go 成员管理 service。
//
// 职责:
//   - 列成员(分页)
//   - 踢出成员(需要 PermMemberRemove,不能踢 owner)
//   - 主动退出(owner 拒绝)
//   - 变更成员角色(需要 PermMemberRoleAssign)
//   - 内部工具:所有权转让的角色交接事务
//
// 移除成员后会调用 HookRegistry.FireMemberRemoved 触发 agent 模块的级联清理。
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

// MemberService 定义成员管理的业务操作。
type MemberService interface {
	// ListMembers 分页列出 org 的所有成员。调用方须是 org 成员。
	ListMembers(ctx context.Context, orgID uint64, page, size int) (*dto.ListMembersResponse, error)

	// RemoveMember 踢出一名成员(不能踢 owner,调用方需持有 PermMemberRemove)。
	// 操作者 ID 用于写角色历史和权限校验。
	RemoveMember(ctx context.Context, operatorUserID, orgID, targetUserID uint64) error

	// LeaveOrg 成员主动退出(owner 不可用此接口)。
	LeaveOrg(ctx context.Context, orgID, userID uint64) error

	// AssignRole 变更成员角色。不能把成员变成 owner(owner 切换走所有权转让流程)。
	AssignRole(ctx context.Context, operatorUserID, orgID, targetUserID, newRoleID uint64) error
}

// ─── 实现 ────────────────────────────────────────────────────────────────────

type memberService struct {
	repo   repository.Repository
	hooks  *HookRegistry
	logger logger.LoggerInterface
}

// NewMemberService 构造一个 MemberService 实例。
func NewMemberService(repo repository.Repository, hooks *HookRegistry, log logger.LoggerInterface) MemberService {
	return &memberService{repo: repo, hooks: hooks, logger: log}
}

// ListMembers 分页返回某 org 的成员列表,每条附带角色和用户资料。用户资料查询失败仅记日志不中断。
// 数据库错误返回 ErrOrgInternal。
func (s *memberService) ListMembers(ctx context.Context, orgID uint64, page, size int) (*dto.ListMembersResponse, error) {
	if size <= 0 || size > organization.MaxPageSize {
		size = organization.DefaultPageSize
	}
	if page <= 0 {
		page = 1
	}

	items, total, err := s.repo.ListMembersByOrg(ctx, orgID, page, size)
	if err != nil {
		s.logger.ErrorCtx(ctx, "列出成员失败", err, map[string]any{"org_id": orgID})
		return nil, fmt.Errorf("list members: %w: %w", err, organization.ErrOrgInternal)
	}

	resp := &dto.ListMembersResponse{
		Items: make([]dto.MemberResponse, 0, len(items)),
		Total: total,
		Page:  page,
		Size:  size,
	}
	for _, mwr := range items {
		// 每条成员查询一次 user profile(小页面可以接受 N+1;大规模可以后续批量优化)
		profile, profileErr := s.repo.FindUserProfileByID(ctx, mwr.Member.UserID)
		if profileErr != nil {
			// 查不到用户不影响成员列表返回,仅记日志
			s.logger.WarnCtx(ctx, "加载成员用户资料失败,跳过", map[string]any{"user_id": mwr.Member.UserID, "error": profileErr.Error()})
			profile = nil
		}
		resp.Items = append(resp.Items, memberToDTO(mwr.Member, mwr.Role, profile))
	}
	return resp, nil
}

// RemoveMember 踢出指定成员(不能踢 owner,不能踢自己)。事务内删除 member + 写历史 + 触发 hook。
// 可能返回:ErrOrgInvalidRequest / ErrOrgNotFound / ErrMemberRemoveOwner / ErrMemberNotFound / ErrOrgInternal
func (s *memberService) RemoveMember(ctx context.Context, operatorUserID, orgID, targetUserID uint64) error {
	// 不能踢自己走这个接口
	if operatorUserID == targetUserID {
		s.logger.WarnCtx(ctx, "不能用 remove 接口踢自己", map[string]any{"user_id": operatorUserID, "org_id": orgID})
		return fmt.Errorf("use leave instead: %w", organization.ErrOrgInvalidRequest)
	}

	// 确认 org 存在且目标是成员
	org, err := s.repo.FindOrgByID(ctx, orgID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "org 不存在", map[string]any{"org_id": orgID})
			return fmt.Errorf("find org: %w", organization.ErrOrgNotFound)
		}
		s.logger.ErrorCtx(ctx, "查询 org 失败", err, map[string]any{"org_id": orgID})
		return fmt.Errorf("find org: %w: %w", err, organization.ErrOrgInternal)
	}
	if org.OwnerUserID == targetUserID {
		s.logger.WarnCtx(ctx, "不能踢出 owner", map[string]any{"org_id": orgID, "target": targetUserID})
		return fmt.Errorf("cannot remove owner: %w", organization.ErrMemberRemoveOwner)
	}

	// 确认目标是成员
	target, err := s.repo.FindMember(ctx, orgID, targetUserID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "成员不存在", map[string]any{"org_id": orgID, "target": targetUserID})
			return fmt.Errorf("find member: %w", organization.ErrMemberNotFound)
		}
		s.logger.ErrorCtx(ctx, "查询成员失败", err, map[string]any{"org_id": orgID, "target": targetUserID})
		return fmt.Errorf("find member: %w: %w", err, organization.ErrOrgInternal)
	}

	// 事务内删除 member + 写 role_history
	err = s.repo.WithTx(ctx, func(tx repository.Repository) error {
		if delErr := tx.DeleteMember(ctx, orgID, targetUserID); delErr != nil {
			s.logger.ErrorCtx(ctx, "事务内删除 member 失败", delErr, map[string]any{"org_id": orgID, "target": targetUserID})
			return fmt.Errorf("tx delete member: %w: %w", delErr, organization.ErrOrgInternal)
		}
		oldRoleID := target.RoleID
		if historyErr := tx.AppendRoleHistory(ctx, &model.OrgMemberRoleHistory{
			OrgID:           orgID,
			UserID:          targetUserID,
			FromRoleID:      &oldRoleID,
			ToRoleID:        0, // 0 表示已离开
			ChangedByUserID: operatorUserID,
			Reason:          organization.RoleChangeReasonLeave,
		}); historyErr != nil {
			s.logger.ErrorCtx(ctx, "事务内写退出历史失败", historyErr, map[string]any{"org_id": orgID, "target": targetUserID})
			return fmt.Errorf("tx append history: %w: %w", historyErr, organization.ErrOrgInternal)
		}
		return nil
	})
	if err != nil {
		s.logger.ErrorCtx(ctx, "踢出成员事务失败", err, map[string]any{"org_id": orgID, "target": targetUserID})
		//sayso-lint:ignore sentinel-wrap
		return err
	}
	s.logger.InfoCtx(ctx, "成员已被踢出", map[string]any{"org_id": orgID, "target": targetUserID, "operator": operatorUserID})

	// 事后触发 hook
	if s.hooks != nil {
		s.hooks.FireMemberRemoved(ctx, orgID, targetUserID, organization.RoleChangeReasonLeave)
	}
	return nil
}

// LeaveOrg 当前用户主动退出 org。Owner 调用此接口会被拒绝(必须先转让所有权)。
// 可能返回:ErrOrgNotFound / ErrOwnerCannotLeave / ErrOrgNotMember / ErrOrgInternal
func (s *memberService) LeaveOrg(ctx context.Context, orgID, userID uint64) error {
	org, err := s.repo.FindOrgByID(ctx, orgID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "org 不存在", map[string]any{"org_id": orgID})
			return fmt.Errorf("find org: %w", organization.ErrOrgNotFound)
		}
		s.logger.ErrorCtx(ctx, "查询 org 失败", err, map[string]any{"org_id": orgID})
		return fmt.Errorf("find org: %w: %w", err, organization.ErrOrgInternal)
	}
	if org.OwnerUserID == userID {
		s.logger.WarnCtx(ctx, "owner 不能主动退出", map[string]any{"org_id": orgID, "user_id": userID})
		return fmt.Errorf("owner cannot leave: %w", organization.ErrOwnerCannotLeave)
	}

	target, err := s.repo.FindMember(ctx, orgID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "非成员", map[string]any{"org_id": orgID, "user_id": userID})
			return fmt.Errorf("find member: %w", organization.ErrOrgNotMember)
		}
		s.logger.ErrorCtx(ctx, "查询成员失败", err, map[string]any{"org_id": orgID, "user_id": userID})
		return fmt.Errorf("find member: %w: %w", err, organization.ErrOrgInternal)
	}

	err = s.repo.WithTx(ctx, func(tx repository.Repository) error {
		if delErr := tx.DeleteMember(ctx, orgID, userID); delErr != nil {
			s.logger.ErrorCtx(ctx, "事务内退出 org 删除 member 失败", delErr, map[string]any{"org_id": orgID, "user_id": userID})
			return fmt.Errorf("tx delete member: %w: %w", delErr, organization.ErrOrgInternal)
		}
		oldRoleID := target.RoleID
		if historyErr := tx.AppendRoleHistory(ctx, &model.OrgMemberRoleHistory{
			OrgID:           orgID,
			UserID:          userID,
			FromRoleID:      &oldRoleID,
			ToRoleID:        0,
			ChangedByUserID: userID,
			Reason:          organization.RoleChangeReasonLeave,
		}); historyErr != nil {
			s.logger.ErrorCtx(ctx, "事务内写退出历史失败", historyErr, map[string]any{"org_id": orgID, "user_id": userID})
			return fmt.Errorf("tx append history: %w: %w", historyErr, organization.ErrOrgInternal)
		}
		return nil
	})
	if err != nil {
		s.logger.ErrorCtx(ctx, "退出 org 事务失败", err, map[string]any{"org_id": orgID, "user_id": userID})
		//sayso-lint:ignore sentinel-wrap
		return err
	}
	s.logger.InfoCtx(ctx, "成员主动退出", map[string]any{"org_id": orgID, "user_id": userID})

	if s.hooks != nil {
		s.hooks.FireMemberRemoved(ctx, orgID, userID, organization.RoleChangeReasonLeave)
	}
	return nil
}

// AssignRole 变更成员角色。不能改 owner 角色、不能给自己改角色、不能升级为 owner。
// 可能返回:ErrOwnerSelfDemote / ErrOrgNotFound / ErrMemberNotFound / ErrRoleNotFound / ErrOrgInvalidRequest / ErrOrgInternal
// 规则:
//   - 目标必须是成员
//   - 目标不能是 owner(owner 角色切换走所有权转让流程)
//   - newRoleID 必须属于同 org 且不是 owner 角色
//   - 不能给自己改角色(防止 owner 把自己降级)
func (s *memberService) AssignRole(ctx context.Context, operatorUserID, orgID, targetUserID, newRoleID uint64) error {
	if operatorUserID == targetUserID {
		s.logger.WarnCtx(ctx, "不能给自己分配角色", map[string]any{"user_id": operatorUserID, "org_id": orgID})
		return fmt.Errorf("cannot self-assign: %w", organization.ErrOwnerSelfDemote)
	}

	org, err := s.repo.FindOrgByID(ctx, orgID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "org 不存在", map[string]any{"org_id": orgID})
			return fmt.Errorf("find org: %w", organization.ErrOrgNotFound)
		}
		s.logger.ErrorCtx(ctx, "查询 org 失败", err, map[string]any{"org_id": orgID})
		return fmt.Errorf("find org: %w: %w", err, organization.ErrOrgInternal)
	}
	if org.OwnerUserID == targetUserID {
		s.logger.WarnCtx(ctx, "不能改 owner 的角色", map[string]any{"org_id": orgID, "target": targetUserID})
		return fmt.Errorf("cannot change owner role: %w", organization.ErrOwnerSelfDemote)
	}

	target, err := s.repo.FindMember(ctx, orgID, targetUserID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "成员不存在", map[string]any{"org_id": orgID, "target": targetUserID})
			return fmt.Errorf("find member: %w", organization.ErrMemberNotFound)
		}
		s.logger.ErrorCtx(ctx, "查询成员失败", err, map[string]any{"org_id": orgID, "target": targetUserID})
		return fmt.Errorf("find member: %w: %w", err, organization.ErrOrgInternal)
	}

	newRole, err := s.repo.FindRoleByID(ctx, newRoleID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "新角色不存在", map[string]any{"role_id": newRoleID})
			return fmt.Errorf("find new role: %w", organization.ErrRoleNotFound)
		}
		s.logger.ErrorCtx(ctx, "查询角色失败", err, map[string]any{"role_id": newRoleID})
		return fmt.Errorf("find role: %w: %w", err, organization.ErrOrgInternal)
	}
	if newRole.OrgID != orgID {
		s.logger.WarnCtx(ctx, "角色不属于该 org", map[string]any{"role_id": newRoleID, "org_id": orgID})
		return fmt.Errorf("role not in org: %w", organization.ErrRoleNotFound)
	}
	if newRole.Name == organization.RoleOwner {
		s.logger.WarnCtx(ctx, "不能把成员变成 owner(走转让流程)", map[string]any{"role_id": newRoleID})
		return fmt.Errorf("cannot assign owner role directly: %w", organization.ErrOrgInvalidRequest)
	}

	if target.RoleID == newRoleID {
		// 角色未变,幂等返回
		return nil
	}

	err = s.repo.WithTx(ctx, func(tx repository.Repository) error {
		if updateErr := tx.UpdateMemberRole(ctx, orgID, targetUserID, newRoleID); updateErr != nil {
			s.logger.ErrorCtx(ctx, "事务内更新 member 角色失败", updateErr, map[string]any{"org_id": orgID, "target": targetUserID, "new_role": newRoleID})
			return fmt.Errorf("tx update role: %w: %w", updateErr, organization.ErrOrgInternal)
		}
		oldRoleID := target.RoleID
		if historyErr := tx.AppendRoleHistory(ctx, &model.OrgMemberRoleHistory{
			OrgID:           orgID,
			UserID:          targetUserID,
			FromRoleID:      &oldRoleID,
			ToRoleID:        newRoleID,
			ChangedByUserID: operatorUserID,
			Reason:          organization.RoleChangeReasonRoleAssign,
		}); historyErr != nil {
			s.logger.ErrorCtx(ctx, "事务内写角色变更历史失败", historyErr, map[string]any{"org_id": orgID, "target": targetUserID})
			return fmt.Errorf("tx append history: %w: %w", historyErr, organization.ErrOrgInternal)
		}
		return nil
	})
	if err != nil {
		s.logger.ErrorCtx(ctx, "变更角色事务失败", err, map[string]any{"org_id": orgID, "target": targetUserID, "new_role": newRoleID})
		//sayso-lint:ignore sentinel-wrap
		return err
	}
	s.logger.InfoCtx(ctx, "成员角色已变更", map[string]any{"org_id": orgID, "target": targetUserID, "new_role": newRoleID, "operator": operatorUserID})
	return nil
}
