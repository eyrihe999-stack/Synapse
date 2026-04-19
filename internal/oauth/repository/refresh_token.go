// refresh_token.go oauth_refresh_tokens 表数据访问,含轮换与 reuse detection。
//
// 核心安全约束(RFC 6749 §10.4):refresh token 不应被多次使用。我们实现:
//  1. Rotate 把旧 token 的 replaced_by_hash 置位,插入新 token,指 parent_token_hash 回旧;tx 原子
//  2. 每次 /token 来 refresh_token,查 token_hash —— 若 revoked_at != NULL(已被轮换或撤销),
//     说明同一 token 被第二次使用 → 推定泄露 → RevokeChain 整条链 revoke
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/oauth/model"
)

// RefreshTokenRepo oauth_refresh_tokens CRUD。
type RefreshTokenRepo interface {
	// Create 首次发 refresh token(authorization_code grant 路径)。ParentTokenHash 留空。
	Create(ctx context.Context, t *model.OAuthRefreshToken) error

	// GetByHash 按 token_hash 查。not-found 返 (nil, nil)。
	// 注意:仍会返 revoked_at != NULL 的行 —— 上层需要判此字段来做 reuse detection。
	GetByHash(ctx context.Context, tokenHash string) (*model.OAuthRefreshToken, error)

	// Rotate 原子轮换:校验 oldHash 未 revoked → 标记 replaced_by/revoked_at → 插入 new(parent=old)。
	// 返新 token model。oldHash 已 revoked 或过期 → ErrRefreshTokenReused(上层据此 RevokeChain + 拒登)。
	Rotate(ctx context.Context, oldHash string, newToken *model.OAuthRefreshToken) (*model.OAuthRefreshToken, error)

	// RevokeChain 沿 parent_token_hash 回溯并把整棵链 revoke 掉。
	// 用于 reuse detection:一旦 Rotate 发现泄露,调用此方法防同一链条的其他 token 继续工作。
	// 返撤销行数(含起点自身)。
	RevokeChain(ctx context.Context, anyHashInChain string) (int64, error)

	// DeleteExpired 清过期。返删除行数。
	DeleteExpired(ctx context.Context, before time.Time) (int64, error)
}

// ErrRefreshTokenReused 当前 token 已被轮换或撤销,再次使用视为泄露。
var ErrRefreshTokenReused = errors.New("oauth: refresh token already used or revoked")

// ErrRefreshTokenInvalid token 不存在或过期。
var ErrRefreshTokenInvalid = errors.New("oauth: refresh token invalid")

type gormRefreshTokenRepo struct {
	db *gorm.DB
}

func NewRefreshTokenRepo(db *gorm.DB) RefreshTokenRepo { return &gormRefreshTokenRepo{db: db} }

func (r *gormRefreshTokenRepo) Create(ctx context.Context, t *model.OAuthRefreshToken) error {
	if err := r.db.WithContext(ctx).Create(t).Error; err != nil {
		return fmt.Errorf("create oauth refresh token: %w", err)
	}
	return nil
}

func (r *gormRefreshTokenRepo) GetByHash(ctx context.Context, tokenHash string) (*model.OAuthRefreshToken, error) {
	var t model.OAuthRefreshToken
	err := r.db.WithContext(ctx).
		Where("token_hash = ?", tokenHash).
		First(&t).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("get oauth refresh token: %w", err)
	}
	return &t, nil
}

