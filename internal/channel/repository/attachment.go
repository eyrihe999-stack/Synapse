// attachment.go channel 级附件(图片等)的数据访问。
//
// 设计:
//   - 单表 channel_attachments,无 versions 概念(图片不存版本历史 — 改图就上传新图)
//   - 去重:(channel_id, sha256) UNIQUE — 同 channel 重传同字节命中已有行
//   - 读路径不过滤 deleted_at(读历史 markdown 引用要看已删 attachment;
//     业务层按需自己判断)
package repository

import (
	"context"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/channel/model"
)

// CreateChannelAttachment 写一条 attachment 行;撞 UNIQUE(channel_id, sha256) 返
// gorm.ErrDuplicatedKey,service 层翻成"已有 → 复用现有行"。
func (r *gormRepository) CreateChannelAttachment(ctx context.Context, a *model.ChannelAttachment) error {
	return r.db.WithContext(ctx).Create(a).Error
}

// FindChannelAttachmentByID 按 ID 查 attachment(不过滤 deleted_at)。
// 调用方按需自己判 a.DeletedAt。
func (r *gormRepository) FindChannelAttachmentByID(ctx context.Context, id uint64) (*model.ChannelAttachment, error) {
	var a model.ChannelAttachment
	if err := r.db.WithContext(ctx).First(&a, id).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

// FindChannelAttachmentByChannelAndHash 按 (channel_id, sha256) 查已有行。
// commit 阶段用:命中 → 复用,否则写新行。
// 找不到返 (nil, gorm.ErrRecordNotFound)。
func (r *gormRepository) FindChannelAttachmentByChannelAndHash(ctx context.Context, channelID uint64, sha string) (*model.ChannelAttachment, error) {
	var a model.ChannelAttachment
	err := r.db.WithContext(ctx).
		Where("channel_id = ? AND sha256 = ?", channelID, sha).
		Take(&a).Error
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// SoftDeleteChannelAttachment 设 deleted_at;重复软删幂等(WHERE deleted_at IS NULL)。
func (r *gormRepository) SoftDeleteChannelAttachment(ctx context.Context, id uint64, now time.Time) error {
	return r.db.WithContext(ctx).Model(&model.ChannelAttachment{}).
		Where("id = ? AND deleted_at IS NULL", id).
		Update("deleted_at", now).Error
}
