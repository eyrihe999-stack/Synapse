// invocation.go 分区表 agent_invocations / agent_invocation_payloads 的 repository 实现。
//
// 注意:
//   - 这两张表由 partitions.go 手写 DDL 创建,GORM 视角就是普通表
//   - 为了让查询走分区裁剪,所有按时间维度的查询都必须带 started_at 条件
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/agent/model"
	"gorm.io/gorm"
)

// InsertInvocation 插入 invocation 主表。
func (r *gormRepository) InsertInvocation(ctx context.Context, inv *model.AgentInvocation) error {
	if err := r.db.WithContext(ctx).Create(inv).Error; err != nil {
		return fmt.Errorf("insert invocation: %w", err)
	}
	return nil
}

// UpdateInvocationByID 按 invocation_id + started_at 更新 invocation 行。
// startedAt 用于分区裁剪(避免全分区扫描)。
func (r *gormRepository) UpdateInvocationByID(ctx context.Context, invocationID string, startedAt time.Time, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	q := r.db.WithContext(ctx).
		Model(&model.AgentInvocation{}).
		Where("invocation_id = ?", invocationID)
	if !startedAt.IsZero() {
		// ±1 天窗口足以覆盖分区裁剪且不错过月末跨日的调用
		lo := startedAt.Add(-24 * time.Hour)
		hi := startedAt.Add(24 * time.Hour)
		q = q.Where("started_at BETWEEN ? AND ?", lo, hi)
	}
	if err := q.Updates(updates).Error; err != nil {
		return fmt.Errorf("update invocation: %w", err)
	}
	return nil
}

// FindInvocationByID 按 invocation_id 查 invocation。
// 查询覆盖最近 100 天窗口(超出基础审计保留期意义不大),可避免大范围分区扫描。
func (r *gormRepository) FindInvocationByID(ctx context.Context, invocationID string) (*model.AgentInvocation, error) {
	var inv model.AgentInvocation
	// 100 天窗口:max retention 90 天 + 缓冲
	lo := time.Now().UTC().AddDate(0, 0, -100)
	err := r.db.WithContext(ctx).
		Where("invocation_id = ? AND started_at >= ?", invocationID, lo).
		Order("started_at DESC").
		First(&inv).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("find invocation: %w", err)
	}
	return &inv, nil
}

// ListInvocationsByOrg 分页列出 org 的 invocation。
func (r *gormRepository) ListInvocationsByOrg(ctx context.Context, orgID uint64, filter InvocationFilter, page, size int) ([]*model.AgentInvocation, int64, error) {
	q := r.db.WithContext(ctx).
		Model(&model.AgentInvocation{}).
		Where("org_id = ?", orgID)
	// 分区裁剪:默认限制在 90 天窗口
	lo := filter.StartedAfter
	hi := filter.StartedBefore
	if lo.IsZero() {
		lo = time.Now().UTC().AddDate(0, 0, -90)
	}
	if hi.IsZero() {
		hi = time.Now().UTC()
	}
	q = q.Where("started_at BETWEEN ? AND ?", lo, hi)
	if filter.CallerUserID != 0 {
		q = q.Where("caller_user_id = ?", filter.CallerUserID)
	}
	if filter.AgentOwnerUserID != 0 {
		q = q.Where("agent_owner_user_id = ?", filter.AgentOwnerUserID)
	}
	if filter.AgentID != 0 {
		q = q.Where("agent_id = ?", filter.AgentID)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count invocations: %w", err)
	}
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = 20
	}
	var out []*model.AgentInvocation
	if err := q.Order("started_at DESC").
		Offset((page - 1) * size).
		Limit(size).
		Find(&out).Error; err != nil {
		return nil, 0, fmt.Errorf("list invocations: %w", err)
	}
	return out, total, nil
}

// InsertInvocationPayload 异步写 payload 记录。
func (r *gormRepository) InsertInvocationPayload(ctx context.Context, p *model.AgentInvocationPayload) error {
	if err := r.db.WithContext(ctx).Create(p).Error; err != nil {
		return fmt.Errorf("insert invocation payload: %w", err)
	}
	return nil
}

// FindInvocationPayload 按 invocation_id 查 payload。
func (r *gormRepository) FindInvocationPayload(ctx context.Context, invocationID string) (*model.AgentInvocationPayload, error) {
	var p model.AgentInvocationPayload
	// 30 天窗口(payload 保留期)
	lo := time.Now().UTC().AddDate(0, 0, -40)
	err := r.db.WithContext(ctx).
		Where("invocation_id = ? AND started_at >= ?", invocationID, lo).
		Order("started_at DESC").
		First(&p).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("find invocation payload: %w", err)
	}
	return &p, nil
}