// Rotate 实现:tx + SELECT FOR UPDATE 锁旧 row → 校验 → UPDATE 旧 row + INSERT 新 row。
// 任何并发第二次 Rotate(同一 oldHash)会在 SELECT FOR UPDATE 阻塞,然后读到 revoked_at != NULL,
// 返 ErrRefreshTokenReused。
func (r *gormRefreshTokenRepo) Rotate(ctx context.Context, oldHash string, newToken *model.OAuthRefreshToken) (*model.OAuthRefreshToken, error) {
	var out *model.OAuthRefreshToken
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var old model.OAuthRefreshToken
		err := tx.
			Set("gorm:query_option", "FOR UPDATE").
			Where("token_hash = ?", oldHash).
			First(&old).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRefreshTokenInvalid
			}
			return fmt.Errorf("select refresh token for update: %w", err)
		}
		now := time.Now()
		// 已撤销 / 已被替换 / 已过期 → reuse detection 或自然失效,分别明确返回。
		if old.RevokedAt != nil || old.ReplacedByHash != "" {
			return ErrRefreshTokenReused
		}
		if now.After(old.ExpiresAt) {
			return ErrRefreshTokenInvalid
		}

		// 继承链:新 token 的 parent 指回旧 token hash。
		newToken.ParentTokenHash = old.TokenHash

		// 旧 token 置 replaced_by + revoked_at。
		if err := tx.Model(&model.OAuthRefreshToken{}).
			Where("id = ?", old.ID).
			Updates(map[string]any{
				"replaced_by_hash": newToken.TokenHash,
				"revoked_at":       now,
			}).Error; err != nil {
			return fmt.Errorf("mark old refresh token replaced: %w", err)
		}

		if err := tx.Create(newToken).Error; err != nil {
			return fmt.Errorf("insert new refresh token: %w", err)
		}
		out = newToken
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RevokeChain 沿 parent_token_hash 和 replaced_by_hash 两个方向 BFS,全部置 revoked_at。
//
// 策略说明:先定位起点 row,再分别向上(parent)+ 向下(replaced_by)遍历。
// 实际场景链深度很浅(一次 refresh 生一个节点),BFS 循环次数小,不用递归 CTE。
func (r *gormRefreshTokenRepo) RevokeChain(ctx context.Context, anyHashInChain string) (int64, error) {
	var affected int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()

		// 收集全链 hash。上行:不停读 parent_token_hash,直到 "".
		// 下行:不停读 replaced_by_hash,直到 "" 或循环保护触发。
		hashes := map[string]struct{}{anyHashInChain: {}}

		// 上行
		cur := anyHashInChain
		for range 100 { // 深度保护,链理论不会这么深
			var row model.OAuthRefreshToken
			if err := tx.Select("parent_token_hash").Where("token_hash = ?", cur).First(&row).Error; err != nil {
				break
			}
			if row.ParentTokenHash == "" {
				break
			}
			if _, seen := hashes[row.ParentTokenHash]; seen {
				break
			}
			hashes[row.ParentTokenHash] = struct{}{}
			cur = row.ParentTokenHash
		}

		// 下行
		cur = anyHashInChain
		for range 100 {
			var row model.OAuthRefreshToken
			if err := tx.Select("replaced_by_hash").Where("token_hash = ?", cur).First(&row).Error; err != nil {
				break
			}
			if row.ReplacedByHash == "" {
				break
			}
			if _, seen := hashes[row.ReplacedByHash]; seen {
				break
			}
			hashes[row.ReplacedByHash] = struct{}{}
			cur = row.ReplacedByHash
		}

		if len(hashes) == 0 {
			return nil
		}
		list := make([]string, 0, len(hashes))
		for h := range hashes {
			list = append(list, h)
		}

		res := tx.Model(&model.OAuthRefreshToken{}).
			Where("token_hash IN ? AND revoked_at IS NULL", list).
			Update("revoked_at", now)
		if res.Error != nil {
			return fmt.Errorf("revoke refresh token chain: %w", res.Error)
		}
		affected = res.RowsAffected
		return nil
	})
	if err != nil {
		return 0, err
	}
	return affected, nil
}

func (r *gormRefreshTokenRepo) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	res := r.db.WithContext(ctx).
		Where("expires_at < ?", before).
		Delete(&model.OAuthRefreshToken{})
	if res.Error != nil {
		return 0, fmt.Errorf("delete expired refresh tokens: %w", res.Error)
	}
	return res.RowsAffected, nil
}
