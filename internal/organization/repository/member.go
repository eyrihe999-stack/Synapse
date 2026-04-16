// member.go Repository 接口中 Member 资源的实现。
package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/organization/model"
	"gorm.io/gorm"
)

// CreateMember 创建一条成员关系。
func (r *gormRepository) CreateMember(ctx context.Context, member *model.OrgMember) error {
	if err := r.db.WithContext(ctx).Create(member).Error; err != nil {
		return fmt.Errorf("create member: %w", err)
	}
	return nil
}

// FindMember 按 (org_id, user_id) 查找唯一成员关系。
// 不存在时返回 gorm.ErrRecordNotFound。
func (r *gormRepository) FindMember(ctx context.Context, orgID, userID uint64) (*model.OrgMember, error) {
	var member model.OrgMember
	if err := r.db.WithContext(ctx).
		Where("org_id = ? AND user_id = ?", orgID, userID).
		First(&member).Error; err != nil {
		return nil, err
	}
	return &member, nil
}

// FindMemberWithRole 查找成员关系并附带其角色。
// 角色会在同一个查询中 JOIN 拉回,避免调用方再查一次。
func (r *gormRepository) FindMemberWithRole(ctx context.Context, orgID, userID uint64) (*MemberWithRole, error) {
	var member model.OrgMember
	if err := r.db.WithContext(ctx).
		Where("org_id = ? AND user_id = ?", orgID, userID).
		First(&member).Error; err != nil {
		return nil, err
	}
	var role model.OrgRole
	if err := r.db.WithContext(ctx).
		Where("id = ?", member.RoleID).
		First(&role).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("member role missing for member %d: %w", member.ID, err)
		}
		return nil, fmt.Errorf("load member role: %w", err)
	}
	return &MemberWithRole{Member: &member, Role: &role}, nil
}

// ListMembersByOrg 分页列出 org 的成员,每条结果带角色。
// 返回 (列表, 总数, error)。
func (r *gormRepository) ListMembersByOrg(ctx context.Context, orgID uint64, page, size int) ([]*MemberWithRole, int64, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	offset := (page - 1) * size

	var total int64
	if err := r.db.WithContext(ctx).
		Model(&model.OrgMember{}).
		Where("org_id = ?", orgID).
		Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count members: %w", err)
	}
	if total == 0 {
		return []*MemberWithRole{}, 0, nil
	}

	var members []*model.OrgMember
	if err := r.db.WithContext(ctx).
		Where("org_id = ?", orgID).
		Order("joined_at ASC").
		Offset(offset).Limit(size).
		Find(&members).Error; err != nil {
		return nil, 0, fmt.Errorf("list members: %w", err)
	}
	if len(members) == 0 {
		return []*MemberWithRole{}, total, nil
	}

	roleIDs := make([]uint64, 0, len(members))
	for _, m := range members {
		roleIDs = append(roleIDs, m.RoleID)
	}
	var roles []*model.OrgRole
	if err := r.db.WithContext(ctx).
		Where("id IN ?", roleIDs).
		Find(&roles).Error; err != nil {
		return nil, 0, fmt.Errorf("load roles: %w", err)
	}
	roleMap := make(map[uint64]*model.OrgRole, len(roles))
	for _, rl := range roles {
		roleMap[rl.ID] = rl
	}

	out := make([]*MemberWithRole, 0, len(members))
	for _, m := range members {
		out = append(out, &MemberWithRole{Member: m, Role: roleMap[m.RoleID]})
	}
	return out, total, nil
}

// CountMembersByOrg 统计某 org 的成员数。
func (r *gormRepository) CountMembersByOrg(ctx context.Context, orgID uint64) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).
		Model(&model.OrgMember{}).
		Where("org_id = ?", orgID).
		Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count members by org: %w", err)
	}
	return count, nil
}

// CountMembersByUser 统计某用户加入的 active org 数量。
// 通过 JOIN orgs 过滤掉 dissolved org。
func (r *gormRepository) CountMembersByUser(ctx context.Context, userID uint64) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Raw(`
		SELECT COUNT(*) FROM org_members m
		INNER JOIN orgs o ON o.id = m.org_id
		WHERE m.user_id = ? AND o.status = ?
	`, userID, model.OrgStatusActive).Scan(&count).Error
	if err != nil {
		return 0, fmt.Errorf("count joined orgs: %w", err)
	}
	return count, nil
}

// UpdateMemberRole 变更某成员的角色(按 (org_id, user_id) 定位)。
func (r *gormRepository) UpdateMemberRole(ctx context.Context, orgID, userID, roleID uint64) error {
	if err := r.db.WithContext(ctx).
		Model(&model.OrgMember{}).
		Where("org_id = ? AND user_id = ?", orgID, userID).
		Update("role_id", roleID).Error; err != nil {
		return fmt.Errorf("update member role: %w", err)
	}
	return nil
}

// DeleteMember 删除一条成员关系(踢出或主动退出)。
func (r *gormRepository) DeleteMember(ctx context.Context, orgID, userID uint64) error {
	if err := r.db.WithContext(ctx).
		Where("org_id = ? AND user_id = ?", orgID, userID).
		Delete(&model.OrgMember{}).Error; err != nil {
		return fmt.Errorf("delete member: %w", err)
	}
	return nil
}

// DeleteMembersByOrg 删除 org 下所有成员(解散时级联)。
func (r *gormRepository) DeleteMembersByOrg(ctx context.Context, orgID uint64) error {
	if err := r.db.WithContext(ctx).
		Where("org_id = ?", orgID).
		Delete(&model.OrgMember{}).Error; err != nil {
		return fmt.Errorf("delete members by org: %w", err)
	}
	return nil
}
