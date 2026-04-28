package repository

import (
	"context"

	"github.com/eyrihe999-stack/Synapse/internal/pm/model"
)

// CreateProjectKBRef 写新 KB 挂载记录。撞 uk_project_kb_refs_uniq 时返 gorm
// 的 Duplicate 错误,service 层翻成 ErrProjectKBRefDuplicated。
func (r *gormRepository) CreateProjectKBRef(ctx context.Context, ref *model.ProjectKBRef) error {
	return r.db.WithContext(ctx).Create(ref).Error
}

// DeleteProjectKBRef 按 id 硬删。删不存在的行返 nil(GORM 行为)。
func (r *gormRepository) DeleteProjectKBRef(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Delete(&model.ProjectKBRef{}, id).Error
}

// FindProjectKBRefByID 按主键查;查无返 gorm.ErrRecordNotFound。
func (r *gormRepository) FindProjectKBRefByID(ctx context.Context, id uint64) (*model.ProjectKBRef, error) {
	var ref model.ProjectKBRef
	if err := r.db.WithContext(ctx).First(&ref, id).Error; err != nil {
		return nil, err
	}
	return &ref, nil
}

// ListProjectKBRefsByProject 列 project 下所有 KB 挂载,按 attached_at DESC。
func (r *gormRepository) ListProjectKBRefsByProject(ctx context.Context, projectID uint64) ([]model.ProjectKBRef, error) {
	var refs []model.ProjectKBRef
	if err := r.db.WithContext(ctx).
		Where("project_id = ?", projectID).
		Order("attached_at DESC").
		Find(&refs).Error; err != nil {
		return nil, err
	}
	return refs, nil
}

// FindProjectKBRefByTarget 按 (project_id, source_id, doc_id) 查重(应用层预检
// 重复挂)。二选一时另一个传 0;查无返 gorm.ErrRecordNotFound。
func (r *gormRepository) FindProjectKBRefByTarget(ctx context.Context, projectID, sourceID, docID uint64) (*model.ProjectKBRef, error) {
	var ref model.ProjectKBRef
	if err := r.db.WithContext(ctx).
		Where("project_id = ? AND kb_source_id = ? AND kb_document_id = ?", projectID, sourceID, docID).
		First(&ref).Error; err != nil {
		return nil, err
	}
	return &ref, nil
}
