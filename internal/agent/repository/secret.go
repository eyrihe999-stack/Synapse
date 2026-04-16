// secret.go AgentSecret 资源的 repository 实现。
package repository

import (
	"context"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/agent/model"
)

// CreateSecret 创建一条 secret。
func (r *gormRepository) CreateSecret(ctx context.Context, s *model.AgentSecret) error {
	if err := r.db.WithContext(ctx).Create(s).Error; err != nil {
		return fmt.Errorf("create secret: %w", err)
	}
	return nil
}

// FindSecretByAgent 按 agent_id 查 secret。
func (r *gormRepository) FindSecretByAgent(ctx context.Context, agentID uint64) (*model.AgentSecret, error) {
	var s model.AgentSecret
	if err := r.db.WithContext(ctx).
		Where("agent_id = ?", agentID).
		First(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// UpdateSecret 部分更新 secret 字段(rotate 时替换 encrypted_secret / previous 等)。
func (r *gormRepository) UpdateSecret(ctx context.Context, agentID uint64, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	if err := r.db.WithContext(ctx).
		Model(&model.AgentSecret{}).
		Where("agent_id = ?", agentID).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("update secret: %w", err)
	}
	return nil
}

// DeleteSecretByAgent 删除 secret(agent 删除时级联)。
func (r *gormRepository) DeleteSecretByAgent(ctx context.Context, agentID uint64) error {
	if err := r.db.WithContext(ctx).
		Where("agent_id = ?", agentID).
		Delete(&model.AgentSecret{}).Error; err != nil {
		return fmt.Errorf("delete secret by agent: %w", err)
	}
	return nil
}
