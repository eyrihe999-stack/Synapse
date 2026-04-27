package repository

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/agentsys/model"
)

// UsageRepo llm_usage 表操作接口。
//
// 两个方法分别对应写入和 rate limit 查询;刻意不暴露 ListByOrg / admin 视图,
// 避免把计费数据随便暴露到未鉴权的地方。
type UsageRepo interface {
	Insert(ctx context.Context, u *model.LLMUsage) error
	// SumCostSince 统计 operatingOrgID 自 since 起的 cost_usd 总和(美元)。
	// 用于 orchestrator 响应前的预算检查:since=今天零点(UTC),返回值 < cfg.DailyBudget 才放行。
	SumCostSince(ctx context.Context, operatingOrgID uint64, since time.Time) (float64, error)
}

type usageRepo struct {
	db *gorm.DB
}

// NewUsageRepo 构造 UsageRepo 实例。
func NewUsageRepo(db *gorm.DB) UsageRepo {
	return &usageRepo{db: db}
}

// Insert 写入一次 LLM 调用的使用量。
func (r *usageRepo) Insert(ctx context.Context, u *model.LLMUsage) error {
	if err := r.db.WithContext(ctx).Create(u).Error; err != nil {
		return fmt.Errorf("insert llm usage: %w", err)
	}
	return nil
}

// SumCostSince 对 (operating_org_id, created_at >= since) 聚合 cost_usd。
// 空集合时返回 0(不是 NULL)—— GORM Scan 到 float64 时 SUM(NULL) 会写 0。
func (r *usageRepo) SumCostSince(ctx context.Context, operatingOrgID uint64, since time.Time) (float64, error) {
	var total float64
	err := r.db.WithContext(ctx).
		Model(&model.LLMUsage{}).
		Where("operating_org_id = ? AND created_at >= ?", operatingOrgID, since).
		Select("COALESCE(SUM(cost_usd), 0)").
		Scan(&total).Error
	if err != nil {
		return 0, fmt.Errorf("sum llm usage cost: %w", err)
	}
	return total, nil
}
