// email_verify_store.go 邮箱激活 token 的 Redis 存取 (M1.1)。
//
//	Key: synapse:email_verify:{token}
//	Val: JSON(EmailVerifyEntry)
//	TTL: 由 EmailConfig.VerificationTTL 控制,默认 24h
//
// 设计与 pwd_reset_store 对齐:token 本身作 key,同一用户并发请求不互相覆盖,
// 每个激活链接独立生命周期,一次性消费后作废;TTL 兜底防孤儿。
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/common/database"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
)

const emailVerifyKeyPrefix = "synapse:email_verify"

func emailVerifyKey(token string) string {
	return fmt.Sprintf("%s:%s", emailVerifyKeyPrefix, token)
}

type redisEmailVerifyStore struct {
	redis database.RedisInterface
	log   logger.LoggerInterface
}

// NewEmailVerifyStore 基于 Redis 的 EmailVerifyStore 实现。
func NewEmailVerifyStore(rdb database.RedisInterface, log logger.LoggerInterface) EmailVerifyStore {
	return &redisEmailVerifyStore{redis: rdb, log: log}
}

// Store 写入 token → entry 的映射,TTL 由调用方控制。
// 错误场景:entry JSON 序列化失败 / Redis SET 失败(网络断 / OOM);调用方负责包装 sentinel。
func (s *redisEmailVerifyStore) Store(ctx context.Context, token string, entry EmailVerifyEntry, ttl time.Duration) error {
	data, err := json.Marshal(entry)
	if err != nil {
		s.log.ErrorCtx(ctx, "序列化 email verify token 失败", err, map[string]interface{}{"user_id": entry.UserID})
		return fmt.Errorf("marshal email verify: %w", err)
	}
	if err := s.redis.Set(ctx, emailVerifyKey(token), string(data), ttl); err != nil {
		s.log.ErrorCtx(ctx, "保存 email verify token 失败", err, map[string]interface{}{"user_id": entry.UserID})
		return fmt.Errorf("redis set email verify: %w", err)
	}
	return nil
}

// Get 读取 token → entry。miss 返 (nil, err),上层统一映射为 ErrVerifyTokenInvalid(不区分过期/不存在防枚举)。
func (s *redisEmailVerifyStore) Get(ctx context.Context, token string) (*EmailVerifyEntry, error) {
	val, err := s.redis.Get(ctx, emailVerifyKey(token))
	if err != nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, err // miss 由上层当作 invalid,此处不打 log 不套 sentinel
	}
	var entry EmailVerifyEntry
	if err := json.Unmarshal([]byte(val), &entry); err != nil {
		s.log.ErrorCtx(ctx, "反序列化 email verify token 失败", err, nil)
		return nil, fmt.Errorf("unmarshal email verify: %w", err)
	}
	return &entry, nil
}

// Delete 一次性消费 token;Redis 失败已打 log,调用方通常 best-effort 忽略 TTL 兜底。
func (s *redisEmailVerifyStore) Delete(ctx context.Context, token string) error {
	if err := s.redis.Del(ctx, emailVerifyKey(token)); err != nil {
		s.log.ErrorCtx(ctx, "删除 email verify token 失败", err, nil)
		return fmt.Errorf("redis del email verify: %w", err)
	}
	return nil
}
