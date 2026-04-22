// org.go Repository 接口中 Org 资源的实现。
package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/organization/model"
)

// CreateOrg 创建一条 org 记录。
func (r *gormRepository) CreateOrg(ctx context.Context, org *model.Org) error {
	if err := r.db.WithContext(ctx).Create(org).Error; err != nil {
		return fmt.Errorf("create org: %w", err)
	}
	return nil
}

// FindOrgByID 按主键查找 org。
// 不存在时返回 gorm.ErrRecordNotFound,service 层应翻译为 ErrOrgNotFound。
func (r *gormRepository) FindOrgByID(ctx context.Context, id uint64) (*model.Org, error) {
	var org model.Org
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&org).Error; err != nil {
		return nil, err
	}
	return &org, nil
}

// FindOrgBySlug 按 slug 查找 org。
func (r *gormRepository) FindOrgBySlug(ctx context.Context, slug string) (*model.Org, error) {
	var org model.Org
	if err := r.db.WithContext(ctx).Where("slug = ?", slug).First(&org).Error; err != nil {
		return nil, err
	}
	return &org, nil
}

// UpdateOrgFields 部分更新 org 字段。
func (r *gormRepository) UpdateOrgFields(ctx context.Context, id uint64, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	if err := r.db.WithContext(ctx).
		Model(&model.Org{}).
		Where("id = ?", id).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("update org fields: %w", err)
	}
	return nil
}

// CountOwnedOrgsByUser 统计某用户作为 owner 的 org 数量。
func (r *gormRepository) CountOwnedOrgsByUser(ctx context.Context, userID uint64, includeDissolved bool) (int64, error) {
	q := r.db.WithContext(ctx).Model(&model.Org{}).Where("owner_user_id = ?", userID)
	if !includeDissolved {
		q = q.Where("status = ?", model.OrgStatusActive)
	}
	var count int64
	if err := q.Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count owned orgs: %w", err)
	}
	return count, nil
}

// ListActiveOrgsOwnedBy 列出某 user 作为 owner 的所有 active org,按 slug 排序。
func (r *gormRepository) ListActiveOrgsOwnedBy(ctx context.Context, userID uint64) ([]*model.Org, error) {
	var orgs []*model.Org
	if err := r.db.WithContext(ctx).
		Where("owner_user_id = ? AND status = ?", userID, model.OrgStatusActive).
		Order("slug ASC").
		Find(&orgs).Error; err != nil {
		return nil, fmt.Errorf("list owned active orgs: %w", err)
	}
	return orgs, nil
}

// ListOrgsByUser 列出某用户所属的所有 active org,返回 (Org, Member) 二元组。
func (r *gormRepository) ListOrgsByUser(ctx context.Context, userID uint64) ([]OrgWithMember, error) {
	type row struct {
		OrgID        uint64    `gorm:"column:org_id"`
		Slug         string    `gorm:"column:slug"`
		DisplayName  string    `gorm:"column:display_name"`
		Description  string    `gorm:"column:description"`
		OwnerUserID  uint64    `gorm:"column:owner_user_id"`
		Status       string    `gorm:"column:status"`
		OrgCreatedAt time.Time `gorm:"column:org_created_at"`
		OrgUpdatedAt time.Time `gorm:"column:org_updated_at"`
		MemberID     uint64    `gorm:"column:member_id"`
		JoinedAt     time.Time `gorm:"column:joined_at"`
	}
	var rows []row
	err := r.db.WithContext(ctx).Raw(`
		SELECT o.id AS org_id, o.slug, o.display_name, o.description, o.owner_user_id,
		       o.status, o.created_at AS org_created_at, o.updated_at AS org_updated_at,
		       m.id AS member_id, m.joined_at
		FROM orgs o
		INNER JOIN org_members m ON m.org_id = o.id
		WHERE m.user_id = ? AND o.status = ?
		ORDER BY m.joined_at DESC
	`, userID, model.OrgStatusActive).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list orgs by user: %w", err)
	}

	if len(rows) == 0 {
		return []OrgWithMember{}, nil
	}

	out := make([]OrgWithMember, 0, len(rows))
	for _, rr := range rows {
		org := &model.Org{
			ID:          rr.OrgID,
			Slug:        rr.Slug,
			DisplayName: rr.DisplayName,
			Description: rr.Description,
			OwnerUserID: rr.OwnerUserID,
			Status:      rr.Status,
			CreatedAt:   rr.OrgCreatedAt,
			UpdatedAt:   rr.OrgUpdatedAt,
		}
		member := &model.OrgMember{
			ID:       rr.MemberID,
			OrgID:    rr.OrgID,
			UserID:   userID,
			JoinedAt: rr.JoinedAt,
		}
		out = append(out, OrgWithMember{
			Org:    org,
			Member: member,
		})
	}
	return out, nil
}
