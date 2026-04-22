// Package repository user_integration 数据访问层(MySQL)。
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/user_integration"
	"github.com/eyrihe999-stack/Synapse/internal/user_integration/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Repository user_integration 模块对外数据访问入口。
type Repository interface {
	// Upsert 创建或更新一条记录。幂等键 (user_id, provider, external_account_id)。
	// 冲突路径下仅更新 token / 过期时间 / status 等可变字段;不改 id、user_id、provider、external_account_id。
	// 字段 CreatedAt / UpdatedAt 会被 repository 层兜底填(不依赖调用方)。
	Upsert(ctx context.Context, integration *model.UserIntegration) error

	// GetByUserProvider 按 (user_id, provider, external_account_id) 精确查。
	// 不存在返 ErrIntegrationNotFound。
	GetByUserProvider(
		ctx context.Context,
		userID uint64, provider, externalAccountID string,
	) (*model.UserIntegration, error)

	// ListByUser 列某用户的所有 integration,按 provider / external_account_id 排序。
	ListByUser(ctx context.Context, userID uint64) ([]*model.UserIntegration, error)

	// UpdateStatus 改状态(active / expired / revoked),同时 touch updated_at。
	UpdateStatus(ctx context.Context, id uint64, status string) error

	// UpdateTokens 在 refresh 流程成功后回写新 access_token + refresh_token + 过期时间。
	// expiresAt 可 nil(某些 provider 的 access_token 永不过期)。
	UpdateTokens(
		ctx context.Context,
		id uint64,
		accessToken, refreshToken string,
		expiresAt *time.Time,
	) error

	// Delete 删除记录(用户主动断开)。不存在视为幂等成功。
	Delete(ctx context.Context, userID, id uint64) error
}

type gormRepository struct {
	db *gorm.DB
}

// New 构造 Repository。
func New(db *gorm.DB) Repository {
	return &gormRepository{db: db}
}

// Upsert 实现见接口。
func (r *gormRepository) Upsert(ctx context.Context, in *model.UserIntegration) error {
	if in == nil {
		return fmt.Errorf("user_integration: upsert nil: %w", user_integration.ErrIntegrationInternal)
	}
	// 统一 UTC,避免服务器 TZ 切换后历史 vs 新数据 wall clock 不一致(MySQL datetime 不带时区元数据)。
	now := time.Now().UTC()
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now
	}
	in.UpdatedAt = now
	if in.Status == "" {
		in.Status = user_integration.StatusActive
	}

	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "user_id"}, {Name: "provider"}, {Name: "external_account_id"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"org_id",
			"account_name", "account_email",
			"access_token", "refresh_token", "token_type", "scopes", "expires_at",
			"provider_meta",
			"status", "last_used_at",
			"updated_at",
		}),
	}).Create(in).Error
	if err != nil {
		return fmt.Errorf("upsert user_integration: %w: %w", err, user_integration.ErrIntegrationInternal)
	}
	return nil
}

func (r *gormRepository) GetByUserProvider(
	ctx context.Context,
	userID uint64, provider, externalAccountID string,
) (*model.UserIntegration, error) {
	var ui model.UserIntegration
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND provider = ? AND external_account_id = ?", userID, provider, externalAccountID).
		Take(&ui).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, user_integration.ErrIntegrationNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user_integration: %w: %w", err, user_integration.ErrIntegrationInternal)
	}
	return &ui, nil
}

func (r *gormRepository) ListByUser(ctx context.Context, userID uint64) ([]*model.UserIntegration, error) {
	var rows []*model.UserIntegration
	if err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("provider ASC, external_account_id ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list user_integration: %w: %w", err, user_integration.ErrIntegrationInternal)
	}
	return rows, nil
}

func (r *gormRepository) UpdateStatus(ctx context.Context, id uint64, status string) error {
	res := r.db.WithContext(ctx).
		Model(&model.UserIntegration{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":     status,
			"updated_at": time.Now().UTC(),
		})
	if res.Error != nil {
		return fmt.Errorf("update status: %w: %w", res.Error, user_integration.ErrIntegrationInternal)
	}
	return nil
}

func (r *gormRepository) UpdateTokens(
	ctx context.Context,
	id uint64,
	accessToken, refreshToken string,
	expiresAt *time.Time,
) error {
	updates := map[string]any{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
		"expires_at":    expiresAt,
		"updated_at":    time.Now().UTC(),
	}
	res := r.db.WithContext(ctx).
		Model(&model.UserIntegration{}).
		Where("id = ?", id).
		Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("update tokens: %w: %w", res.Error, user_integration.ErrIntegrationInternal)
	}
	return nil
}

func (r *gormRepository) Delete(ctx context.Context, userID, id uint64) error {
	res := r.db.WithContext(ctx).
		Where("id = ? AND user_id = ?", id, userID).
		Delete(&model.UserIntegration{})
	if res.Error != nil {
		return fmt.Errorf("delete user_integration: %w: %w", res.Error, user_integration.ErrIntegrationInternal)
	}
	return nil
}
