// Package repository OAuth 模块数据访问层。
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/oauth/model"
)

// Repository OAuth 所有子实体的数据访问统一入口。
type Repository interface {
	WithTx(ctx context.Context, fn func(tx Repository) error) error

	// ── Client ───────────────────────────────────────────────────────────
	CreateClient(ctx context.Context, c *model.OAuthClient) error
	FindClientByClientID(ctx context.Context, clientID string) (*model.OAuthClient, error)
	FindClientByID(ctx context.Context, id uint64) (*model.OAuthClient, error)
	ListClientsByUser(ctx context.Context, userID uint64) ([]model.OAuthClient, error)
	UpdateClientDisabled(ctx context.Context, id uint64, disabled bool) error

	// ── Authorization code ──────────────────────────────────────────────
	CreateAuthorizationCode(ctx context.Context, c *model.OAuthAuthorizationCode) error
	FindAuthorizationCodeByHash(ctx context.Context, hash string) (*model.OAuthAuthorizationCode, error)
	// ConsumeAuthorizationCode 原子消费:只有 consumed_at IS NULL 才能成功。
	// 返 RowsAffected;==0 表示已被消费(重放 / 并发)。
	ConsumeAuthorizationCode(ctx context.Context, id uint64, at time.Time) (int64, error)

	// ── Access token ─────────────────────────────────────────────────────
	CreateAccessToken(ctx context.Context, t *model.OAuthAccessToken) error
	FindAccessTokenByHash(ctx context.Context, hash string) (*model.OAuthAccessToken, error)
	TouchAccessTokenLastUsed(ctx context.Context, id uint64, at time.Time) error
	RevokeAccessToken(ctx context.Context, id uint64, at time.Time) error
	// RevokeAccessTokensByClient 批量吊销某 client 的所有 active token(client disable 时)。
	RevokeAccessTokensByClient(ctx context.Context, clientID string, at time.Time) (int64, error)

	// ── Refresh token ────────────────────────────────────────────────────
	CreateRefreshToken(ctx context.Context, t *model.OAuthRefreshToken) error
	FindRefreshTokenByHash(ctx context.Context, hash string) (*model.OAuthRefreshToken, error)
	RevokeRefreshToken(ctx context.Context, id uint64, at time.Time, rotatedToHash string) error
	RevokeRefreshTokensByClient(ctx context.Context, clientID string, at time.Time) (int64, error)

	// ── PAT ──────────────────────────────────────────────────────────────
	CreatePAT(ctx context.Context, p *model.UserPAT) error
	FindPATByHash(ctx context.Context, hash string) (*model.UserPAT, error)
	FindPATByID(ctx context.Context, id uint64) (*model.UserPAT, error)
	ListPATsByUser(ctx context.Context, userID uint64) ([]model.UserPAT, error)
	TouchPATLastUsed(ctx context.Context, id uint64, at time.Time) error
	RevokePAT(ctx context.Context, id uint64, at time.Time) error
}

// gormRepository Repository 的 GORM 实现。
type gormRepository struct {
	db *gorm.DB
}

// New 构造 Repository。
func New(db *gorm.DB) Repository { return &gormRepository{db: db} }

func (r *gormRepository) WithTx(ctx context.Context, fn func(tx Repository) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&gormRepository{db: tx})
	})
}

// ── Client ────────────────────────────────────────────────────────────

func (r *gormRepository) CreateClient(ctx context.Context, c *model.OAuthClient) error {
	if err := r.db.WithContext(ctx).Create(c).Error; err != nil {
		return fmt.Errorf("create client: %w", err)
	}
	return nil
}

func (r *gormRepository) FindClientByClientID(ctx context.Context, clientID string) (*model.OAuthClient, error) {
	var row model.OAuthClient
	err := r.db.WithContext(ctx).Where("client_id = ?", clientID).Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find client by client_id: %w", err)
	}
	return &row, nil
}

func (r *gormRepository) FindClientByID(ctx context.Context, id uint64) (*model.OAuthClient, error) {
	var row model.OAuthClient
	err := r.db.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find client by id: %w", err)
	}
	return &row, nil
}

func (r *gormRepository) ListClientsByUser(ctx context.Context, userID uint64) ([]model.OAuthClient, error) {
	var rows []model.OAuthClient
	if err := r.db.WithContext(ctx).
		Where("registered_by_user_id = ?", userID).
		Order("id DESC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list clients by user: %w", err)
	}
	return rows, nil
}

