// role.go Repository 接口中 Role 资源的实现。
package repository

import (
	"context"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/organization/model"
)

// CreateRole 创建单个角色。
func (r *gormRepository) CreateRole(ctx context.Context, role *model.OrgRole) error {
	if err := r.db.WithContext(ctx).Create(role).Error; err != nil {
		return fmt.Errorf("create role: %w", err)
	}
	return nil
}

// CreateRolesBatch 批量创建角色,用于 org 创建时种入 owner/admin/member 三条预设。
// 空切片返回 nil。
func (r *gormRepository) CreateRolesBatch(ctx context.Context, roles []*model.OrgRole) error {
	if len(roles) == 0 {
		return nil
	}
	if err := r.db.WithContext(ctx).Create(&roles).Error; err != nil {
		return fmt.Errorf("create roles batch: %w", err)
	}
	return nil
}

// FindRoleByID 按主键查找角色。
func (r *gormRepository) FindRoleByID(ctx context.Context, id uint64) (*model.OrgRole, error) {
	var role model.OrgRole
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&role).Error; err != nil {
		return nil, err
	}
	return &role, nil
}

// FindRoleByOrgName 按 (org_id, name) 查找角色。
func (r *gormRepository) FindRoleByOrgName(ctx context.Context, orgID uint64, name string) (*model.OrgRole, error) {
	var role model.OrgRole
	if err := r.db.WithContext(ctx).
		Where("org_id = ? AND name = ?", orgID, name).
		First(&role).Error; err != nil {
		return nil, err
	}
	return &role, nil
}

// ListRolesByOrg 列出 org 的所有角色(按 is_preset 降序 + ID 升序,让预设排在前面)。
func (r *gormRepository) ListRolesByOrg(ctx context.Context, orgID uint64) ([]*model.OrgRole, error) {
	var roles []*model.OrgRole
	if err := r.db.WithContext(ctx).
		Where("org_id = ?", orgID).
		Order("is_preset DESC, id ASC").
		Find(&roles).Error; err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	return roles, nil
}

// UpdateRoleFields 部分更新角色字段。
func (r *gormRepository) UpdateRoleFields(ctx context.Context, id uint64, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	if err := r.db.WithContext(ctx).
		Model(&model.OrgRole{}).
		Where("id = ?", id).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("update role fields: %w", err)
	}
	return nil
}

// DeleteRole 按 ID 删除角色。
// service 层在调用前必须确认:非预设(is_preset=false)且 CountMembersByRoleID=0。
func (r *gormRepository) DeleteRole(ctx context.Context, id uint64) error {
	if err := r.db.WithContext(ctx).
		Where("id = ?", id).
		Delete(&model.OrgRole{}).Error; err != nil {
		return fmt.Errorf("delete role: %w", err)
	}
	return nil
}

// CountMembersByRoleID 统计使用某角色的成员数量。
// 用于删除角色前的引用检查,返回 > 0 表示该角色还在被使用。
func (r *gormRepository) CountMembersByRoleID(ctx context.Context, roleID uint64) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).
		Model(&model.OrgMember{}).
		Where("role_id = ?", roleID).
		Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count members by role: %w", err)
	}
	return count, nil
}
