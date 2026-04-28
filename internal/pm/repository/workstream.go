package repository

import (
	"context"

	"github.com/eyrihe999-stack/Synapse/internal/pm/model"
)

// CreateWorkstream 插入新 workstream。
func (r *gormRepository) CreateWorkstream(ctx context.Context, w *model.Workstream) error {
	return r.db.WithContext(ctx).Create(w).Error
}

// FindWorkstreamByID 查 workstream;查无返 gorm.ErrRecordNotFound。
func (r *gormRepository) FindWorkstreamByID(ctx context.Context, id uint64) (*model.Workstream, error) {
	var w model.Workstream
	if err := r.db.WithContext(ctx).First(&w, id).Error; err != nil {
		return nil, err
	}
	return &w, nil
}

// ListWorkstreamsByInitiative 列 initiative 下的所有 workstream(含归档),按
// created_at DESC。
func (r *gormRepository) ListWorkstreamsByInitiative(ctx context.Context, initiativeID uint64, limit, offset int) ([]model.Workstream, error) {
	return r.listWorkstreams(ctx, "initiative_id = ?", initiativeID, limit, offset)
}

// ListWorkstreamsByVersion 列 version 下的所有 workstream(交付盘点视角)。
func (r *gormRepository) ListWorkstreamsByVersion(ctx context.Context, versionID uint64, limit, offset int) ([]model.Workstream, error) {
	return r.listWorkstreams(ctx, "version_id = ?", versionID, limit, offset)
}

// ListWorkstreamsByProject 列 project 下的所有 workstream(全局视图)。
func (r *gormRepository) ListWorkstreamsByProject(ctx context.Context, projectID uint64, limit, offset int) ([]model.Workstream, error) {
	return r.listWorkstreams(ctx, "project_id = ?", projectID, limit, offset)
}

// listWorkstreams 共享分页 + 排序逻辑。
func (r *gormRepository) listWorkstreams(ctx context.Context, where string, val any, limit, offset int) ([]model.Workstream, error) {
	var ws []model.Workstream
	q := r.db.WithContext(ctx).Where(where, val).Order("created_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if offset > 0 {
		q = q.Offset(offset)
	}
	if err := q.Find(&ws).Error; err != nil {
		return nil, err
	}
	return ws, nil
}

// UpdateWorkstreamFields 改 workstream 字段。
func (r *gormRepository) UpdateWorkstreamFields(ctx context.Context, id uint64, updates map[string]any) error {
	return r.db.WithContext(ctx).Model(&model.Workstream{}).Where("id = ?", id).Updates(updates).Error
}
