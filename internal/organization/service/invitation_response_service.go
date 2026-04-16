// invitation_response_service.go 邀请的响应类方法(accept/reject/revoke)和过期定时任务。
//
// 这里从 invitation_service.go 拆分出来,是为了满足模块级文件行数阈值。
// 所有方法都是 *invitationService 的 receiver 方法,共享同一个 struct。
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/organization"
	"github.com/eyrihe999-stack/Synapse/internal/organization/model"
	"github.com/eyrihe999-stack/Synapse/internal/organization/repository"
	"gorm.io/gorm"
)

// Accept 接受邀请。普通邀请直接插 member,所有权转让邀请在事务内完成 owner 交接。
// 可能返回:ErrInvitationNotFound / ErrInvitationNotForYou / ErrInvitationNotPending / ErrInvitationExpired / ErrOrgNotFound / ErrOrgDissolved / ErrOrgInternal
// 流程:
//  1. 校验邀请存在、属于当前用户、处于 pending、未过期
//  2. 再次检查 org 未被解散
//  3. 事务内:
//     a. 如果是 ownership_transfer:把旧 owner 降级为 admin,org.owner_user_id 更新
//     b. 创建新的 member 记录(新成员加入,或新 owner)
//     c. 更新邀请状态为 accepted
//     d. 写 role_history
func (s *invitationService) Accept(ctx context.Context, userID, invitationID uint64) error {
	inv, err := s.repo.FindInvitationByID(ctx, invitationID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "邀请不存在", map[string]any{"inv_id": invitationID})
			return fmt.Errorf("invitation not found: %w", organization.ErrInvitationNotFound)
		}
		s.logger.ErrorCtx(ctx, "查询邀请失败", err, map[string]any{"inv_id": invitationID})
		return fmt.Errorf("find invitation: %w: %w", err, organization.ErrOrgInternal)
	}
	if inv.InviteeUserID != userID {
		s.logger.WarnCtx(ctx, "非被邀请人", map[string]any{"inv_id": invitationID, "user_id": userID})
		return fmt.Errorf("not for you: %w", organization.ErrInvitationNotForYou)
	}
	if inv.Status != model.InvitationStatusPending {
		s.logger.WarnCtx(ctx, "邀请非 pending", map[string]any{"inv_id": invitationID, "status": inv.Status})
		return fmt.Errorf("not pending: %w", organization.ErrInvitationNotPending)
	}
	if time.Now().UTC().After(inv.ExpiresAt) {
		s.logger.WarnCtx(ctx, "邀请已过期", map[string]any{"inv_id": invitationID})
		return fmt.Errorf("expired: %w", organization.ErrInvitationExpired)
	}

	// 再次检查 org 状态(可能刚被解散)
	org, err := s.repo.FindOrgByID(ctx, inv.OrgID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "org 不存在", map[string]any{"org_id": inv.OrgID})
			return fmt.Errorf("find org: %w", organization.ErrOrgNotFound)
		}
		s.logger.ErrorCtx(ctx, "查询 org 失败", err, map[string]any{"org_id": inv.OrgID})
		return fmt.Errorf("find org: %w: %w", err, organization.ErrOrgInternal)
	}
	if org.Status != model.OrgStatusActive {
		s.logger.WarnCtx(ctx, "org 已解散", map[string]any{"org_id": inv.OrgID})
		return fmt.Errorf("org dissolved: %w", organization.ErrOrgDissolved)
	}

	// 事务内完成接受逻辑,闭包内的 helper 已自行包装 sentinel
	err = s.repo.WithTx(ctx, func(tx repository.Repository) error {
		if inv.Type == model.InvitationTypeOwnershipTransfer {
			//sayso-lint:ignore sentinel-wrap
			return s.acceptOwnershipTransfer(ctx, tx, inv, org, userID)
		}
		//sayso-lint:ignore sentinel-wrap
		return s.acceptMemberJoin(ctx, tx, inv, userID)
	})
	if err != nil {
		s.logger.ErrorCtx(ctx, "接受邀请事务失败", err, map[string]any{"inv_id": invitationID, "user_id": userID})
		// 事务闭包内的错误已携带 sentinel 外层重复包装会冗余
		//sayso-lint:ignore sentinel-wrap
		return err
	}
	s.logger.InfoCtx(ctx, "邀请已接受", map[string]any{"inv_id": invitationID, "user_id": userID, "type": inv.Type})
	return nil
}

