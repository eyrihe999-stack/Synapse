// ratelimit.go 简化的限流服务,基于 Redis INCR + EXPIRE。
package service

import (
	"context"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/agent"
	"github.com/eyrihe999-stack/Synapse/pkg/database"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
)

// RateLimitService 提供简单的 user+agent 组合限流。
type RateLimitService interface {
	// CheckChatLimit 检查用户对指定 agent 的对话频率是否超限。
	CheckChatLimit(ctx context.Context, userID, agentID uint64) error
}

type rateLimitService struct {
	cfg    Config
	rdb    database.RedisInterface
	logger logger.LoggerInterface
}

// NewRateLimitService 构造限流服务。当 cfg.ChatRateLimitPerMinute <= 0 时使用默认值。
func NewRateLimitService(cfg Config, rdb database.RedisInterface, log logger.LoggerInterface) RateLimitService {
	if cfg.ChatRateLimitPerMinute <= 0 {
		cfg.ChatRateLimitPerMinute = agent.DefaultChatRateLimitPerMinute
	}
	return &rateLimitService{cfg: cfg, rdb: rdb, logger: log}
}

// CheckChatLimit 基于 Redis 滑动窗口检查 user+agent 每分钟对话频率。
// Redis 不可用时 fail-open(放行请求并记录日志)。
// 超限时返回 ErrChatRateLimited。
func (s *rateLimitService) CheckChatLimit(ctx context.Context, userID, agentID uint64) error {
	key := fmt.Sprintf("synapse:rl:chat:%d:%d", userID, agentID)
	count, err := s.rdb.Incr(ctx, key)
	if err != nil {
		// Redis 错误时 fail-open,记录日志但不阻塞请求
		s.logger.WarnCtx(ctx, "rate limit redis incr failed, allowing request", map[string]any{
			"error": err.Error(), "user_id": userID, "agent_id": agentID,
		})
		return nil
	}
	if count == 1 {
		// 首次请求,设置 60 秒过期
		s.rdb.Expire(ctx, key, 60*time.Second)
	}
	if count > int64(s.cfg.ChatRateLimitPerMinute) {
		s.logger.WarnCtx(ctx, "chat rate limited", map[string]any{
			"user_id": userID, "agent_id": agentID, "count": count, "limit": s.cfg.ChatRateLimitPerMinute,
		})
		return fmt.Errorf("chat rate limited: %w", agent.ErrChatRateLimited)
	}
	return nil
}
