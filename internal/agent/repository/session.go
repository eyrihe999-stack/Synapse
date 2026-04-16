// session.go Session + Message 资源的 repository 实现。
package repository

import (
	"context"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/agent/model"
)

// ─── Session ────────────────────────────────────────────────────────────────

// CreateSession 创建一条对话 session 记录。
func (r *gormRepository) CreateSession(ctx context.Context, s *model.AgentSession) error {
	if err := r.db.WithContext(ctx).Create(s).Error; err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// FindSessionByID 根据 session UUID 查找 session。
func (r *gormRepository) FindSessionByID(ctx context.Context, sessionID string) (*model.AgentSession, error) {
	var s model.AgentSession
	if err := r.db.WithContext(ctx).Where("session_id = ?", sessionID).First(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// ListSessionsByUserAgent 分页列出指定用户在指定 agent 下的 session,按更新时间降序排列。
func (r *gormRepository) ListSessionsByUserAgent(ctx context.Context, orgID, userID, agentID uint64, page, size int) ([]*model.AgentSession, int64, error) {
	q := r.db.WithContext(ctx).
		Model(&model.AgentSession{}).
		Where("org_id = ? AND user_id = ? AND agent_id = ?", orgID, userID, agentID)

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count sessions: %w", err)
	}
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = 20
	}
	var out []*model.AgentSession
	if err := q.Order("updated_at DESC").
		Offset((page - 1) * size).
		Limit(size).
		Find(&out).Error; err != nil {
		return nil, 0, fmt.Errorf("list sessions: %w", err)
	}
	return out, total, nil
}

// UpdateSessionTitle 更新 session 的标题。
func (r *gormRepository) UpdateSessionTitle(ctx context.Context, sessionID string, title string) error {
	if err := r.db.WithContext(ctx).
		Model(&model.AgentSession{}).
		Where("session_id = ?", sessionID).
		Update("title", title).Error; err != nil {
		return fmt.Errorf("update session title: %w", err)
	}
	return nil
}

// DeleteSession 删除指定 session 记录。
func (r *gormRepository) DeleteSession(ctx context.Context, sessionID string) error {
	if err := r.db.WithContext(ctx).
		Where("session_id = ?", sessionID).
		Delete(&model.AgentSession{}).Error; err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// ─── Message ────────────────────────────────────────────────────────────────

// CreateMessage 创建一条对话消息记录。
func (r *gormRepository) CreateMessage(ctx context.Context, m *model.AgentMessage) error {
	if err := r.db.WithContext(ctx).Create(m).Error; err != nil {
		return fmt.Errorf("create message: %w", err)
	}
	return nil
}

// GetRecentMessages 获取 session 最近 maxRounds 轮(每轮 user+assistant = 2 条)消息。
// 按 created_at DESC 取 limit 条,然后在内存中反转为正序。
func (r *gormRepository) GetRecentMessages(ctx context.Context, sessionID string, maxRounds int) ([]*model.AgentMessage, error) {
	limit := maxRounds * 2
	var out []*model.AgentMessage
	if err := r.db.WithContext(ctx).
		Where("session_id = ?", sessionID).
		Order("created_at DESC, id DESC").
		Limit(limit).
		Find(&out).Error; err != nil {
		return nil, fmt.Errorf("get recent messages: %w", err)
	}
	// 反转为时间正序
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// ListMessagesBySession 分页列出 session 内的消息,按时间正序排列。
func (r *gormRepository) ListMessagesBySession(ctx context.Context, sessionID string, page, size int) ([]*model.AgentMessage, int64, error) {
	q := r.db.WithContext(ctx).
		Model(&model.AgentMessage{}).
		Where("session_id = ?", sessionID)

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count messages: %w", err)
	}
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = 20
	}
	var out []*model.AgentMessage
	if err := q.Order("created_at ASC, id ASC").
		Offset((page - 1) * size).
		Limit(size).
		Find(&out).Error; err != nil {
		return nil, 0, fmt.Errorf("list messages: %w", err)
	}
	return out, total, nil
}

// DeleteMessagesBySession 删除指定 session 下的所有消息。
func (r *gormRepository) DeleteMessagesBySession(ctx context.Context, sessionID string) error {
	if err := r.db.WithContext(ctx).
		Where("session_id = ?", sessionID).
		Delete(&model.AgentMessage{}).Error; err != nil {
		return fmt.Errorf("delete messages by session: %w", err)
	}
	return nil
}