// acceptOwnershipTransfer 处理所有权转让的事务逻辑。
// 把原 owner 降级为 admin、把新 owner(已是成员)升级为 owner、更新 org.owner_user_id。
// 可能返回:ErrOrgInternal(任一 DB 操作失败)
func (s *invitationService) acceptOwnershipTransfer(ctx context.Context, tx repository.Repository, inv *model.OrgInvitation, org *model.Org, newOwnerUserID uint64) error {
	// 查 admin 和 owner 角色
	adminRole, findAdminErr := tx.FindRoleByOrgName(ctx, inv.OrgID, organization.RoleAdmin)
	if findAdminErr != nil {
		s.logger.ErrorCtx(ctx, "查 admin 角色失败", findAdminErr, map[string]any{"org_id": inv.OrgID})
		return fmt.Errorf("find admin role: %w: %w", findAdminErr, organization.ErrOrgInternal)
	}
	ownerRole, findOwnerErr := tx.FindRoleByOrgName(ctx, inv.OrgID, organization.RoleOwner)
	if findOwnerErr != nil {
		s.logger.ErrorCtx(ctx, "查 owner 角色失败", findOwnerErr, map[string]any{"org_id": inv.OrgID})
		return fmt.Errorf("find owner role: %w: %w", findOwnerErr, organization.ErrOrgInternal)
	}

	// 原 owner → admin
	oldOwnerMember, findOldErr := tx.FindMember(ctx, inv.OrgID, org.OwnerUserID)
	if findOldErr != nil {
		s.logger.ErrorCtx(ctx, "查原 owner member 失败", findOldErr, map[string]any{"org_id": inv.OrgID})
		return fmt.Errorf("find old owner member: %w: %w", findOldErr, organization.ErrOrgInternal)
	}
	if demoteErr := tx.UpdateMemberRole(ctx, inv.OrgID, org.OwnerUserID, adminRole.ID); demoteErr != nil {
		s.logger.ErrorCtx(ctx, "降级原 owner 失败", demoteErr, map[string]any{"org_id": inv.OrgID})
		return fmt.Errorf("demote old owner: %w: %w", demoteErr, organization.ErrOrgInternal)
	}
	oldFrom := oldOwnerMember.RoleID
	if historyOldErr := tx.AppendRoleHistory(ctx, &model.OrgMemberRoleHistory{
		OrgID:           inv.OrgID,
		UserID:          org.OwnerUserID,
		FromRoleID:      &oldFrom,
		ToRoleID:        adminRole.ID,
		ChangedByUserID: newOwnerUserID,
		Reason:          organization.RoleChangeReasonOwnershipTransfer,
	}); historyOldErr != nil {
		s.logger.ErrorCtx(ctx, "写原 owner 历史失败", historyOldErr, map[string]any{"org_id": inv.OrgID})
		return fmt.Errorf("history old owner: %w: %w", historyOldErr, organization.ErrOrgInternal)
	}

	// 新 owner(已是成员)→ owner
	newMember, findNewErr := tx.FindMember(ctx, inv.OrgID, newOwnerUserID)
	if findNewErr != nil {
		s.logger.ErrorCtx(ctx, "查新 owner member 失败", findNewErr, map[string]any{"org_id": inv.OrgID, "user_id": newOwnerUserID})
		return fmt.Errorf("find new owner member: %w: %w", findNewErr, organization.ErrOrgInternal)
	}
	if promoteErr := tx.UpdateMemberRole(ctx, inv.OrgID, newOwnerUserID, ownerRole.ID); promoteErr != nil {
		s.logger.ErrorCtx(ctx, "升级新 owner 失败", promoteErr, map[string]any{"org_id": inv.OrgID, "user_id": newOwnerUserID})
		return fmt.Errorf("promote new owner: %w: %w", promoteErr, organization.ErrOrgInternal)
	}
	newFrom := newMember.RoleID
	if historyErr := tx.AppendRoleHistory(ctx, &model.OrgMemberRoleHistory{
		OrgID:           inv.OrgID,
		UserID:          newOwnerUserID,
		FromRoleID:      &newFrom,
		ToRoleID:        ownerRole.ID,
		ChangedByUserID: newOwnerUserID,
		Reason:          organization.RoleChangeReasonOwnershipTransfer,
	}); historyErr != nil {
		s.logger.ErrorCtx(ctx, "写新 owner 历史失败", historyErr, map[string]any{"org_id": inv.OrgID})
		return fmt.Errorf("history new owner: %w: %w", historyErr, organization.ErrOrgInternal)
	}

	// 更新 org.owner_user_id
	if updateErr := tx.UpdateOrgFields(ctx, inv.OrgID, map[string]any{"owner_user_id": newOwnerUserID}); updateErr != nil {
		s.logger.ErrorCtx(ctx, "更新 org owner 失败", updateErr, map[string]any{"org_id": inv.OrgID})
		return fmt.Errorf("update org owner: %w: %w", updateErr, organization.ErrOrgInternal)
	}

	// 更新邀请状态
	if statusErr := tx.UpdateInvitationStatus(ctx, inv.ID, model.InvitationStatusAccepted); statusErr != nil {
		s.logger.ErrorCtx(ctx, "更新邀请状态失败", statusErr, map[string]any{"inv_id": inv.ID})
		return fmt.Errorf("accept invitation: %w: %w", statusErr, organization.ErrOrgInternal)
	}
	return nil
}

