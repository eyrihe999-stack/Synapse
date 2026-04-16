// ratelimit_service.go 四层叠加限流 service,基于 Redis ZSET 滑动窗口。
//
// 四个维度:
//   - User Global: key = "sayso:rl:user:{user_id}"
//   - Org Global:  key = "sayso:rl:org:{org_id}"
//   - Agent Global: key = "sayso:rl:agent:{agent_id}"(limit 来自 agent.rate_limit_per_minute)
//   - User-Agent:  key = "sayso:rl:ua:{user_id}:{agent_id}"
//
// 算法:Redis ZSET + Lua 原子脚本
//
//	local now = tonumber(ARGV[1])
//	local window = tonumber(ARGV[2])
//	local limit = tonumber(ARGV[3])
//	redis.call('ZREMRANGEBYSCORE', key, 0, now - window)
//	local count = redis.call('ZCARD', key)
//	if count >= limit then return 0 end
//	redis.call('ZADD', key, now, ARGV[4])
//	redis.call('EXPIRE', key, ARGV[5])
//	return 1
//
// 超限返回对应的 ErrXxxRateLimited(handler 映射为 HTTP 429)。
package service

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/agent"
	"github.com/eyrihe999-stack/Synapse/pkg/database"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"github.com/go-redis/redis/v8"
)

// RateLimitService 对外提供"检查并计数"原子操作。
//sayso-lint:ignore interface-pollution
type RateLimitService interface {
	// CheckAndConsume 按顺序检查 user / org / agent / user-agent 四个维度,任一超限即返回对应的 sentinel error。
	// 通过则消耗一个令牌(ZADD 当前时间戳)。
	// agentLimit 是 per-agent 的限制值(来自 agent.rate_limit_per_minute);其它维度使用 Config 默认值。
	CheckAndConsume(ctx context.Context, userID, orgID, agentID uint64, agentLimit int, nonce string) error
}

// NewRateLimitService 构造 RateLimitService 实例。
func NewRateLimitService(cfg Config, redisDB database.RedisInterface, log logger.LoggerInterface) RateLimitService {
	return &rateLimitService{
		cfg:     cfg,
		redis:   redisDB,
		logger:  log,
		script:  redis.NewScript(luaSlidingWindow),
	}
}

type rateLimitService struct {
	cfg    Config
	redis  database.RedisInterface
	logger logger.LoggerInterface
	script *redis.Script
}

// luaSlidingWindow 滑动窗口原子脚本。
//
//	KEYS[1] = zset key
//	ARGV[1] = now (milliseconds)
//	ARGV[2] = window (milliseconds)
//	ARGV[3] = limit
//	ARGV[4] = member (nonce,保证每次调用唯一)
//	ARGV[5] = ttl seconds
//
// 返回 1 = 通过,0 = 超限。
const luaSlidingWindow = `
local now    = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local limit  = tonumber(ARGV[3])
redis.call('ZREMRANGEBYSCORE', KEYS[1], 0, now - window)
local count = redis.call('ZCARD', KEYS[1])
if count >= limit then
	return 0
end
redis.call('ZADD', KEYS[1], now, ARGV[4])
redis.call('EXPIRE', KEYS[1], tonumber(ARGV[5]))
return 1
`

// CheckAndConsume 顺序检查四个维度。
// 任一层 Redis 故障都降级为"放行 + 打 WARN"(优先保证可用性)。
func (s *rateLimitService) CheckAndConsume(ctx context.Context, userID, orgID, agentID uint64, agentLimit int, nonce string) error {
	if nonce == "" {
		nonce = strconv.FormatInt(time.Now().UTC().UnixNano(), 10)
	}
	// 顺序:user → org → agent → ua;从最粗粒度到最细粒度
	checks := []struct {
		key   string
		limit int
		err   error
	}{
		{fmt.Sprintf("sayso:rl:user:%d", userID), s.cfg.UserGlobalRatePerMinute, agent.ErrUserRateLimited},
		{fmt.Sprintf("sayso:rl:org:%d", orgID), s.cfg.OrgGlobalRatePerMinute, agent.ErrOrgRateLimited},
		{fmt.Sprintf("sayso:rl:agent:%d", agentID), agentLimit, agent.ErrAgentRateLimited},
		{fmt.Sprintf("sayso:rl:ua:%d:%d", userID, agentID), s.cfg.UserAgentRatePerMinute, agent.ErrUserAgentRateLimited},
	}
	for _, c := range checks {
		if c.limit <= 0 {
			continue // 无限制(不太可能,配置层应保证 > 0)
		}
		allowed, err := s.run(ctx, c.key, c.limit, nonce)
		if err != nil {
			// Redis 故障降级:放行并记录 WARN,避免因限流组件不可用导致全站阻塞
			s.logger.WarnCtx(ctx, "ratelimit redis error, fail-open", map[string]any{
				"key":   c.key,
				"error": err.Error(),
			})
			continue
		}
		if !allowed {
			return fmt.Errorf("rate limited: %w", c.err)
		}
	}
	return nil
}

// run 执行单次 Lua 脚本,返回是否放行。
func (s *rateLimitService) run(ctx context.Context, key string, limit int, nonce string) (bool, error) {
	client := s.redis.GetClient()
	if client == nil {
		return true, nil
	}
	now := time.Now().UTC().UnixMilli()
	window := int64(60 * 1000)
	ttl := int64(65)
	res, err := s.script.Run(ctx, client, []string{key}, now, window, limit, nonce, ttl).Int64()
	if err != nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return false, err
	}
	return res == 1, nil
}
