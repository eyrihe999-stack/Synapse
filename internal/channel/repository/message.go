package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/channel/model"
)

// CreateMessage 插入一条 channel 消息。调用方应在事务里 chain `AddMessageMentions`。
func (r *gormRepository) CreateMessage(ctx context.Context, m *model.ChannelMessage) error {
	if err := r.db.WithContext(ctx).Create(m).Error; err != nil {
		return fmt.Errorf("create channel message: %w", err)
	}
	return nil
}

// AddMessageMentions 批量写 mention 关联(message_id, principal_id)。principalIDs 为空即 noop。
// 已存在的复合 PK 会被 GORM 的 INSERT 冲突回错;调用方应保证去重(或用 INSERT IGNORE,
// 当前 GORM 不方便直接表达,暂靠应用层去重)。
func (r *gormRepository) AddMessageMentions(ctx context.Context, messageID uint64, principalIDs []uint64) error {
	if len(principalIDs) == 0 {
		return nil
	}
	rows := make([]model.ChannelMessageMention, 0, len(principalIDs))
	seen := make(map[uint64]struct{}, len(principalIDs))
	for _, pid := range principalIDs {
		if pid == 0 {
			continue
		}
		if _, dup := seen[pid]; dup {
			continue
		}
		seen[pid] = struct{}{}
		rows = append(rows, model.ChannelMessageMention{
			MessageID:   messageID,
			PrincipalID: pid,
		})
	}
	if len(rows) == 0 {
		return nil
	}
	if err := r.db.WithContext(ctx).Create(&rows).Error; err != nil {
		return fmt.Errorf("create channel message mentions: %w", err)
	}
	return nil
}

// ListMessages 按 id 倒序拉 channel 的消息;beforeID=0 从头,否则拉 id<beforeID 的。
// limit 上限硬约束 1..100,超界由调用方截断或本函数夹紧。
func (r *gormRepository) ListMessages(ctx context.Context, channelID uint64, beforeID uint64, limit int) ([]model.ChannelMessage, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	var rows []model.ChannelMessage
	q := r.db.WithContext(ctx).
		Where("channel_id = ?", channelID)
	if beforeID > 0 {
		q = q.Where("id < ?", beforeID)
	}
	if err := q.Order("id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list channel messages: %w", err)
	}
	return rows, nil
}

// ListMentionsByMessages 批量拉 mentions 关联,用于 list 拼 response 时一次 fetch all。
func (r *gormRepository) ListMentionsByMessages(ctx context.Context, messageIDs []uint64) ([]model.ChannelMessageMention, error) {
	if len(messageIDs) == 0 {
		return nil, nil
	}
	var rows []model.ChannelMessageMention
	if err := r.db.WithContext(ctx).
		Where("message_id IN ?", messageIDs).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list message mentions: %w", err)
	}
	return rows, nil
}

// MentionRow JOIN channel_message_mentions + channel_messages 的扁平结果,
// 用于跨 channel 列出"我被 @ 的消息"。
type MentionRow struct {
	MessageID         uint64 `gorm:"column:message_id"`
	ChannelID         uint64 `gorm:"column:channel_id"`
	AuthorPrincipalID uint64 `gorm:"column:author_principal_id"`
	Body              string `gorm:"column:body"`
	Kind              string `gorm:"column:kind"`
	CreatedAt         time.Time `gorm:"column:created_at"`
}