// acceptMemberJoin 处理普通成员邀请接受的事务逻辑:插入 member 记录、写首次加入历史、标记邀请 accepted。
// 可能返回:ErrOrgInternal(任一 DB 操作失败)
func (s *invitationService) acceptMemberJoin(ctx context.Context, tx repository.Repository, inv *model.OrgInvitation, userID uint64) error {
	now := time.Now().UTC()
	member := &model.OrgMember{
		OrgID:    inv.OrgID,
		UserID:   userID,
		RoleID:   inv.RoleID,
		JoinedAt: now,
	}
	if createErr := tx.CreateMember(ctx, member); createErr != nil {
		s.logger.ErrorCtx(ctx, "创建 member 失败", createErr, map[string]any{"org_id": inv.OrgID, "user_id": userID})
		return fmt.Errorf("create member: %w: %w", createErr, organization.ErrOrgInternal)
	}
	if historyErr := tx.AppendRoleHistory(ctx, &model.OrgMemberRoleHistory{
		OrgID:           inv.OrgID,
		UserID:          userID,
		FromRoleID:      nil,
		ToRoleID:        inv.RoleID,
		ChangedByUserID: userID,
		Reason:          organization.RoleChangeReasonJoin,
	}); historyErr != nil {
		s.logger.ErrorCtx(ctx, "写加入历史失败", historyErr, map[string]any{"org_id": inv.OrgID})
		return fmt.Errorf("history join: %w: %w", historyErr, organization.ErrOrgInternal)
	}
	if statusErr := tx.UpdateInvitationStatus(ctx, inv.ID, model.InvitationStatusAccepted); statusErr != nil {
		s.logger.ErrorCtx(ctx, "更新邀请状态失败", statusErr, map[string]any{"inv_id": inv.ID})
		return fmt.Errorf("accept invitation: %w: %w", statusErr, organization.ErrOrgInternal)
	}
	return nil
}

