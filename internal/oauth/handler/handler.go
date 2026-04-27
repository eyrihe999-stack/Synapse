// Package handler OAuth 模块 HTTP 端点聚合。
package handler

import (
	"context"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/oauth/service"
)

// DCRRateLimiter 精简接口,包 database.RedisInterface.SlidingWindowAdd 一层。
type DCRRateLimiter interface {
	Add(ctx context.Context, key string, now time.Time, window time.Duration) (int64, error)
}

// DCRRateLimiterFunc 函数适配器。
type DCRRateLimiterFunc func(ctx context.Context, key string, now time.Time, window time.Duration) (int64, error)

// Add 实现 DCRRateLimiter。
func (f DCRRateLimiterFunc) Add(ctx context.Context, key string, now time.Time, window time.Duration) (int64, error) {
	return f(ctx, key, now, window)
}

// Handler OAuth 模块所有 HTTP endpoint 的持有者。
type Handler struct {
	svc *service.Service
	log logger.LoggerInterface

	// DCR 限速(per-IP)
	dcrRateLimiter        DCRRateLimiter
	dcrRateLimitWindowSec int
	dcrRateLimitMax       int

	// .well-known metadata
	metadata MetadataProvider

	// OAuth consent flow 用:session store / user 认证 / agent 自动建
	sessionStore      service.OAuthSessionStore
	userAuthenticator service.UserAuthenticator
	agentBootstrapper service.AgentBootstrapper

	// Token TTL(token / refresh / code TTL 由 config 注入)
	accessTokenTTL       time.Duration
	refreshTokenTTL      time.Duration
	authorizationCodeTTL time.Duration

	// Cookie 安全属性(dev 允许 http / 生产要 https + Secure)
	cookieSecure bool
}

// Config 构造 Handler 的参数。
type Config struct {
	RateLimiter           DCRRateLimiter
	DCRRateLimitWindowSec int
	DCRRateLimitMax       int
	Metadata              MetadataProvider

	SessionStore      service.OAuthSessionStore
	UserAuthenticator service.UserAuthenticator
	AgentBootstrapper service.AgentBootstrapper

	AccessTokenTTL       time.Duration
	RefreshTokenTTL      time.Duration
	AuthorizationCodeTTL time.Duration

	CookieSecure bool // dev=false, prod=true
}

// NewHandler 构造。
func NewHandler(svc *service.Service, cfg Config, log logger.LoggerInterface) *Handler {
	w, m := ensureDCRRateLimitDefaults(cfg.DCRRateLimitWindowSec, cfg.DCRRateLimitMax)
	return &Handler{
		svc:                   svc,
		log:                   log,
		dcrRateLimiter:        cfg.RateLimiter,
		dcrRateLimitWindowSec: w,
		dcrRateLimitMax:       m,
		metadata:              cfg.Metadata,
		sessionStore:          cfg.SessionStore,
		userAuthenticator:     cfg.UserAuthenticator,
		agentBootstrapper:     cfg.AgentBootstrapper,
		accessTokenTTL:        cfg.AccessTokenTTL,
		refreshTokenTTL:       cfg.RefreshTokenTTL,
		authorizationCodeTTL:  cfg.AuthorizationCodeTTL,
		cookieSecure:          cfg.CookieSecure,
	}
}
