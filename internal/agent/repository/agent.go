// agent.go Agent 资源的 repository 实现。
package repository

import (
	"context"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/agent/model"
)

// CreateAgent 创建一条 agent 记录。
func (r *gormRepository) CreateAgent(ctx context.Context, agent *model.Agent) error {
	if err := r.db.WithContext(ctx).Create(agent).Error; err != nil {
		return fmt.Errorf("create agent: %w", err)
	}
	return nil
}

// FindAgentByID 按主键查 agent。
func (r *gormRepository) FindAgentByID(ctx context.Context, id uint64) (*model.Agent, error) {
	var a model.Agent
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

// FindAgentByOwnerSlug 按 (owner_user_id, slug) 查 agent。
func (r *gormRepository) FindAgentByOwnerSlug(ctx context.Context, ownerUserID uint64, slug string) (*model.Agent, error) {
	var a model.Agent
	if err := r.db.WithContext(ctx).
		Where("owner_user_id = ? AND slug = ?", ownerUserID, slug).
		First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

// ListAgentsByOwner 列出作者的所有 agent。
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

// ListAgentsByIDs 批量按 ID 查 agent。
func (r *gormRepository) ListAgentsByIDs(ctx context.Context, ids []uint64) ([]*model.Agent, error) {
	if len(ids) == 0 {
		return []*model.Agent{}, nil
	}
	var out []*model.Agent
	if err := r.db.WithContext(ctx).
		Where("id IN ?", ids).
		Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list agents by ids: %w", err)
	}
	return out, nil
}

// UpdateAgentFields 部分更新 agent。
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

// DeleteAgent 删除 agent 记录(不级联,调用方在事务里显式处理 method/secret/publish)。
func (r *gormRepository) DeleteAgent(ctx context.Context, id uint64) error {
	if err := r.db.WithContext(ctx).
		Where("id = ?", id).
		Delete(&model.Agent{}).Error; err != nil {
		return fmt.Errorf("delete agent: %w", err)
	}
	return nil
}

// ListActiveAgentsForHealthCheck 取 status=active 的 agent 列表供健康检查使用。
// limit=0 表示不限制。
func (r *gormRepository) ListActiveAgentsForHealthCheck(ctx context.Context, limit int) ([]*model.Agent, error) {
	q := r.db.WithContext(ctx).Where("status = ?", model.AgentStatusActive)
	if limit > 0 {
		q = q.Limit(limit)
	}
	var out []*model.Agent
	if err := q.Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list active agents: %w", err)
	}
	return out, nil
}
