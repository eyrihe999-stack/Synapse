// org.go Repository 接口中 Org 资源的实现。
package repository

import (
	"context"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/organization/model"
)

// CreateOrg 创建一条 org 记录。
// 调用方通常在事务内先创建 org,再用同一个事务创建预设角色和 owner member。
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
// updates 为空时不做任何事(返回 nil)。
// 使用 Updates 而非 Save 以避免覆盖其他字段。
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
// includeDissolved=false 时排除已解散 org(用于"是否超出创建上限"的检查)。
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

// ListOrgsByUser 列出某用户所属的所有 active org,返回 (Org, Member, Role) 三元组。
// 一次性 JOIN 回来避免 N+1 查询。已解散的 org 不返回。
func (r *gormRepository) ListOrgsByUser(ctx context.Context, userID uint64) ([]OrgWithMember, error) {
	// 先查成员关系和 org(内连接),再一次性查出所有涉及的 role。
	type row struct {
		// org 字段
		OrgID                 uint64 `gorm:"column:org_id"`
		Slug                  string
		DisplayName           string
		Description           string
		OwnerUserID           uint64
		Status                string
		RequireAgentReview    bool
		RecordFullPayload     bool
		// member 字段
		MemberID uint64 `gorm:"column:member_id"`
		RoleID   uint64 `gorm:"column:role_id"`
		JoinedAt []byte `gorm:"column:joined_at"` // 用 []byte 避免时间类型扫描差异
	}
	var rows []row
	err := r.db.WithContext(ctx).Raw(`
		SELECT o.id AS org_id, o.slug, o.display_name, o.description, o.owner_user_id,
		       o.status, o.require_agent_review, o.record_full_payload,
		       m.id AS member_id, m.role_id, m.joined_at
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

	// 收集所有 role_id,一次性查回
	roleIDs := make([]uint64, 0, len(rows))
	for _, rr := range rows {
		roleIDs = append(roleIDs, rr.RoleID)
	}
	var roles []*model.OrgRole
	if err := r.db.WithContext(ctx).
		Where("id IN ?", roleIDs).
		Find(&roles).Error; err != nil {
		return nil, fmt.Errorf("load roles: %w", err)
	}
	roleMap := make(map[uint64]*model.OrgRole, len(roles))
	for _, rl := range roles {
		roleMap[rl.ID] = rl
	}

	// 拼装结果
	out := make([]OrgWithMember, 0, len(rows))
	for _, rr := range rows {
		org := &model.Org{
			ID:                 rr.OrgID,
			Slug:               rr.Slug,
			DisplayName:        rr.DisplayName,
			Description:        rr.Description,
			OwnerUserID:        rr.OwnerUserID,
			Status:             rr.Status,
			RequireAgentReview: rr.RequireAgentReview,
			RecordFullPayload:  rr.RecordFullPayload,
		}
		member := &model.OrgMember{
			ID:     rr.MemberID,
			OrgID:  rr.OrgID,
			UserID: userID,
			RoleID: rr.RoleID,
		}
		out = append(out, OrgWithMember{
			Org:    org,
			Member: member,
			Role:   roleMap[rr.RoleID],
		})
	}
	return out, nil
}
