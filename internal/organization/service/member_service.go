// member_service.go 成员管理 service。
//
// 职责:
//   - 列成员(分页)
//   - 踢出成员(不能踢 owner)
//   - 主动退出(owner 拒绝)
package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/organization"
	"github.com/eyrihe999-stack/Synapse/internal/organization/dto"
	"github.com/eyrihe999-stack/Synapse/internal/organization/repository"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"gorm.io/gorm"
)

// MemberService 定义成员管理的业务操作。
type MemberService interface {
	// ListMembers 分页列出 org 的所有成员。调用方须是 org 成员。
	ListMembers(ctx context.Context, orgID uint64, page, size int) (*dto.ListMembersResponse, error)

	// RemoveMember 踢出一名成员(不能踢 owner,不能踢自己)。
	RemoveMember(ctx context.Context, operatorUserID, orgID, targetUserID uint64) error

	// LeaveOrg 成员主动退出(owner 不可用此接口)。
	LeaveOrg(ctx context.Context, orgID, userID uint64) error
}

// ─── 实现 ────────────────────────────────────────────────────────────────────

type memberService struct {
	repo   repository.Repository
	logger logger.LoggerInterface
}

// NewMemberService 构造一个 MemberService 实例。
func NewMemberService(repo repository.Repository, log logger.LoggerInterface) MemberService {
	return &memberService{repo: repo, logger: log}
}

// ListMembers 分页返回某 org 的成员列表。
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
	for _, m := range items {
		resp.Items = append(resp.Items, memberWithProfileToDTO(m))
	}
	return resp, nil
}

// RemoveMember 踢出指定成员(不能踢 owner,不能踢自己)。
//
// 可能的错误:
//   - ErrOrgInvalidRequest:operator 等于 target(应走 LeaveOrg)
//   - ErrOrgNotFound:org 不存在
//   - ErrMemberRemoveOwner:目标是 owner
//   - ErrMemberNotFound:目标用户不是 org 成员
//   - ErrOrgInternal:数据库查询或删除失败
func (s *memberService) RemoveMember(ctx context.Context, operatorUserID, orgID, targetUserID uint64) error {
	if operatorUserID == targetUserID {
		s.logger.WarnCtx(ctx, "不能用 remove 接口踢自己", map[string]any{"user_id": operatorUserID, "org_id": orgID})
		return fmt.Errorf("use leave instead: %w", organization.ErrOrgInvalidRequest)
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
		s.logger.WarnCtx(ctx, "不能踢出 owner", map[string]any{"org_id": orgID, "target": targetUserID})
		return fmt.Errorf("cannot remove owner: %w", organization.ErrMemberRemoveOwner)
	}

	//sayso-lint:ignore err-swallow
	if _, err := s.repo.FindMember(ctx, orgID, targetUserID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "成员不存在", map[string]any{"org_id": orgID, "target": targetUserID})
			return fmt.Errorf("find member: %w", organization.ErrMemberNotFound)
		}
		s.logger.ErrorCtx(ctx, "查询成员失败", err, map[string]any{"org_id": orgID, "target": targetUserID})
		return fmt.Errorf("find member: %w: %w", err, organization.ErrOrgInternal)
	}

	if err := s.repo.DeleteMember(ctx, orgID, targetUserID); err != nil {
		s.logger.ErrorCtx(ctx, "删除 member 失败", err, map[string]any{"org_id": orgID, "target": targetUserID})
		return fmt.Errorf("delete member: %w: %w", err, organization.ErrOrgInternal)
	}
	s.logger.InfoCtx(ctx, "成员已被踢出", map[string]any{"org_id": orgID, "target": targetUserID, "operator": operatorUserID})
	return nil
}

// LeaveOrg 当前用户主动退出 org。Owner 调用此接口会被拒绝。
//
// 可能的错误:
//   - ErrOrgNotFound:org 不存在
//   - ErrOwnerCannotLeave:调用者是该 org 的 owner
//   - ErrOrgNotMember:调用者不是该 org 的成员
//   - ErrOrgInternal:数据库查询或删除失败
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

	//sayso-lint:ignore err-swallow
	if _, err := s.repo.FindMember(ctx, orgID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "非成员", map[string]any{"org_id": orgID, "user_id": userID})
			return fmt.Errorf("find member: %w", organization.ErrOrgNotMember)
		}
		s.logger.ErrorCtx(ctx, "查询成员失败", err, map[string]any{"org_id": orgID, "user_id": userID})
		return fmt.Errorf("find member: %w: %w", err, organization.ErrOrgInternal)
	}

	if err := s.repo.DeleteMember(ctx, orgID, userID); err != nil {
		s.logger.ErrorCtx(ctx, "退出 org 删除 member 失败", err, map[string]any{"org_id": orgID, "user_id": userID})
		return fmt.Errorf("delete member: %w: %w", err, organization.ErrOrgInternal)
	}
	s.logger.InfoCtx(ctx, "成员主动退出", map[string]any{"org_id": orgID, "user_id": userID})
	return nil
}
