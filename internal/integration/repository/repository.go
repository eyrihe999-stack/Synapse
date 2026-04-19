// Package repository UserIntegration 的持久化层。
//
// 接口故意保持"薄" —— 只暴露 OAuth 编排和 sync worker 真正需要的方法,避免将来新 provider 接入时
// 重新取舍"该不该加 xx 查询"的决策。调用点少 + 语义明确比通用性重要。
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/integration/model"
)

// Repository user_integrations 表的数据访问接口。
type Repository interface {
	// Upsert 插入或更新(user_id, provider)对应的记录。OAuth 首次授权走这个;再次授权(用户重新授权)也走这个。
	// 返回 persisted 行(带 ID + CreatedAt),调用方若要在同事务里继续操作可用。
	Upsert(ctx context.Context, in *model.UserIntegration) (*model.UserIntegration, error)

	// FindByUserProvider 按 (user_id, provider) 唯一查找。找不到返 (nil, nil),不报错 —— 调用方按此判断
	// "用户是否已授权"。
	FindByUserProvider(ctx context.Context, userID uint64, provider string) (*model.UserIntegration, error)

	// ListByProvider 列出所有有效的 (provider) 授权。用于 sync worker 逐个调 Adapter。
	// 排除 refresh_token 为空的行(= 撤销 / 失效)。orgID 为 0 时扫全表,非 0 限定 org(多租户隔离)。
	ListByProvider(ctx context.Context, orgID uint64, provider string) ([]*model.UserIntegration, error)

	// UpdateTokens 刷新 access_token(和可能轮换的 refresh_token)后回写。
	// 为什么单独一个方法:这是 Adapter 层异步调用路径,简化只更新 token 相关字段,不影响 LastSyncAt / Metadata。
	// 两个 expiresAt 用指针:nil 表示"永不过期",入库 NULL;非 nil 原样写入。
	UpdateTokens(ctx context.Context, id uint64, accessToken string, accessExpiresAt *time.Time, refreshToken string, refreshExpiresAt *time.Time) error

	// UpdateLastSyncAt sync worker 跑完一轮后调。单独方法避免和 token 刷新并发写同一行。
	UpdateLastSyncAt(ctx context.Context, id uint64, at time.Time) error

	// Delete 撤销授权。硬删 —— 审计需求低且敏感数据不宜残留。
	Delete(ctx context.Context, userID uint64, provider string) error
}

// New 构造。
func New(db *gorm.DB) Repository { return &gormRepo{db: db} }

type gormRepo struct{ db *gorm.DB }

func (r *gormRepo) Upsert(ctx context.Context, in *model.UserIntegration) (*model.UserIntegration, error) {
	if in == nil || in.UserID == 0 || in.Provider == "" {
		return nil, fmt.Errorf("integration upsert: user_id and provider required")
	}
	// 手写 "lookup → update / create" 替代 GORM OnConflict —— MySQL/PG 语法不同,显式两步更可控 + 可移植。
	// 并发保护:(user_id, provider) 有唯一索引,竞争时后到者走 update 分支(或 UNIQUE 冲突报错由调用方重试)。
	existing, err := r.FindByUserProvider(ctx, in.UserID, in.Provider)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		if err := r.db.WithContext(ctx).Create(in).Error; err != nil {
			return nil, fmt.Errorf("integration create: %w", err)
		}
		return in, nil
	}
	updates := map[string]any{
		"org_id":                   in.OrgID, // user 换 org 的场景罕见,但让 upsert 能带过去
		"refresh_token":            in.RefreshToken,
		"refresh_token_expires_at": in.RefreshTokenExpiresAt,
		"access_token":             in.AccessToken,
		"access_token_expires_at":  in.AccessTokenExpiresAt,
		"metadata":                 in.Metadata,
	}
	if err := r.db.WithContext(ctx).Model(&model.UserIntegration{}).Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("integration update: %w", err)
	}
	existing.OrgID = in.OrgID
	existing.RefreshToken = in.RefreshToken
	existing.RefreshTokenExpiresAt = in.RefreshTokenExpiresAt
	existing.AccessToken = in.AccessToken
	existing.AccessTokenExpiresAt = in.AccessTokenExpiresAt
	existing.Metadata = in.Metadata
	return existing, nil
}

func (r *gormRepo) FindByUserProvider(ctx context.Context, userID uint64, provider string) (*model.UserIntegration, error) {
	var row model.UserIntegration
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND provider = ?", userID, provider).
		Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("integration find: %w", err)
	}
	return &row, nil
}

func (r *gormRepo) ListByProvider(ctx context.Context, orgID uint64, provider string) ([]*model.UserIntegration, error) {
	q := r.db.WithContext(ctx).
		Where("provider = ? AND refresh_token <> ''", provider)
	if orgID != 0 {
		q = q.Where("org_id = ?", orgID)
	}
	var rows []*model.UserIntegration
	if err := q.Order("id ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("integration list: %w", err)
	}
	return rows, nil
}

func (r *gormRepo) UpdateTokens(ctx context.Context, id uint64, accessToken string, accessExpiresAt *time.Time, refreshToken string, refreshExpiresAt *time.Time) error {
	// refresh_token 可能空串(部分 provider 不轮换);空则不覆盖,保留原值。
	// access_token 总是更新 —— 刷新意味着有新 access_token,空串是异常不该落库。
	// expires_at 为 nil 时写 NULL(= 不过期),这是 GitLab PAT 等静态凭证的语义。
	updates := map[string]any{
		"access_token":            accessToken,
		"access_token_expires_at": accessExpiresAt,
	}
	if refreshToken != "" {
		updates["refresh_token"] = refreshToken
		if refreshExpiresAt != nil {
			updates["refresh_token_expires_at"] = refreshExpiresAt
		}
	}
	if err := r.db.WithContext(ctx).
		Model(&model.UserIntegration{}).
		Where("id = ?", id).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("integration update tokens: %w", err)
	}
	return nil
}

func (r *gormRepo) UpdateLastSyncAt(ctx context.Context, id uint64, at time.Time) error {
	if err := r.db.WithContext(ctx).
		Model(&model.UserIntegration{}).
		Where("id = ?", id).
		Update("last_sync_at", at).Error; err != nil {
		return fmt.Errorf("integration update last_sync_at: %w", err)
	}
	return nil
}

func (r *gormRepo) Delete(ctx context.Context, userID uint64, provider string) error {
	if err := r.db.WithContext(ctx).
		Where("user_id = ? AND provider = ?", userID, provider).
		Delete(&model.UserIntegration{}).Error; err != nil {
		return fmt.Errorf("integration delete: %w", err)
	}
	return nil
}
