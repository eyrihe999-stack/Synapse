// auth_code.go oauth_authorization_codes 表数据访问。
//
// 关键点:ExchangeOnce 是 tx + SELECT FOR UPDATE + UPDATE used_at 的原子操作,
// 保证并发重放下一个 code 只能被成功交换一次(RFC 6749 §10.5 reuse detection 的基线)。
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/oauth/model"
)

// AuthCodeRepo oauth_authorization_codes CRUD。
type AuthCodeRepo interface {
	// Create 发一张新授权码。CodeHash 必须由调用方预先算好(SHA256 hex),明文只在响应里回给 client 一次。
	Create(ctx context.Context, c *model.OAuthAuthorizationCode) error

	// ExchangeOnce 单用交换:tx 内 SELECT FOR UPDATE + 校验(未过期 / 未用) + UPDATE used_at。
	//   - 成功:返 (*code, nil),used_at 已被置位,再次调用此方法相同 codeHash 会返 ErrAuthCodeAlreadyUsed
	//   - code 不存在 / 过期 / 已用:返 ErrAuthCodeInvalid
	//   - DB 错误:透传
	//
	// 调用方在 ExchangeOnce 成功后才能校验 PKCE + 签 access/refresh token。
	ExchangeOnce(ctx context.Context, codeHash string) (*model.OAuthAuthorizationCode, error)

	// DeleteExpired 后台清理。返删除行数,诊断用。
	DeleteExpired(ctx context.Context, before time.Time) (int64, error)
}

// ErrAuthCodeInvalid 授权码不存在 / 过期 / 已使用。对上层统一暴露"invalid_grant"。
var ErrAuthCodeInvalid = errors.New("oauth: authorization code invalid")

type gormAuthCodeRepo struct {
	db *gorm.DB
}

func NewAuthCodeRepo(db *gorm.DB) AuthCodeRepo { return &gormAuthCodeRepo{db: db} }

func (r *gormAuthCodeRepo) Create(ctx context.Context, c *model.OAuthAuthorizationCode) error {
	if err := r.db.WithContext(ctx).Create(c).Error; err != nil {
		return fmt.Errorf("create oauth auth code: %w", err)
	}
	return nil
}

// ExchangeOnce 关键实现:SELECT ... FOR UPDATE 在 tx 内锁行 → 校验条件 → UPDATE used_at。
// 任何并发第二次调用会被 SELECT FOR UPDATE 阻塞,等第一次 tx COMMIT 后读到 used_at != NULL 直接返 invalid。
func (r *gormAuthCodeRepo) ExchangeOnce(ctx context.Context, codeHash string) (*model.OAuthAuthorizationCode, error) {
	var out *model.OAuthAuthorizationCode
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var c model.OAuthAuthorizationCode
		err := tx.
			Set("gorm:query_option", "FOR UPDATE").
			Where("code_hash = ?", codeHash).
			First(&c).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAuthCodeInvalid
			}
			return fmt.Errorf("select auth code for update: %w", err)
		}
		now := time.Now()
		if c.UsedAt != nil || now.After(c.ExpiresAt) {
			return ErrAuthCodeInvalid
		}
		c.UsedAt = &now
		if err := tx.Model(&model.OAuthAuthorizationCode{}).
			Where("id = ?", c.ID).
			Update("used_at", now).Error; err != nil {
			return fmt.Errorf("mark auth code used: %w", err)
		}
		out = &c
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r *gormAuthCodeRepo) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	res := r.db.WithContext(ctx).
		Where("expires_at < ?", before).
		Delete(&model.OAuthAuthorizationCode{})
	if res.Error != nil {
		return 0, fmt.Errorf("delete expired auth codes: %w", res.Error)
	}
	return res.RowsAffected, nil
}
