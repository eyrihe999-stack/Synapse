package repository

import (
	"context"

	"github.com/eyrihe999-stack/Synapse/internal/pm"
	"github.com/eyrihe999-stack/Synapse/internal/pm/model"
)

// CreateVersion 插入新 version。GORM 自动填 CreatedAt / UpdatedAt(如果有)。
func (r *gormRepository) CreateVersion(ctx context.Context, v *model.Version) error {
	return r.db.WithContext(ctx).Create(v).Error
}

// FindVersionByID 返回 version;查无返 gorm.ErrRecordNotFound。
func (r *gormRepository) FindVersionByID(ctx context.Context, id uint64) (*model.Version, error) {
	var v model.Version
	if err := r.db.WithContext(ctx).First(&v, id).Error; err != nil {
		return nil, err
	}
	return &v, nil
}

// ListVersionsByProject 按 created_at DESC 列出 project 下所有 version。
// 不分页 —— 单 project 下 version 数量可控(里程碑级粒度)。
func (r *gormRepository) ListVersionsByProject(ctx context.Context, projectID uint64) ([]model.Version, error) {
	var vs []model.Version
	if err := r.db.WithContext(ctx).
		Where("project_id = ?", projectID).
		Order("created_at DESC").
		Find(&vs).Error; err != nil {
		return nil, err
	}
	return vs, nil
}

// UpdateVersionFields 改 version 字段。service 层负责构造 updates map。
func (r *gormRepository) UpdateVersionFields(ctx context.Context, id uint64, updates map[string]any) error {
	return r.db.WithContext(ctx).Model(&model.Version{}).Where("id = ?", id).Updates(updates).Error
}

// FindBacklogVersion 找 project 的 system Backlog version。命中 (project_id,
// is_system=true, name=BacklogVersionName)。查无返 gorm.ErrRecordNotFound。
func (r *gormRepository) FindBacklogVersion(ctx context.Context, projectID uint64) (*model.Version, error) {
	var v model.Version
	if err := r.db.WithContext(ctx).
		Where("project_id = ? AND is_system = ? AND name = ?", projectID, true, pm.BacklogVersionName).
		First(&v).Error; err != nil {
		return nil, err
	}
	return &v, nil
}

// CountActiveVersionByName 同 project 下按名字查重(含已 cancelled —— 因为
// uk_versions_project_name 不区分状态,DDL 层就要求名字全局唯一)。
func (r *gormRepository) CountActiveVersionByName(ctx context.Context, projectID uint64, name string) (int64, error) {
	var n int64
	if err := r.db.WithContext(ctx).Model(&model.Version{}).
		Where("project_id = ? AND name = ?", projectID, name).
		Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}
