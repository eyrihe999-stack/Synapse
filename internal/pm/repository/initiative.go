package repository

import (
	"context"

	"github.com/eyrihe999-stack/Synapse/internal/pm"
	"github.com/eyrihe999-stack/Synapse/internal/pm/model"
)

// CreateInitiative 插入新 initiative。
func (r *gormRepository) CreateInitiative(ctx context.Context, i *model.Initiative) error {
	return r.db.WithContext(ctx).Create(i).Error
}

// FindInitiativeByID 返回 initiative;查无返 gorm.ErrRecordNotFound。
func (r *gormRepository) FindInitiativeByID(ctx context.Context, id uint64) (*model.Initiative, error) {
	var i model.Initiative
	if err := r.db.WithContext(ctx).First(&i, id).Error; err != nil {
		return nil, err
	}
	return &i, nil
}

// ListInitiativesByProject 按 created_at DESC 列出 project 下的所有 initiative
// (含已归档)。service 层按需过滤归档状态。
func (r *gormRepository) ListInitiativesByProject(ctx context.Context, projectID uint64, limit, offset int) ([]model.Initiative, error) {
	var is []model.Initiative
	q := r.db.WithContext(ctx).Where("project_id = ?", projectID).Order("created_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if offset > 0 {
		q = q.Offset(offset)
	}
	if err := q.Find(&is).Error; err != nil {
		return nil, err
	}
	return is, nil
}

// UpdateInitiativeFields 更新指定字段;调用方构造 updates map 避免零值覆盖。
func (r *gormRepository) UpdateInitiativeFields(ctx context.Context, id uint64, updates map[string]any) error {
	return r.db.WithContext(ctx).Model(&model.Initiative{}).Where("id = ?", id).Updates(updates).Error
}

// CountActiveInitiativeByName 同 project 下统计名字相同且未归档的条数,应用层
// 预检返友好错误码;DB 层 uk_initiatives_project_name_active 兜底。
func (r *gormRepository) CountActiveInitiativeByName(ctx context.Context, projectID uint64, name string) (int64, error) {
	var n int64
	if err := r.db.WithContext(ctx).Model(&model.Initiative{}).
		Where("project_id = ? AND name = ? AND archived_at IS NULL", projectID, name).
		Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

// FindDefaultInitiative 找 project 的 system Default initiative。
func (r *gormRepository) FindDefaultInitiative(ctx context.Context, projectID uint64) (*model.Initiative, error) {
	var i model.Initiative
	if err := r.db.WithContext(ctx).
		Where("project_id = ? AND is_system = ? AND name = ?", projectID, true, pm.DefaultInitiativeName).
		First(&i).Error; err != nil {
		return nil, err
	}
	return &i, nil
}

// CountActiveWorkstreamsByInitiative 统计 initiative 下未归档(archived_at IS NULL)
// + 状态非 cancelled / done 的 workstream 数。给"删 initiative"前置守卫用。
func (r *gormRepository) CountActiveWorkstreamsByInitiative(ctx context.Context, initiativeID uint64) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&model.Workstream{}).
		Where("initiative_id = ? AND archived_at IS NULL AND status NOT IN ?",
			initiativeID, []string{pm.WorkstreamStatusCancelled, pm.WorkstreamStatusDone}).
		Count(&n).Error
	if err != nil {
		return 0, err
	}
	return n, nil
}
