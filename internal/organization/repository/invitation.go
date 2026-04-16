// invitation.go Repository 接口中 Invitation 资源的实现。
package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/organization/model"
)

// CreateInvitation 创建一条邀请。
func (r *gormRepository) CreateInvitation(ctx context.Context, inv *model.OrgInvitation) error {
	if err := r.db.WithContext(ctx).Create(inv).Error; err != nil {
		return fmt.Errorf("create invitation: %w", err)
	}
	return nil
}

// FindInvitationByID 按主键查找邀请。
func (r *gormRepository) FindInvitationByID(ctx context.Context, id uint64) (*model.OrgInvitation, error) {
	var inv model.OrgInvitation
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&inv).Error; err != nil {
		return nil, err
	}
	return &inv, nil
}

// FindPendingByOrgInvitee 按 (org_id, invitee_user_id, status=pending) 查找唯一记录。
// 第一版通过业务约束保证同 org 同 invitee 只能有一条 pending,DB 层不做唯一索引。
func (r *gormRepository) FindPendingByOrgInvitee(ctx context.Context, orgID, inviteeUserID uint64) (*model.OrgInvitation, error) {
	var inv model.OrgInvitation
	if err := r.db.WithContext(ctx).
		Where("org_id = ? AND invitee_user_id = ? AND status = ?", orgID, inviteeUserID, model.InvitationStatusPending).
		First(&inv).Error; err != nil {
		return nil, err
	}
	return &inv, nil
}

// ListPendingByInvitee 列出某用户收到的 pending 邀请,分页。
func (r *gormRepository) ListPendingByInvitee(ctx context.Context, inviteeUserID uint64, page, size int) ([]*model.OrgInvitation, int64, error) {
	return r.ListByInvitee(ctx, inviteeUserID, model.InvitationStatusPending, page, size)
}

// ListByInvitee 列出某用户收到的邀请,按状态过滤(空字符串=全部),分页。
func (r *gormRepository) ListByInvitee(ctx context.Context, inviteeUserID uint64, status string, page, size int) ([]*model.OrgInvitation, int64, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	offset := (page - 1) * size

	q := r.db.WithContext(ctx).
		Model(&model.OrgInvitation{}).
		Where("invitee_user_id = ?", inviteeUserID)
	if status != "" {
		q = q.Where("status = ?", status)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count invitations: %w", err)
	}
	if total == 0 {
		return []*model.OrgInvitation{}, 0, nil
	}

	var list []*model.OrgInvitation
	if err := q.Order("created_at DESC").
		Offset(offset).Limit(size).
		Find(&list).Error; err != nil {
		return nil, 0, fmt.Errorf("list invitations: %w", err)
	}
	return list, total, nil
}

// ListPendingByOrg 列出某 org 的 pending 邀请,分页。
func (r *gormRepository) ListPendingByOrg(ctx context.Context, orgID uint64, page, size int) ([]*model.OrgInvitation, int64, error) {
	return r.ListByOrg(ctx, orgID, model.InvitationStatusPending, page, size)
}

// ListByOrg 列出某 org 的邀请,按状态过滤(空字符串=全部),分页。
func (r *gormRepository) ListByOrg(ctx context.Context, orgID uint64, status string, page, size int) ([]*model.OrgInvitation, int64, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	offset := (page - 1) * size

	q := r.db.WithContext(ctx).
		Model(&model.OrgInvitation{}).
		Where("org_id = ?", orgID)
	if status != "" {
		q = q.Where("status = ?", status)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count invitations: %w", err)
	}
	if total == 0 {
		return []*model.OrgInvitation{}, 0, nil
	}

	var list []*model.OrgInvitation
	if err := q.Order("created_at DESC").
		Offset(offset).Limit(size).
		Find(&list).Error; err != nil {
		return nil, 0, fmt.Errorf("list invitations: %w", err)
	}
	return list, total, nil
}

// UpdateInvitationStatus 更新邀请状态,同时写入 responded_at。
// 用于 accept / reject / revoke 三种状态变更(expire 走专用的 ExpirePendingInvitations)。
func (r *gormRepository) UpdateInvitationStatus(ctx context.Context, id uint64, status string) error {
	now := time.Now().UTC()
	updates := map[string]any{
		"status":       status,
		"responded_at": &now,
	}
	if err := r.db.WithContext(ctx).
		Model(&model.OrgInvitation{}).
		Where("id = ?", id).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("update invitation status: %w", err)
	}
	return nil
}

// ExpirePendingInvitations 把所有 expires_at 已过且 status=pending 的邀请批量标记为 expired。
// 用于定时任务(每天一次)。返回受影响行数便于日志追踪。
func (r *gormRepository) ExpirePendingInvitations(ctx context.Context) (int64, error) {
	result := r.db.WithContext(ctx).
		Model(&model.OrgInvitation{}).
		Where("status = ? AND expires_at < ?", model.InvitationStatusPending, time.Now().UTC()).
		Update("status", model.InvitationStatusExpired)
	if result.Error != nil {
		return 0, fmt.Errorf("expire invitations: %w", result.Error)
	}
	return result.RowsAffected, nil
}