func (r *gormRepository) UpdateClientDisabled(ctx context.Context, id uint64, disabled bool) error {
	res := r.db.WithContext(ctx).Model(&model.OAuthClient{}).
		Where("id = ?", id).
		Update("disabled", disabled)
	if res.Error != nil {
		return fmt.Errorf("update client disabled: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ── Authorization code ──────────────────────────────────────────────

func (r *gormRepository) CreateAuthorizationCode(ctx context.Context, c *model.OAuthAuthorizationCode) error {
	if err := r.db.WithContext(ctx).Create(c).Error; err != nil {
		return fmt.Errorf("create auth code: %w", err)
	}
	return nil
}

func (r *gormRepository) FindAuthorizationCodeByHash(ctx context.Context, hash string) (*model.OAuthAuthorizationCode, error) {
	var row model.OAuthAuthorizationCode
	err := r.db.WithContext(ctx).Where("code_hash = ?", hash).Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find auth code by hash: %w", err)
	}
	return &row, nil
}

func (r *gormRepository) ConsumeAuthorizationCode(ctx context.Context, id uint64, at time.Time) (int64, error) {
	res := r.db.WithContext(ctx).Model(&model.OAuthAuthorizationCode{}).
		Where("id = ? AND consumed_at IS NULL", id).
		Update("consumed_at", at)
	if res.Error != nil {
		return 0, fmt.Errorf("consume auth code: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// ── Access token ─────────────────────────────────────────────────────

func (r *gormRepository) CreateAccessToken(ctx context.Context, t *model.OAuthAccessToken) error {
	if err := r.db.WithContext(ctx).Create(t).Error; err != nil {
		return fmt.Errorf("create access token: %w", err)
	}
	return nil
}

func (r *gormRepository) FindAccessTokenByHash(ctx context.Context, hash string) (*model.OAuthAccessToken, error) {
	var row model.OAuthAccessToken
	err := r.db.WithContext(ctx).Where("token_hash = ?", hash).Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find access token: %w", err)
	}
	return &row, nil
}

func (r *gormRepository) TouchAccessTokenLastUsed(ctx context.Context, id uint64, at time.Time) error {
	if err := r.db.WithContext(ctx).Model(&model.OAuthAccessToken{}).
		Where("id = ?", id).
		Update("last_used_at", at).Error; err != nil {
		return fmt.Errorf("touch access token: %w", err)
	}
	return nil
}

func (r *gormRepository) RevokeAccessToken(ctx context.Context, id uint64, at time.Time) error {
	if err := r.db.WithContext(ctx).Model(&model.OAuthAccessToken{}).
		Where("id = ? AND revoked_at IS NULL", id).
		Update("revoked_at", at).Error; err != nil {
		return fmt.Errorf("revoke access token: %w", err)
	}
	return nil
}

func (r *gormRepository) RevokeAccessTokensByClient(ctx context.Context, clientID string, at time.Time) (int64, error) {
	res := r.db.WithContext(ctx).Model(&model.OAuthAccessToken{}).
		Where("client_id = ? AND revoked_at IS NULL", clientID).
		Update("revoked_at", at)
	if res.Error != nil {
		return 0, fmt.Errorf("revoke access tokens by client: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// ── Refresh token ────────────────────────────────────────────────────

func (r *gormRepository) CreateRefreshToken(ctx context.Context, t *model.OAuthRefreshToken) error {
	if err := r.db.WithContext(ctx).Create(t).Error; err != nil {
		return fmt.Errorf("create refresh token: %w", err)
	}
	return nil
}

func (r *gormRepository) FindRefreshTokenByHash(ctx context.Context, hash string) (*model.OAuthRefreshToken, error) {
	var row model.OAuthRefreshToken
	err := r.db.WithContext(ctx).Where("token_hash = ?", hash).Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find refresh token: %w", err)
	}
	return &row, nil
}

func (r *gormRepository) RevokeRefreshToken(ctx context.Context, id uint64, at time.Time, rotatedToHash string) error {
	updates := map[string]any{"revoked_at": at}
	if rotatedToHash != "" {
		updates["rotated_to_token_hash"] = rotatedToHash
	}
	if err := r.db.WithContext(ctx).Model(&model.OAuthRefreshToken{}).
		Where("id = ? AND revoked_at IS NULL", id).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("revoke refresh token: %w", err)
	}
	return nil
}

func (r *gormRepository) RevokeRefreshTokensByClient(ctx context.Context, clientID string, at time.Time) (int64, error) {
	res := r.db.WithContext(ctx).Model(&model.OAuthRefreshToken{}).
		Where("client_id = ? AND revoked_at IS NULL", clientID).
		Update("revoked_at", at)
	if res.Error != nil {
		return 0, fmt.Errorf("revoke refresh tokens by client: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// ── PAT ──────────────────────────────────────────────────────────────

func (r *gormRepository) CreatePAT(ctx context.Context, p *model.UserPAT) error {
	if err := r.db.WithContext(ctx).Create(p).Error; err != nil {
		return fmt.Errorf("create pat: %w", err)
	}
	return nil
}

func (r *gormRepository) FindPATByHash(ctx context.Context, hash string) (*model.UserPAT, error) {
	var row model.UserPAT
	err := r.db.WithContext(ctx).Where("token_hash = ?", hash).Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find pat by hash: %w", err)
	}
	return &row, nil
}

func (r *gormRepository) FindPATByID(ctx context.Context, id uint64) (*model.UserPAT, error) {
	var row model.UserPAT
	err := r.db.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find pat by id: %w", err)
	}
	return &row, nil
}

func (r *gormRepository) ListPATsByUser(ctx context.Context, userID uint64) ([]model.UserPAT, error) {
	var rows []model.UserPAT
	if err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("id DESC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list pats by user: %w", err)
	}
	return rows, nil
}

func (r *gormRepository) TouchPATLastUsed(ctx context.Context, id uint64, at time.Time) error {
	if err := r.db.WithContext(ctx).Model(&model.UserPAT{}).
		Where("id = ?", id).
		Update("last_used_at", at).Error; err != nil {
		return fmt.Errorf("touch pat: %w", err)
	}
	return nil
}

func (r *gormRepository) RevokePAT(ctx context.Context, id uint64, at time.Time) error {
	res := r.db.WithContext(ctx).Model(&model.UserPAT{}).
		Where("id = ? AND revoked_at IS NULL", id).
		Update("revoked_at", at)
	if res.Error != nil {
		return fmt.Errorf("revoke pat: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
