// session_store.go user.SessionStore 的 Redis 实现。
//
// Redis key: synapse:session:{user_id}:{device_id}
// Value: JSON 序列化的 user.SessionInfo
// TTL: refresh token 有效期,每次 Save 时重置。
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/user"
	"github.com/eyrihe999-stack/Synapse/pkg/database"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
)

const sessionKeyPrefix = "synapse:session"

type redisSessionStore struct {
	redis database.RedisInterface
	log   logger.LoggerInterface
}

// NewSessionStore 创建基于 Redis 的 SessionStore 实例。
func NewSessionStore(rdb database.RedisInterface, log logger.LoggerInterface) user.SessionStore {
	return &redisSessionStore{redis: rdb, log: log}
}

func sessionKey(userID uint64, deviceID string) string {
	return fmt.Sprintf("%s:%d:%s", sessionKeyPrefix, userID, deviceID)
}

func sessionPattern(userID uint64) string {
	return fmt.Sprintf("%s:%d:*", sessionKeyPrefix, userID)
}

// Save 创建或更新一个设备 session。
//sayso-lint:ignore sentinel-wrap
func (s *redisSessionStore) Save(ctx context.Context, userID uint64, deviceID string, info user.SessionInfo, ttl time.Duration) error {
	data, err := json.Marshal(info)
	if err != nil {
		s.log.ErrorCtx(ctx, "序列化 session 失败", err, map[string]any{"user_id": userID, "device_id": deviceID})
		return fmt.Errorf("marshal session info: %w", err)
	}
	if err := s.redis.Set(ctx, sessionKey(userID, deviceID), string(data), ttl); err != nil {
		s.log.ErrorCtx(ctx, "保存 session 到 Redis 失败", err, map[string]any{"user_id": userID, "device_id": deviceID})
		return fmt.Errorf("redis set session: %w", err)
	}
	return nil
}

// Get 获取指定设备的 session。
//sayso-lint:ignore sentinel-wrap
func (s *redisSessionStore) Get(ctx context.Context, userID uint64, deviceID string) (*user.SessionInfo, error) {
	val, err := s.redis.Get(ctx, sessionKey(userID, deviceID))
	if err != nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, err // key 不存在是正常流程,由上层判断语义
	}
	var info user.SessionInfo
	if err := json.Unmarshal([]byte(val), &info); err != nil {
		s.log.ErrorCtx(ctx, "反序列化 session 失败", err, map[string]any{"user_id": userID, "device_id": deviceID})
		return nil, fmt.Errorf("unmarshal session info: %w", err)
	}
	return &info, nil
}

// List 返回用户的所有活跃 session。
func (s *redisSessionStore) List(ctx context.Context, userID uint64) ([]user.SessionEntry, error) {
	client := s.redis.GetClient()
	pattern := sessionPattern(userID)

	var keys []string
	var cursor uint64
	for {
		batch, nextCursor, err := client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			s.log.ErrorCtx(ctx, "扫描 session keys 失败", err, map[string]any{"user_id": userID})
			return nil, fmt.Errorf("scan sessions: %w", err)
		}
		keys = append(keys, batch...)
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	entries := make([]user.SessionEntry, 0, len(keys))
	prefix := fmt.Sprintf("%s:%d:", sessionKeyPrefix, userID)
	for _, key := range keys {
		val, err := s.redis.Get(ctx, key)
		if err != nil {
			continue // key 可能刚好过期
		}
		var info user.SessionInfo
		if err := json.Unmarshal([]byte(val), &info); err != nil {
			s.log.WarnCtx(ctx, "解析 session 失败", map[string]any{"key": key})
			continue
		}
		deviceID := strings.TrimPrefix(key, prefix)
		entries = append(entries, user.SessionEntry{
			DeviceID:   deviceID,
			DeviceName: info.DeviceName,
			LoginIP:    info.LoginIP,
			LoginAt:    info.LoginAt,
		})
	}
	return entries, nil
}

// Delete 删除指定设备的 session。
//sayso-lint:ignore sentinel-wrap
func (s *redisSessionStore) Delete(ctx context.Context, userID uint64, deviceID string) error {
	if err := s.redis.Del(ctx, sessionKey(userID, deviceID)); err != nil {
		s.log.ErrorCtx(ctx, "删除 session 失败", err, map[string]any{"user_id": userID, "device_id": deviceID})
		return fmt.Errorf("redis del session: %w", err)
	}
	return nil
}

// DeleteAll 删除用户的所有 session。
//
// 返回 Redis 操作错误。
func (s *redisSessionStore) DeleteAll(ctx context.Context, userID uint64) error {
	client := s.redis.GetClient()
	pattern := sessionPattern(userID)

	var cursor uint64
	for {
		keys, nextCursor, err := client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			s.log.ErrorCtx(ctx, "扫描 session keys 失败(删除)", err, map[string]any{"user_id": userID})
			return fmt.Errorf("scan sessions for delete: %w", err)
		}
		if len(keys) > 0 {
			if err := s.redis.Del(ctx, keys...); err != nil {
				s.log.ErrorCtx(ctx, "批量删除 session 失败", err, map[string]any{"user_id": userID})
				return fmt.Errorf("delete sessions: %w", err)
			}
		}
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
	return nil
}
