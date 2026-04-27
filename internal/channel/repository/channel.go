package repository

import (
	"context"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/channel/model"
)

func (r *gormRepository) CreateChannel(ctx context.Context, c *model.Channel) error {
	return r.db.WithContext(ctx).Create(c).Error
}

func (r *gormRepository) FindChannelByID(ctx context.Context, id uint64) (*model.Channel, error) {
	var c model.Channel
	if err := r.db.WithContext(ctx).First(&c, id).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// ListChannelsByProject 按 created_at DESC 分页列出 project 下 channel(含已归档)。
func (r *gormRepository) ListChannelsByProject(ctx context.Context, projectID uint64, limit, offset int) ([]model.Channel, error) {
	var cs []model.Channel
	q := r.db.WithContext(ctx).Where("project_id = ?", projectID).Order("created_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if offset > 0 {
		q = q.Offset(offset)
	}
	if err := q.Find(&cs).Error; err != nil {
		return nil, err
	}
	return cs, nil
}

// ListChannelsByPrincipal 列 principal 作为成员的 channel;JOIN channel_members 过滤。
// MCP tool list_channels 用;跨 org / 跨 project 都可返(principal 在哪些 channel
// 由 channel_members 本身决定,不再额外做 org 过滤)。
func (r *gormRepository) ListChannelsByPrincipal(ctx context.Context, principalID uint64, limit, offset int) ([]model.Channel, error) {
	var cs []model.Channel
	q := r.db.WithContext(ctx).
		Table("channels c").
		Joins("JOIN channel_members m ON m.channel_id = c.id").
		Where("m.principal_id = ?", principalID).
		Order("c.created_at DESC").
		Select("c.*")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if offset > 0 {
		q = q.Offset(offset)
	}
	if err := q.Find(&cs).Error; err != nil {
		return nil, err
	}
	return cs, nil
}

func (r *gormRepository) UpdateChannelFields(ctx context.Context, id uint64, updates map[string]any) error {
	return r.db.WithContext(ctx).Model(&model.Channel{}).Where("id = ?", id).Updates(updates).Error
}

// ArchiveOpenChannelsByProject 批量归档指定 project 下所有仍 open 的 channel。
// 见 Repository 接口注释。已归档的(status != 'open')不动。
func (r *gormRepository) ArchiveOpenChannelsByProject(ctx context.Context, projectID uint64, now time.Time) (int64, error) {
	res := r.db.WithContext(ctx).
		Model(&model.Channel{}).
		Where("project_id = ? AND status = ?", projectID, "open").
		Updates(map[string]any{
			"status":      "archived",
			"archived_at": now,
		})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}
