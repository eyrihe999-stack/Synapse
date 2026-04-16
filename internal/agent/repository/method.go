// method.go AgentMethod 资源的 repository 实现。
package repository

import (
	"context"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/agent/model"
)

// CreateMethod 创建一条 method。
func (r *gormRepository) CreateMethod(ctx context.Context, m *model.AgentMethod) error {
	if err := r.db.WithContext(ctx).Create(m).Error; err != nil {
		return fmt.Errorf("create method: %w", err)
	}
	return nil
}

// CreateMethodsBatch 批量创建 method(agent 首次注册时用)。
func (r *gormRepository) CreateMethodsBatch(ctx context.Context, methods []*model.AgentMethod) error {
	if len(methods) == 0 {
		return nil
	}
	if err := r.db.WithContext(ctx).Create(methods).Error; err != nil {
		return fmt.Errorf("create methods batch: %w", err)
	}
	return nil
}

// FindMethodByID 按 ID 查 method。
func (r *gormRepository) FindMethodByID(ctx context.Context, id uint64) (*model.AgentMethod, error) {
	var m model.AgentMethod
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// FindMethodByAgentName 按 (agent_id, method_name) 查 method。
func (r *gormRepository) FindMethodByAgentName(ctx context.Context, agentID uint64, methodName string) (*model.AgentMethod, error) {
	var m model.AgentMethod
	if err := r.db.WithContext(ctx).
		Where("agent_id = ? AND method_name = ?", agentID, methodName).
		First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// ListMethodsByAgent 列出某 agent 的所有 method。
func (r *gormRepository) ListMethodsByAgent(ctx context.Context, agentID uint64) ([]*model.AgentMethod, error) {
	var out []*model.AgentMethod
	if err := r.db.WithContext(ctx).
		Where("agent_id = ?", agentID).
		Order("id ASC").
		Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list methods: %w", err)
	}
	return out, nil
}

// CountMethodsByAgent 统计 method 数量。
func (r *gormRepository) CountMethodsByAgent(ctx context.Context, agentID uint64) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).
		Model(&model.AgentMethod{}).
		Where("agent_id = ?", agentID).
		Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count methods: %w", err)
	}
	return count, nil
}

// UpdateMethodFields 部分更新 method。
func (r *gormRepository) UpdateMethodFields(ctx context.Context, id uint64, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	if err := r.db.WithContext(ctx).
		Model(&model.AgentMethod{}).
		Where("id = ?", id).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("update method: %w", err)
	}
	return nil
}

// DeleteMethod 删除 method。
func (r *gormRepository) DeleteMethod(ctx context.Context, id uint64) error {
	if err := r.db.WithContext(ctx).
		Where("id = ?", id).
		Delete(&model.AgentMethod{}).Error; err != nil {
		return fmt.Errorf("delete method: %w", err)
	}
	return nil
}

// DeleteMethodsByAgent 删除 agent 下所有 method。
func (r *gormRepository) DeleteMethodsByAgent(ctx context.Context, agentID uint64) error {
	if err := r.db.WithContext(ctx).
		Where("agent_id = ?", agentID).
		Delete(&model.AgentMethod{}).Error; err != nil {
		return fmt.Errorf("delete methods by agent: %w", err)
	}
	return nil
}
