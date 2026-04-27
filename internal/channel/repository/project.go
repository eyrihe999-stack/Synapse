package repository

import (
	"context"

	"github.com/eyrihe999-stack/Synapse/internal/channel/model"
)

// CreateProject 插入新 project。GORM 自动填 CreatedAt / UpdatedAt。
func (r *gormRepository) CreateProject(ctx context.Context, p *model.Project) error {
	return r.db.WithContext(ctx).Create(p).Error
}

// FindProjectByID 返回指定 project;查无记录返 gorm.ErrRecordNotFound。
func (r *gormRepository) FindProjectByID(ctx context.Context, id uint64) (*model.Project, error) {
	var p model.Project
	if err := r.db.WithContext(ctx).First(&p, id).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// ListProjectsByOrg 按 created_at DESC 分页列出 org 下的所有 project(含已归档)。
// service 层按需过滤归档状态。
func (r *gormRepository) ListProjectsByOrg(ctx context.Context, orgID uint64, limit, offset int) ([]model.Project, error) {
	var ps []model.Project
	q := r.db.WithContext(ctx).Where("org_id = ?", orgID).Order("created_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if offset > 0 {
		q = q.Offset(offset)
	}
	if err := q.Find(&ps).Error; err != nil {
		return nil, err
	}
	return ps, nil
}

// UpdateProjectFields 更新指定字段;调用方构造 updates map,避免零值覆盖。
func (r *gormRepository) UpdateProjectFields(ctx context.Context, id uint64, updates map[string]any) error {
	return r.db.WithContext(ctx).Model(&model.Project{}).Where("id = ?", id).Updates(updates).Error
}

// CountActiveProjectByName 在 org 下统计名字相同且未归档的项目条数。
//
// 本应由 uk_projects_org_name_active 唯一索引在 INSERT 时兜底,但这里提供
// 应用层预检以返回友好的业务错误码(否则就是 gorm 的 "Error 1062: Duplicate entry")。
func (r *gormRepository) CountActiveProjectByName(ctx context.Context, orgID uint64, name string) (int64, error) {
	var n int64
	if err := r.db.WithContext(ctx).Model(&model.Project{}).
		Where("org_id = ? AND name = ? AND archived_at IS NULL", orgID, name).
		Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}