// ListMentionsByPrincipals 跨 channel 列出**任一 principal_id 在 candidates 中**
// 被 @ 的消息(按 message_id DESC)。candidates 通常是 [callerPrincipal, ownerUserPrincipal]。
//
// 参数:
//   - candidatePrincipalIDs: principal 候选列表(去重由调用方保证);空返 nil
//   - sinceMessageID: 0 → 从最新拉;>0 → 拉 message_id > sinceMessageID 的(增量"自上次后")
//   - limit: 1..200 范围,超界本函数夹紧
//
// 一条消息同时 @ 了 candidates 里多人时,DISTINCT 去重保证只返一行。
//
// 不做 channel 状态过滤(归档 channel 里的 mention 也返回,符合 inbox 的"历史回顾"语义)。
// 权限层不在这里做 —— 调用方保证 candidates 都属于 caller 自己。
//
// 返回:[]MentionRow,按 message_id DESC 排;空返 nil。
func (r *gormRepository) ListMentionsByPrincipals(ctx context.Context, candidatePrincipalIDs []uint64, sinceMessageID uint64, limit int) ([]MentionRow, error) {
	if len(candidatePrincipalIDs) == 0 {
		return nil, nil
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	q := r.db.WithContext(ctx).
		Table("channel_message_mentions AS m").
		Select("DISTINCT m.message_id AS message_id, c.channel_id AS channel_id, " +
			"c.author_principal_id AS author_principal_id, c.body AS body, " +
			"c.kind AS kind, c.created_at AS created_at").
		Joins("JOIN channel_messages AS c ON c.id = m.message_id").
		Where("m.principal_id IN ?", candidatePrincipalIDs)
	if sinceMessageID > 0 {
		q = q.Where("m.message_id > ?", sinceMessageID)
	}
	var rows []MentionRow
	if err := q.Order("m.message_id DESC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list mentions by principals: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows, nil
}

// AddReaction 插入一行 reaction;已存在同 PK 视 duplicate(service 层决定幂等)。
func (r *gormRepository) AddReaction(ctx context.Context, row *model.ChannelMessageReaction) error {
	return r.db.WithContext(ctx).Create(row).Error
}

// RemoveReaction 按复合 PK 删除一行;不存在时返 gorm.ErrRecordNotFound(通过 RowsAffected 判定)。
func (r *gormRepository) RemoveReaction(ctx context.Context, messageID, principalID uint64, emoji string) error {
	res := r.db.WithContext(ctx).
		Where("message_id = ? AND principal_id = ? AND emoji = ?", messageID, principalID, emoji).
		Delete(&model.ChannelMessageReaction{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ListReactionsByMessages 批量拿 N 条消息的所有 reactions。按 (message_id, emoji) 排序
// 给前端聚合稳定输出。
func (r *gormRepository) ListReactionsByMessages(ctx context.Context, messageIDs []uint64) ([]model.ChannelMessageReaction, error) {
	if len(messageIDs) == 0 {
		return nil, nil
	}
	var rows []model.ChannelMessageReaction
	if err := r.db.WithContext(ctx).
		Where("message_id IN ?", messageIDs).
		Order("message_id, emoji, created_at").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list message reactions: %w", err)
	}
	return rows, nil
}

// FindMessageBySourceEventID 按 source_event_id UNIQUE 列查 system_event 消息。
// 找不到返 (nil, nil),系统消息 consumer 据此判断是否已写过(幂等)。
func (r *gormRepository) FindMessageBySourceEventID(ctx context.Context, sourceEventID string) (*model.ChannelMessage, error) {
	if sourceEventID == "" {
		return nil, nil
	}
	var m model.ChannelMessage
	err := r.db.WithContext(ctx).
		Where("source_event_id = ?", sourceEventID).
		Take(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find message by source_event_id: %w", err)
	}
	return &m, nil
}

// FindMessageByID 按主键查消息。找不到返 (nil, nil),让 service 层决定要不要当错误。
func (r *gormRepository) FindMessageByID(ctx context.Context, messageID uint64) (*model.ChannelMessage, error) {
	var m model.ChannelMessage
	err := r.db.WithContext(ctx).Where("id = ?", messageID).Take(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find message by id: %w", err)
	}
	return &m, nil
}

// FindMessageInChannel 按 (channel_id, id) 查单条。返回 gorm.ErrRecordNotFound
// 的路径由 service 层翻译成哨兵错误。
func (r *gormRepository) FindMessageInChannel(ctx context.Context, channelID, messageID uint64) (*model.ChannelMessage, error) {
	var m model.ChannelMessage
	if err := r.db.WithContext(ctx).
		Where("channel_id = ? AND id = ?", channelID, messageID).
		Take(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// FindMessagesByIDsInChannel 批量拿同 channel 的若干 message(按 id 一次性 IN 查询),
// 给 list 接口拼 reply 预览用。返回顺序无保证,调用方自己 by-id 建 map。
func (r *gormRepository) FindMessagesByIDsInChannel(ctx context.Context, channelID uint64, messageIDs []uint64) ([]model.ChannelMessage, error) {
	if len(messageIDs) == 0 {
		return nil, nil
	}
	var rows []model.ChannelMessage
	if err := r.db.WithContext(ctx).
		Where("channel_id = ? AND id IN ?", channelID, messageIDs).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("find messages by ids: %w", err)
	}
	return rows, nil
}
