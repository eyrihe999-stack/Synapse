// agent.go Agent 资源的 repository 实现。
package repository

import (
	"context"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/agent/model"
)

// CreateAgent 将新的 agent 记录写入数据库。
func (r *gormRepository) CreateAgent(ctx context.Context, agent *model.Agent) error {
	if err := r.db.WithContext(ctx).Create(agent).Error; err != nil {
		return fmt.Errorf("create agent: %w", err)
	}
	return nil
}

// FindAgentByID 根据主键 ID 查找 agent。
func (r *gormRepository) FindAgentByID(ctx context.Context, id uint64) (*model.Agent, error) {
	var a model.Agent
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

// FindAgentByOwnerSlug 根据作者 ID 和 slug 查找 agent。
func (r *gormRepository) FindAgentByOwnerSlug(ctx context.Context, ownerUserID uint64, slug string) (*model.Agent, error) {
	var a model.Agent
	if err := r.db.WithContext(ctx).
		Where("owner_user_id = ? AND slug = ?", ownerUserID, slug).
		First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

// ListAgentsByOwner 列出指定用户拥有的所有 agent,按创建时间降序排列。
func (r *gormRepository) ListAgentsByOwner(ctx context.Context, ownerUserID uint64) ([]*model.Agent, error) {
	var out []*model.Agent
	if err := r.db.WithContext(ctx).
		Where("owner_user_id = ?", ownerUserID).
		Order("created_at DESC").
		Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list agents by owner: %w", err)
	}
	return out, nil
}

// UpdateAgentFields 按字段名批量更新 agent 记录。
func (r *gormRepository) UpdateAgentFields(ctx context.Context, id uint64, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	if err := r.db.WithContext(ctx).
		Model(&model.Agent{}).
		Where("id = ?", id).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("update agent fields: %w", err)
	}
	return nil
}

// DeleteAgent 根据 ID 删除 agent 记录。
func (r *gormRepository) DeleteAgent(ctx context.Context, id uint64) error {
	if err := r.db.WithContext(ctx).
		Where("id = ?", id).
		Delete(&model.Agent{}).Error; err != nil {
		return fmt.Errorf("delete agent: %w", err)
	}
	return nil
}

// DeletePublishesByAgent 删除指定 agent 的所有发布记录。
func (r *gormRepository) DeletePublishesByAgent(ctx context.Context, agentID uint64) error {
	if err := r.db.WithContext(ctx).
		Where("agent_id = ?", agentID).
		Delete(&model.AgentPublish{}).Error; err != nil {
		return fmt.Errorf("delete publishes by agent: %w", err)
	}
	return nil
}

// DeleteSessionsByAgent 删除指定 agent 的所有 session 记录。
func (r *gormRepository) DeleteSessionsByAgent(ctx context.Context, agentID uint64) error {
	if err := r.db.WithContext(ctx).
		Where("agent_id = ?", agentID).
		Delete(&model.AgentSession{}).Error; err != nil {
		return fmt.Errorf("delete sessions by agent: %w", err)
	}
	return nil
}

// DeleteMessagesByAgent 删除指定 agent 所有 session 下的消息。
func (r *gormRepository) DeleteMessagesByAgent(ctx context.Context, agentID uint64) error {
	if err := r.db.WithContext(ctx).
		Where("session_id IN (?)",
			r.db.Model(&model.AgentSession{}).Select("session_id").Where("agent_id = ?", agentID),
		).
		Delete(&model.AgentMessage{}).Error; err != nil {
		return fmt.Errorf("delete messages by agent: %w", err)
	}
	return nil
}

// DeleteMethodsByAgent 删除指定 agent 的所有方法定义。
func (r *gormRepository) DeleteMethodsByAgent(ctx context.Context, agentID uint64) error {
	if err := r.db.WithContext(ctx).Exec("DELETE FROM agent_methods WHERE agent_id = ?", agentID).Error; err != nil {
		return fmt.Errorf("delete methods by agent: %w", err)
	}
	return nil
}

// DeleteSecretsByAgent 删除指定 agent 的所有密钥记录。
func (r *gormRepository) DeleteSecretsByAgent(ctx context.Context, agentID uint64) error {
	if err := r.db.WithContext(ctx).Exec("DELETE FROM agent_secrets WHERE agent_id = ?", agentID).Error; err != nil {
		return fmt.Errorf("delete secrets by agent: %w", err)
	}
	return nil
}

// DeleteInvocationPayloadsByAgent 删除指定 agent 所有调用的 payload 记录。
func (r *gormRepository) DeleteInvocationPayloadsByAgent(ctx context.Context, agentID uint64) error {
	if err := r.db.WithContext(ctx).Exec(
		"DELETE FROM agent_invocation_payloads WHERE invocation_id IN (SELECT invocation_id FROM agent_invocations WHERE agent_id = ?)", agentID,
	).Error; err != nil {
		return fmt.Errorf("delete invocation payloads by agent: %w", err)
	}
	return nil
}

// DeleteInvocationsByAgent 删除指定 agent 的所有调用记录。
func (r *gormRepository) DeleteInvocationsByAgent(ctx context.Context, agentID uint64) error {
	if err := r.db.WithContext(ctx).Exec("DELETE FROM agent_invocations WHERE agent_id = ?", agentID).Error; err != nil {
		return fmt.Errorf("delete invocations by agent: %w", err)
	}
	return nil
}
