package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/common/database"
)

// RedisSessionStore OAuthSessionStore 的 Redis 实现。
//
// Key:synapse:oauth_session:<sid>
// Value:user_id 字符串
// TTL:由 Config 注入(默认 10min,够走完 login → consent)
type RedisSessionStore struct {
	rdb database.RedisInterface
	ttl time.Duration
}

// NewRedisSessionStore 构造。ttl<=0 时走内置默认 10 分钟。
func NewRedisSessionStore(rdb database.RedisInterface, ttl time.Duration) *RedisSessionStore {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &RedisSessionStore{rdb: rdb, ttl: ttl}
}

// Create 生成 sessionID + 存 userID,返 sessionID。
func (s *RedisSessionStore) Create(ctx context.Context, userID uint64) (string, error) {
	if userID == 0 {
		return "", errors.New("oauth session: user_id required")
	}
	sid, err := randomBase64URL(24) // 192 bit 熵
	if err != nil {
		return "", fmt.Errorf("oauth session: gen sid: %w", err)
	}
	key := "synapse:oauth_session:" + sid
	if err := s.rdb.Set(ctx, key, strconv.FormatUint(userID, 10), s.ttl); err != nil {
		return "", fmt.Errorf("oauth session: set: %w", err)
	}
	return sid, nil
}

// Resolve 按 sid 查 userID。找不到(TTL 过期 / 删除 / 不存在)返 0 + nil(不报错,
// 调用方按"未登录"处理)。
func (s *RedisSessionStore) Resolve(ctx context.Context, sid string) (uint64, error) {
	if sid == "" {
		return 0, nil
	}
	key := "synapse:oauth_session:" + sid
	raw, err := s.rdb.Get(ctx, key)
	if err != nil {
		return 0, nil
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, nil
	}
	return id, nil
}

// Delete 主动删 session(consent 完成后立刻清,减少暴露窗口)。
func (s *RedisSessionStore) Delete(ctx context.Context, sid string) error {
	if sid == "" {
		return nil
	}
	return s.rdb.Del(ctx, "synapse:oauth_session:"+sid)
}
