// ratelimit.go 简化的限流服务,基于 Redis 原子 INCR+EXPIRE,Redis 不可用时降级到本地内存计数。
package service

import (
	"context"
	"fmt"
	"sync"
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

// localCounter 本地内存计数器,用于 Redis 不可用时的降级限流。
// count 与 resetAt 在同一锁下读写,避免"窗口边界重置"与"自增"交叠导致计数丢失。
type localCounter struct {
	mu      sync.Mutex
	count   int64
	resetAt int64 // unix timestamp
}

// fallbackSweepInterval 本地降级计数 map 的清理间隔。
// Redis 长时间故障期间,每个独立 (user, agent) 对都会在 fallback map 留一个 entry,
// Redis 恢复后这些 entry 不再被访问,需要定期清理避免内存累积。
const fallbackSweepInterval = 5 * time.Minute

type rateLimitService struct {
	cfg      Config
	rdb      database.RedisInterface
	logger   logger.LoggerInterface
	fallback sync.Map // key -> *localCounter
}

// NewRateLimitService 构造限流服务。当 cfg.ChatRateLimitPerMinute <= 0 时使用默认值。
// 同时启动 fallback map 的后台清理 goroutine,daemon 性质,进程退出时自然结束。
func NewRateLimitService(cfg Config, rdb database.RedisInterface, log logger.LoggerInterface) RateLimitService {
	if cfg.ChatRateLimitPerMinute <= 0 {
		cfg.ChatRateLimitPerMinute = agent.DefaultChatRateLimitPerMinute
	}
	s := &rateLimitService{cfg: cfg, rdb: rdb, logger: log}
	go s.sweepFallbackLoop()
	return s
}

// sweepFallbackLoop 周期性清理 fallback map 中已过窗口的 entry。
// 只删 resetAt > 0 且 resetAt < now 的项(说明被使用过且窗口已过),不删新建的零值 entry,
// 避免与并发 LoadOrStore 抢同一个 key 时把刚创建的计数误删。
func (s *rateLimitService) sweepFallbackLoop() {
	t := time.NewTicker(fallbackSweepInterval)
	defer t.Stop()
	for range t.C {
		now := time.Now().Unix()
		s.fallback.Range(func(k, v any) bool {
			lc, ok := v.(*localCounter)
			if !ok {
				return true
			}
			lc.mu.Lock()
			stale := lc.resetAt > 0 && lc.resetAt < now
			lc.mu.Unlock()
			if stale {
				s.fallback.Delete(k)
			}
			return true
		})
	}
}

// CheckChatLimit 基于 Redis 固定窗口检查 user+agent 每分钟对话频率。
// 使用 Lua 脚本保证 INCR + EXPIRE 原子,避免 Expire 失败导致 key 永不过期、用户被永久限流。
// Redis 不可用时降级到本地内存计数(仅单实例内有效)。
// 超限时返回 ErrChatRateLimited。
func (s *rateLimitService) CheckChatLimit(ctx context.Context, userID, agentID uint64) error {
	key := fmt.Sprintf("synapse:rl:chat:%d:%d", userID, agentID)
	count, err := s.rdb.IncrAndExpireIfNew(ctx, key, 60*time.Second)
	if err != nil {
		// Redis 不可用,降级到本地内存限流
		s.logger.WarnCtx(ctx, "rate limit redis failed, falling back to local counter", map[string]any{
			"error": err.Error(), "user_id": userID, "agent_id": agentID,
		})
		return s.checkLocal(ctx, key, userID, agentID)
	}
	if count > int64(s.cfg.ChatRateLimitPerMinute) {
		s.logger.WarnCtx(ctx, "chat rate limited", map[string]any{
			"user_id": userID, "agent_id": agentID, "count": count, "limit": s.cfg.ChatRateLimitPerMinute,
		})
		return fmt.Errorf("chat rate limited: %w", agent.ErrChatRateLimited)
	}
	return nil
}

// checkLocal 本地内存降级限流:每 60 秒重置计数。
func (s *rateLimitService) checkLocal(ctx context.Context, key string, userID, agentID uint64) error {
	now := time.Now().Unix()
	val, _ := s.fallback.LoadOrStore(key, &localCounter{})
	lc := val.(*localCounter)

	lc.mu.Lock()
	if now >= lc.resetAt {
		lc.count = 0
		lc.resetAt = now + 60
	}
	lc.count++
	count := lc.count
	lc.mu.Unlock()

	if count > int64(s.cfg.ChatRateLimitPerMinute) {
		s.logger.WarnCtx(ctx, "chat rate limited (local fallback)", map[string]any{
			"user_id": userID, "agent_id": agentID, "count": count, "limit": s.cfg.ChatRateLimitPerMinute,
		})
		return fmt.Errorf("chat rate limited: %w", agent.ErrChatRateLimited)
	}
	return nil
}