// Reject 当前用户拒绝收到的邀请。
// 可能返回:ErrInvitationNotFound / ErrInvitationNotForYou / ErrInvitationNotPending / ErrOrgInternal
func (s *invitationService) Reject(ctx context.Context, userID, invitationID uint64) error {
	inv, err := s.repo.FindInvitationByID(ctx, invitationID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "邀请不存在", map[string]any{"inv_id": invitationID})
			return fmt.Errorf("invitation not found: %w", organization.ErrInvitationNotFound)
		}
		s.logger.ErrorCtx(ctx, "查询邀请失败", err, map[string]any{"inv_id": invitationID})
		return fmt.Errorf("find invitation: %w: %w", err, organization.ErrOrgInternal)
	}
	if inv.InviteeUserID != userID {
		s.logger.WarnCtx(ctx, "非被邀请人", map[string]any{"inv_id": invitationID, "user_id": userID})
		return fmt.Errorf("not for you: %w", organization.ErrInvitationNotForYou)
	}
	if inv.Status != model.InvitationStatusPending {
		s.logger.WarnCtx(ctx, "非 pending", map[string]any{"inv_id": invitationID, "status": inv.Status})
		return fmt.Errorf("not pending: %w", organization.ErrInvitationNotPending)
	}
	if err := s.repo.UpdateInvitationStatus(ctx, inv.ID, model.InvitationStatusRejected); err != nil {
		s.logger.ErrorCtx(ctx, "拒绝邀请失败", err, map[string]any{"inv_id": invitationID})
		return fmt.Errorf("reject invitation: %w: %w", err, organization.ErrOrgInternal)
	}
	s.logger.InfoCtx(ctx, "邀请已拒绝", map[string]any{"inv_id": invitationID, "user_id": userID})
	return nil
}

// Revoke 撤销一条 pending 邀请。发起人和管理员都可以调用,权限校验由 handler 层完成。
// 可能返回:ErrInvitationNotFound / ErrInvitationNotPending / ErrOrgInternal
func (s *invitationService) Revoke(ctx context.Context, operatorUserID, invitationID uint64) error {
	inv, err := s.repo.FindInvitationByID(ctx, invitationID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "邀请不存在", map[string]any{"inv_id": invitationID})
			return fmt.Errorf("invitation not found: %w", organization.ErrInvitationNotFound)
		}
		s.logger.ErrorCtx(ctx, "查询邀请失败", err, map[string]any{"inv_id": invitationID})
		return fmt.Errorf("find invitation: %w: %w", err, organization.ErrOrgInternal)
	}
	if inv.Status != model.InvitationStatusPending {
		s.logger.WarnCtx(ctx, "非 pending,无法撤销", map[string]any{"inv_id": invitationID, "status": inv.Status})
		return fmt.Errorf("not pending: %w", organization.ErrInvitationNotPending)
	}
	if err := s.repo.UpdateInvitationStatus(ctx, inv.ID, model.InvitationStatusRevoked); err != nil {
		s.logger.ErrorCtx(ctx, "撤销邀请失败", err, map[string]any{"inv_id": invitationID})
		return fmt.Errorf("revoke invitation: %w: %w", err, organization.ErrOrgInternal)
	}
	s.logger.InfoCtx(ctx, "邀请已撤销", map[string]any{"inv_id": invitationID, "operator": operatorUserID})
	return nil
}

// ExpireJob 定时任务:批量将所有 expires_at 已过且 status=pending 的邀请标记为 expired。
// 返回受影响行数。数据库失败返回 ErrOrgInternal。
func (s *invitationService) ExpireJob(ctx context.Context) (int64, error) {
	n, err := s.repo.ExpirePendingInvitations(ctx)
	if err != nil {
		s.logger.ErrorCtx(ctx, "过期邀请定时任务失败", err, nil)
		return 0, fmt.Errorf("expire invitations: %w: %w", err, organization.ErrOrgInternal)
	}
	if n > 0 {
		s.logger.InfoCtx(ctx, "过期邀请清理完成", map[string]any{"affected": n})
	}
	return n, nil
}
