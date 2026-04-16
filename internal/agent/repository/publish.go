// publish.go AgentPublish 资源的 repository 实现。
package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/agent/model"
)

// CreatePublish 创建发布记录。
func (r *gormRepository) CreatePublish(ctx context.Context, p *model.AgentPublish) error {
	if err := r.db.WithContext(ctx).Create(p).Error; err != nil {
		return fmt.Errorf("create publish: %w", err)
	}
	return nil
}

// FindPublishByID 按 ID 查 publish。
func (r *gormRepository) FindPublishByID(ctx context.Context, id uint64) (*model.AgentPublish, error) {
	var p model.AgentPublish
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// FindActivePublish 查 (agent_id, org_id) 的 active 记录(pending 或 approved)。
// 用于注册时检查 "不能重复发布"。
func (r *gormRepository) FindActivePublish(ctx context.Context, agentID, orgID uint64) (*model.AgentPublish, error) {
	var p model.AgentPublish
	if err := r.db.WithContext(ctx).
		Where("agent_id = ? AND org_id = ? AND status IN ?",
			agentID, orgID,
			[]string{model.PublishStatusPending, model.PublishStatusApproved}).
		First(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// ListPublishesByOrg 分页列出 org 的 publish。
// status="" 表示不过滤。
func (r *gormRepository) ListPublishesByOrg(ctx context.Context, orgID uint64, status string, page, size int) ([]*model.AgentPublish, int64, error) {
	q := r.db.WithContext(ctx).
		Model(&model.AgentPublish{}).
		Where("org_id = ?", orgID)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count publishes: %w", err)
	}
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = 20
	}
	var out []*model.AgentPublish
	if err := q.Order("created_at DESC").
		Offset((page - 1) * size).
		Limit(size).
		Find(&out).Error; err != nil {
		return nil, 0, fmt.Errorf("list publishes: %w", err)
	}
	return out, total, nil
}

// ListActivePublishesByAgent 列出某 agent 所有 active 绑定。
func (r *gormRepository) ListActivePublishesByAgent(ctx context.Context, agentID uint64) ([]*model.AgentPublish, error) {
	var out []*model.AgentPublish
	if err := r.db.WithContext(ctx).
		Where("agent_id = ? AND status IN ?",
			agentID,
			[]string{model.PublishStatusPending, model.PublishStatusApproved}).
		Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list active publishes by agent: %w", err)
	}
	return out, nil
}

// ListActivePublishesByAuthorOrg 列出 (org_id, author_user_id) 下所有 active publish。
// 供成员离开 hook 使用。
func (r *gormRepository) ListActivePublishesByAuthorOrg(ctx context.Context, orgID, authorUserID uint64) ([]*model.AgentPublish, error) {
	var out []*model.AgentPublish
	if err := r.db.WithContext(ctx).
		Where("org_id = ? AND submitted_by_user_id = ? AND status IN ?",
			orgID, authorUserID,
			[]string{model.PublishStatusPending, model.PublishStatusApproved}).
		Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list active publishes by author/org: %w", err)
	}
	return out, nil
}

// UpdatePublishFields 部分更新 publish 字段。
func (r *gormRepository) UpdatePublishFields(ctx context.Context, id uint64, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	if err := r.db.WithContext(ctx).
		Model(&model.AgentPublish{}).
		Where("id = ?", id).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("update publish: %w", err)
	}
	return nil
}

// RevokePublishesByAuthorOrg 批量标记 (org_id, author_user_id) 所有 active publish 为 revoked。
func (r *gormRepository) RevokePublishesByAuthorOrg(ctx context.Context, orgID, authorUserID uint64, reason string, now time.Time) (int64, error) {
	res := r.db.WithContext(ctx).
		Model(&model.AgentPublish{}).
		Where("org_id = ? AND submitted_by_user_id = ? AND status IN ?",
			orgID, authorUserID,
			[]string{model.PublishStatusPending, model.PublishStatusApproved}).
		Updates(map[string]any{
			"status":         model.PublishStatusRevoked,
			"revoked_at":     &now,
			"revoked_reason": reason,
		})
	if res.Error != nil {
		return 0, fmt.Errorf("revoke publishes by author/org: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// RevokePublishesByOrg 批量标记 org 下所有 active publish 为 revoked(org 解散)。
func (r *gormRepository) RevokePublishesByOrg(ctx context.Context, orgID uint64, reason string, now time.Time) (int64, error) {
	res := r.db.WithContext(ctx).
		Model(&model.AgentPublish{}).
		Where("org_id = ? AND status IN ?",
			orgID,
			[]string{model.PublishStatusPending, model.PublishStatusApproved}).
		Updates(map[string]any{
			"status":         model.PublishStatusRevoked,
			"revoked_at":     &now,
			"revoked_reason": reason,
		})
	if res.Error != nil {
		return 0, fmt.Errorf("revoke publishes by org: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// ListAgentIDsByOrg 列出 org 内所有 approved 的 agent_id 去重集合。
func (r *gormRepository) ListAgentIDsByOrg(ctx context.Context, orgID uint64) ([]uint64, error) {
	var out []uint64
	if err := r.db.WithContext(ctx).
		Model(&model.AgentPublish{}).
		Where("org_id = ? AND status = ?", orgID, model.PublishStatusApproved).
		Distinct("agent_id").
		Pluck("agent_id", &out).Error; err != nil {
		return nil, fmt.Errorf("list agent ids by org: %w", err)
	}
	return out, nil
}
