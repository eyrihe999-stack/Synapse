// pwd_reset_store.go 密码重置 token 的 Redis 存取。
//
//   Key: synapse:pwd_reset:{token}
//   Val: JSON(PasswordResetEntry)
//   TTL: 由 EmailConfig.PasswordResetTTL 控制,默认 15m
//
// token 本身作 key 的好处:同一邮箱并发请求不会互相覆盖,每个链接独立生命周期;
// 坏处是无法通过 email 枚举当前 token,但这正好匹配"响应不泄露账户存在性"的要求。
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/common/database"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
)

const pwdResetKeyPrefix = "synapse:pwd_reset"

func pwdResetKey(token string) string { return fmt.Sprintf("%s:%s", pwdResetKeyPrefix, token) }

type redisPwdResetStore struct {
	redis database.RedisInterface
	log   logger.LoggerInterface
}

// NewPasswordResetStore 基于 Redis 的 PasswordResetStore 实现。
func NewPasswordResetStore(rdb database.RedisInterface, log logger.LoggerInterface) PasswordResetStore {
	return &redisPwdResetStore{redis: rdb, log: log}
}

// Store 写入 token → entry 的映射,TTL 由调用方决定。
// JSON 序列化失败或 Redis Set 失败都会打 ErrorCtx 并透传包装后的 error。
func (s *redisPwdResetStore) Store(ctx context.Context, token string, entry PasswordResetEntry, ttl time.Duration) error {
	data, err := json.Marshal(entry)
	if err != nil {
		s.log.ErrorCtx(ctx, "序列化 reset token 失败", err, map[string]interface{}{"email": entry.Email})
		return fmt.Errorf("marshal pwd reset: %w", err)
	}
	if err := s.redis.Set(ctx, pwdResetKey(token), string(data), ttl); err != nil {
		s.log.ErrorCtx(ctx, "保存 reset token 失败", err, map[string]interface{}{"email": entry.Email})
		return fmt.Errorf("redis set pwd reset: %w", err)
	}
	return nil
}

// Get 读取 token 对应的 entry。miss 返 (nil, err),由上层统一映射为 ErrResetTokenInvalid。
func (s *redisPwdResetStore) Get(ctx context.Context, token string) (*PasswordResetEntry, error) {
	val, err := s.redis.Get(ctx, pwdResetKey(token))
	if err != nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, err // miss 由上层统一当作 invalid,此处不打 log 不套 sentinel
	}
	var entry PasswordResetEntry
	if err := json.Unmarshal([]byte(val), &entry); err != nil {
		s.log.ErrorCtx(ctx, "反序列化 reset token 失败", err, nil)
		return nil, fmt.Errorf("unmarshal pwd reset: %w", err)
	}
	return &entry, nil
}

// Delete 消费 token 后调用,一次性语义的核心。
// Redis Del 失败时已打 ErrorCtx 日志,返回包装后的 error(上层通常 best-effort 忽略,TTL 兜底)。
func (s *redisPwdResetStore) Delete(ctx context.Context, token string) error {
	if err := s.redis.Del(ctx, pwdResetKey(token)); err != nil {
		s.log.ErrorCtx(ctx, "删除 reset token 失败", err, nil)
		return fmt.Errorf("redis del pwd reset: %w", err)
	}
	return nil
}
