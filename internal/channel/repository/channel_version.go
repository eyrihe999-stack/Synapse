package repository

import (
	"context"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/channel/model"
)

// AttachChannelVersion 建立 channel 和 version 的关联。幂等:重复插入撞 PK
// 复合主键冲突会 return Duplicate entry;service 层按需忽略。
func (r *gormRepository) AttachChannelVersion(ctx context.Context, channelID, versionID uint64) error {
	return r.db.WithContext(ctx).Create(&model.ChannelVersion{
		ChannelID: channelID,
		VersionID: versionID,
		CreatedAt: time.Now(),
	}).Error
}

// DetachChannelVersion 取消关联。查无记录返 nil(幂等);仅在 DB 错误时返错。
func (r *gormRepository) DetachChannelVersion(ctx context.Context, channelID, versionID uint64) error {
	return r.db.WithContext(ctx).
		Where("channel_id = ? AND version_id = ?", channelID, versionID).
		Delete(&model.ChannelVersion{}).Error
}

// ListVersionsByChannel 返回 channel 关联的所有 version(join channel_versions)。
func (r *gormRepository) ListVersionsByChannel(ctx context.Context, channelID uint64) ([]model.Version, error) {
	var vs []model.Version
	if err := r.db.WithContext(ctx).
		Joins("JOIN channel_versions cv ON cv.version_id = versions.id").
		Where("cv.channel_id = ?", channelID).
		Order("versions.created_at DESC").
		Find(&vs).Error; err != nil {
		return nil, err
	}
	return vs, nil
}
