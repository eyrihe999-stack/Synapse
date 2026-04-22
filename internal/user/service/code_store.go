// code_store.go 邮箱验证码的 Redis 存取 + 防刷计数器。
//
// 三条 key(均已登记到 internal/common/database/redis.go 顶部 Key Registry):
//
//   - synapse:email_code:{email}
//     JSON {code, ip, created_at},TTL = EmailConfig.CodeTTL(默认 10 分钟)。
//     SET 语义 → 第二次发码自动覆盖旧码(单码原则,防爆破设计的基石)。
//
//   - synapse:email_rl:{email}:{YYYY-MM-DD}
//     int,TTL 24h,每次 SendEmailCode INCR。
//     超过 DailyVerificationLimit 则拒绝再发,按 UTC 天自然切换 key。
//
//   - synapse:email_attempt:{email}
//     int,TTL 同 code(过期即没意义),验错时 INCR。
//     触达 MaxAttempts → 上层删 code + 删 attempt(码作废,下一次必须重新发)。
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/common/database"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
)

const (
	emailCodeKeyPrefix    = "synapse:email_code"
	emailRLKeyPrefix      = "synapse:email_rl"
	emailAttemptKeyPrefix = "synapse:email_attempt"
)

// EmailCodeEntry Redis 中存的验证码条目。
type EmailCodeEntry struct {
	Code      string    `json:"code"`
	IP        string    `json:"ip"`
	CreatedAt time.Time `json:"created_at"`
}

// EmailCodeStore 邮箱验证码存取接口。
type EmailCodeStore interface {
	Store(ctx context.Context, email string, entry EmailCodeEntry, ttl time.Duration) error
	Get(ctx context.Context, email string) (*EmailCodeEntry, error)
	Delete(ctx context.Context, email string) error

	IncrDailyCount(ctx context.Context, email string) (int64, error)

	IncrAttempt(ctx context.Context, email string, ttl time.Duration) (int64, error)
	DeleteAttempt(ctx context.Context, email string) error
}

type redisCodeStore struct {
	redis database.RedisInterface
	log   logger.LoggerInterface
}

// NewEmailCodeStore 基于 Redis 的 EmailCodeStore 实例。
func NewEmailCodeStore(rdb database.RedisInterface, log logger.LoggerInterface) EmailCodeStore {
	return &redisCodeStore{redis: rdb, log: log}
}

func emailCodeKey(email string) string    { return fmt.Sprintf("%s:%s", emailCodeKeyPrefix, email) }
func emailAttemptKey(email string) string { return fmt.Sprintf("%s:%s", emailAttemptKeyPrefix, email) }

// emailRLKey 按 UTC 天分桶,过天后新 key 从 0 开始,老 key 自然过期。
func emailRLKey(email string) string {
	day := time.Now().UTC().Format("2006-01-02")
	return fmt.Sprintf("%s:%s:%s", emailRLKeyPrefix, email, day)
}

// Store 将验证码条目写入 Redis,TTL 由调用方决定。
// JSON 序列化或 Redis Set 失败会直接透传 error(已打 ErrorCtx 日志)。
func (s *redisCodeStore) Store(ctx context.Context, email string, entry EmailCodeEntry, ttl time.Duration) error {
	data, err := json.Marshal(entry)
	if err != nil {
		s.log.ErrorCtx(ctx, "序列化验证码失败", err, map[string]interface{}{"email": email})
		return fmt.Errorf("marshal email code: %w", err)
	}
	if err := s.redis.Set(ctx, emailCodeKey(email), string(data), ttl); err != nil {
		s.log.ErrorCtx(ctx, "保存验证码失败", err, map[string]interface{}{"email": email})
		return fmt.Errorf("redis set email code: %w", err)
	}
	return nil
}

// Get 按 email 读出验证码条目。
// key 不存在(miss)是正常流程 —— 由上层通过 (entry == nil || err != nil) 判断并
// 映射为 ErrEmailCodeNotFound;miss 不打日志也不套 sentinel,避免污染正常链路。
// 反序列化失败视为致命错误,会打 log 并透传包装后的 error。
func (s *redisCodeStore) Get(ctx context.Context, email string) (*EmailCodeEntry, error) {
	val, err := s.redis.Get(ctx, emailCodeKey(email))
	if err != nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, err // key miss 是正常流程,由上层判断,不加 sentinel 也不打 log
	}
	var entry EmailCodeEntry
	if err := json.Unmarshal([]byte(val), &entry); err != nil {
		s.log.ErrorCtx(ctx, "反序列化验证码失败", err, map[string]interface{}{"email": email})
		return nil, fmt.Errorf("unmarshal email code: %w", err)
	}
	return &entry, nil
}

// Delete 删除 email 对应的验证码条目。
// Redis Del 失败时已打 ErrorCtx 日志,返回包装后的 error。
func (s *redisCodeStore) Delete(ctx context.Context, email string) error {
	if err := s.redis.Del(ctx, emailCodeKey(email)); err != nil {
		s.log.ErrorCtx(ctx, "删除验证码失败", err, map[string]interface{}{"email": email})
		return fmt.Errorf("redis del email code: %w", err)
	}
	return nil
}

// IncrDailyCount 首次创建时设 24h TTL,靠 Redis Lua 保证原子。
// Redis 脚本执行失败(连接断开/脚本异常)会打 ErrorCtx 并透传包装后的 error。
func (s *redisCodeStore) IncrDailyCount(ctx context.Context, email string) (int64, error) {
	count, err := s.redis.IncrAndExpireIfNew(ctx, emailRLKey(email), 24*time.Hour)
	if err != nil {
		s.log.ErrorCtx(ctx, "递增日限计数失败", err, map[string]interface{}{"email": email})
		return 0, fmt.Errorf("incr email rl: %w", err)
	}
	return count, nil
}

// IncrAttempt 首次创建时用传入 ttl(一般等于 code ttl)。
// Redis 脚本执行失败会打 ErrorCtx 并透传包装后的 error。
func (s *redisCodeStore) IncrAttempt(ctx context.Context, email string, ttl time.Duration) (int64, error) {
	count, err := s.redis.IncrAndExpireIfNew(ctx, emailAttemptKey(email), ttl)
	if err != nil {
		s.log.ErrorCtx(ctx, "递增验证失败计数失败", err, map[string]interface{}{"email": email})
		return 0, fmt.Errorf("incr email attempt: %w", err)
	}
	return count, nil
}

// DeleteAttempt 清除 email 对应的验错计数器。
// Redis Del 失败时已打 ErrorCtx 日志,返回包装后的 error。
func (s *redisCodeStore) DeleteAttempt(ctx context.Context, email string) error {
	if err := s.redis.Del(ctx, emailAttemptKey(email)); err != nil {
		s.log.ErrorCtx(ctx, "删除验证失败计数失败", err, map[string]interface{}{"email": email})
		return fmt.Errorf("redis del email attempt: %w", err)
	}
	return nil
}
