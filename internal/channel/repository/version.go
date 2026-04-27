package repository

import (
	"context"

	"github.com/eyrihe999-stack/Synapse/internal/channel/model"
)

func (r *gormRepository) CreateVersion(ctx context.Context, v *model.Version) error {
	return r.db.WithContext(ctx).Create(v).Error
}

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
