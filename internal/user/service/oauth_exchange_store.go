// oauth_exchange_store.go M1.6 OAuth 登录回调的 token 中转。
//
// 为什么要走 exchange code 而不是 URL query 直接塞 access_token:
//   - access_token 放 URL 会在浏览器历史 / referer / 网关日志里留痕
//   - exchange code 是一次性的短随机串,前端兑换一次就作废,泄露影响面小
//
// Key: synapse:oauth_exchange:{code}  Value: JSON(AuthResponse)  TTL: 60s
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/common/database"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
)

// oauthExchangeTTL 从 IdP 回调到前端 pickup 的窗口,60s 足够用户浏览器跳转 + 前端 fetch。
const oauthExchangeTTL = 60 * time.Second

const oauthExchangeKeyPrefix = "synapse:oauth_exchange"

func oauthExchangeKey(code string) string { return fmt.Sprintf("%s:%s", oauthExchangeKeyPrefix, code) }

// OAuthExchangeStore OAuth 登录回调产物的一次性存取。
// AuthResponse 以 JSON 整存,前端 PickupOAuthExchange 一次 GET+DEL 拿到就删,无法重放。
type OAuthExchangeStore interface {
	Store(ctx context.Context, code string, auth *AuthResponse) error
	// Take GET 再 DEL,原子一次性消费(实现走 Lua 脚本或 WATCH+MULTI 皆可;
	// 当前走"GET 后立刻 DEL"的先读后删,竞争概率 60s 内极低且都是自家前端)。
	Take(ctx context.Context, code string) (*AuthResponse, error)
}

type redisOAuthExchangeStore struct {
	redis database.RedisInterface
	log   logger.LoggerInterface
}

// NewOAuthExchangeStore 基于 Redis 的 OAuthExchangeStore 实现。
func NewOAuthExchangeStore(rdb database.RedisInterface, log logger.LoggerInterface) OAuthExchangeStore {
	return &redisOAuthExchangeStore{redis: rdb, log: log}
}

// Store 序列化 AuthResponse 并写 Redis,60s TTL。
// JSON 序列化失败或 Redis Set 失败都会打 log 并透传包装后的 error。
func (s *redisOAuthExchangeStore) Store(ctx context.Context, code string, auth *AuthResponse) error {
	data, err := json.Marshal(auth)
	if err != nil {
		s.log.ErrorCtx(ctx, "序列化 oauth exchange 失败", err, nil)
		return fmt.Errorf("marshal oauth exchange: %w", err)
	}
	if err := s.redis.Set(ctx, oauthExchangeKey(code), string(data), oauthExchangeTTL); err != nil {
		s.log.ErrorCtx(ctx, "保存 oauth exchange 失败", err, nil)
		return fmt.Errorf("redis set oauth exchange: %w", err)
	}
	return nil
}

// Take 读出 AuthResponse,随后立刻删除(一次性)。
// miss(前端超过 60s 才 pickup、或被重放)返 (nil, err),上层统一映 ErrOAuthExchangeExpired。
func (s *redisOAuthExchangeStore) Take(ctx context.Context, code string) (*AuthResponse, error) {
	val, err := s.redis.Get(ctx, oauthExchangeKey(code))
	if err != nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, err // miss 走正常路径,不打 log,由上层映 sentinel
	}
	// 尽早删,防止前端重复 pickup
	//sayso-lint:ignore err-swallow
	_ = s.redis.Del(ctx, oauthExchangeKey(code)) // best-effort,TTL 兜底自销
	var auth AuthResponse
	if err := json.Unmarshal([]byte(val), &auth); err != nil {
		s.log.ErrorCtx(ctx, "反序列化 oauth exchange 失败", err, nil)
		return nil, fmt.Errorf("unmarshal oauth exchange: %w", err)
	}
	return &auth, nil
}
