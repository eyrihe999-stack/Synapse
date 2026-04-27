// Package middleware OAuth / MCP 模块共用的中间件。
package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/common/async"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/oauth/repository"
)

// Context keys(gin Context 里统一存 (user_id, agent_principal_id, client_id, auth_method))。
// MCP tool handler 依赖这些 key 从 ctx 取调用者身份。
const (
	CtxKeyUserID            = "mcp_user_id"
	CtxKeyAgentPrincipalID  = "mcp_agent_principal_id"
	CtxKeyOAuthClientID     = "mcp_oauth_client_id" // OAuth 路径才有,PAT 路径为空
	CtxKeyAuthMethod        = "mcp_auth_method"     // "oauth" / "pat"
)

// BearerAuthResult 认证通过后落入 gin Context 的元信息。
type BearerAuthResult struct {
	UserID           uint64
	AgentPrincipalID uint64
	OAuthClientID    string // 空串表示 PAT 认证
	AuthMethod       string // "oauth" / "pat"
}

// Hasher 计算 token hash(默认 sha256)。为测试 / 未来换算法留抽象。
type Hasher func(s string) string

// BearerAuth 统一 Bearer 认证中间件 —— 查 oauth_access_tokens 和 user_pats,
// 命中 → 把 (user_id, agent_principal_id, ...) 注入 gin Context;否则 401。
//
// 命中顺序:先 OAuth access_token,再 PAT(PAT token 前缀 "syn_pat_",OAuth 是 "syn_at_",
// 前缀判断可做快速路由,但对 DB 查询优化有限;统一 sha256 hash 查 UNIQUE 索引即可)。
//
// last_used_at 异步更新走 AsyncRunner:获得 admission control(DB 抖动时 goroutine 不
// 无界堆积)、panic recovery、进程关停时的 graceful wait。runner 由上层注入,main.go
// 统一在 server shutdown 前 Shutdown 它。
func BearerAuth(repo repository.Repository, hasher Hasher, runner *async.AsyncRunner, log logger.LoggerInterface) gin.HandlerFunc {
	if hasher == nil {
		return func(c *gin.Context) {
			writeBearerError(c, http.StatusInternalServerError, "invalid_setup", "hasher not configured")
		}
	}
	if runner == nil {
		return func(c *gin.Context) {
			writeBearerError(c, http.StatusInternalServerError, "invalid_setup", "async runner not configured")
		}
	}

	return func(c *gin.Context) {
		tok, ok := extractBearer(c)
		if !ok {
			writeBearerError(c, http.StatusUnauthorized, "invalid_token", "missing bearer token")
			return
		}

		hash := hasher(tok)
		now := time.Now().UTC()

		// 1. 先查 OAuth access_token
		if atRow, _ := repo.FindAccessTokenByHash(c.Request.Context(), hash); atRow != nil {
			if atRow.RevokedAt != nil {
				writeBearerError(c, http.StatusUnauthorized, "invalid_token", "access_token revoked")
				return
			}
			if now.After(atRow.ExpiresAt) {
				writeBearerError(c, http.StatusUnauthorized, "invalid_token", "access_token expired")
				return
			}
			// 异步 touch last_used_at(失败只 log,不阻塞)。runner 满 / 已 Shutdown 时
			// 直接丢弃这次 touch —— 它本来就是 best-effort,不能让鉴权路径被它拖累。
			//sayso-lint:ignore err-swallow
			_ = runner.Go(c.Request.Context(), "bearer-auth:touch-at", func(rctx context.Context) {
				touchAccessToken(rctx, repo, atRow.ID, now, log)
			})
			injectAuth(c, BearerAuthResult{
				UserID:           atRow.UserID,
				AgentPrincipalID: atRow.AgentID,
				OAuthClientID:    atRow.ClientID,
				AuthMethod:       "oauth",
			})
			c.Next()
			return
		}

		// 2. PAT
		if patRow, _ := repo.FindPATByHash(c.Request.Context(), hash); patRow != nil {
			if patRow.RevokedAt != nil {
				writeBearerError(c, http.StatusUnauthorized, "invalid_token", "pat revoked")
				return
			}
			if patRow.ExpiresAt != nil && now.After(*patRow.ExpiresAt) {
				writeBearerError(c, http.StatusUnauthorized, "invalid_token", "pat expired")
				return
			}
			//sayso-lint:ignore err-swallow
			_ = runner.Go(c.Request.Context(), "bearer-auth:touch-pat", func(rctx context.Context) {
				touchPAT(rctx, repo, patRow.ID, now, log)
			})
			injectAuth(c, BearerAuthResult{
				UserID:           patRow.UserID,
				AgentPrincipalID: patRow.AgentID,
				OAuthClientID:    "",
				AuthMethod:       "pat",
			})
			c.Next()
			return
		}

		writeBearerError(c, http.StatusUnauthorized, "invalid_token", "unknown token")
	}
}

// BearerOrJWT 复合鉴权 —— 给"同时被 daemon (PAT/OAuth) 和浏览器 (JWT) 访问"的
// endpoint 用,比如 SSE 事件流。
//
// 难点:JWT 也走 Authorization: Bearer 头(前端 axios interceptor 把 access_token
// 当 Bearer 发),所以不能按"有没有 Bearer 头"分流,要按 **token 前缀** 看实际类型:
//   - syn_pat_*  → PAT(走 BearerAuth)
//   - syn_at_*   → OAuth access_token(走 BearerAuth)
//   - 其他(JWT base64 段,通常以 eyJ 开头) → 走 JWTAuthWithSession
//   - 没 Bearer 头  → 走 JWTAuthWithSession(它自己会拒掉)
//
// 两个中间件向 c 注入的 user 身份 key 不同(BearerAuth=CtxKeyUserID="mcp_user_id",
// JWTAuthWithSession="user_id"),下游 handler 要自己兼容(见
// channel/handler/sse_handler.go 的 resolveCallerIdentity)。
//
// 注意:c.AbortWith* 由各中间件自己处理,本函数只做分发。
func BearerOrJWT(bearer gin.HandlerFunc, jwt gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		tok, ok := extractBearer(c)
		if !ok {
			// 没 Bearer 头 —— 让 JWT middleware 拒
			jwt(c)
			return
		}
		if isSynapseBearerToken(tok) {
			bearer(c)
			return
		}
		jwt(c)
	}
}

// isSynapseBearerToken 判断是不是 PAT 或 OAuth access_token(都由 oauth 模块发,
// 有固定前缀)。其他 token(典型:登录态 JWT)走 JWT 中间件。
func isSynapseBearerToken(tok string) bool {
	return strings.HasPrefix(tok, "syn_pat_") || strings.HasPrefix(tok, "syn_at_")
}

// extractBearer 从 Authorization header 取 "Bearer xxx"。
func extractBearer(c *gin.Context) (string, bool) {
	auth := c.GetHeader("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return "", false
	}
	tok := strings.TrimSpace(auth[len(prefix):])
	return tok, tok != ""
}

// authContextKey 用于 context.Context,独立 type 避免和其他包 key 撞。
type authContextKey struct{}

func injectAuth(c *gin.Context, r BearerAuthResult) {
	// 写 gin context —— 给 gin handler 用(request.Context 不易直接拿)
	c.Set(CtxKeyUserID, r.UserID)
	c.Set(CtxKeyAgentPrincipalID, r.AgentPrincipalID)
	c.Set(CtxKeyOAuthClientID, r.OAuthClientID)
	c.Set(CtxKeyAuthMethod, r.AuthMethod)
	// 同步写 request.Context —— 给 gin.WrapH 挂的 http.Handler 用(MCP server)。
	// gin 链后续 handler 读 request.Context 也能拿到。
	newCtx := context.WithValue(c.Request.Context(), authContextKey{}, r)
	c.Request = c.Request.WithContext(newCtx)
}

// AuthFromContext 从纯 context.Context 读回身份(通过 request.Context 传进来的)。
// MCP tool handler 用这个取身份。
func AuthFromContext(ctx context.Context) (BearerAuthResult, bool) {
	v, ok := ctx.Value(authContextKey{}).(BearerAuthResult)
	return v, ok && v.UserID != 0
}

// GetAuth 从 gin.Context 读回中间件注入的身份三元组。
// 没走中间件时返 (BearerAuthResult{}, false)。
func GetAuth(c *gin.Context) (BearerAuthResult, bool) {
	uidAny, ok := c.Get(CtxKeyUserID)
	if !ok {
		return BearerAuthResult{}, false
	}
	uid, _ := uidAny.(uint64)
	apAny, _ := c.Get(CtxKeyAgentPrincipalID)
	ap, _ := apAny.(uint64)
	cidAny, _ := c.Get(CtxKeyOAuthClientID)
	cid, _ := cidAny.(string)
	methodAny, _ := c.Get(CtxKeyAuthMethod)
	method, _ := methodAny.(string)
	return BearerAuthResult{
		UserID: uid, AgentPrincipalID: ap, OAuthClientID: cid, AuthMethod: method,
	}, uid != 0
}

// ── 错误响应 ──────────────────────────────────────────────────────────

// writeBearerError 按 OAuth 2 Bearer(RFC 6750)的 WWW-Authenticate header 风格返。
// MCP 要求对无效 token 返 401 + 指向 AS 的 resource_metadata URL,那个由调用点
// 扩展;本中间件只做最简 401 + JSON error。
func writeBearerError(c *gin.Context, httpStatus int, code, description string) {
	c.Header("WWW-Authenticate", fmt.Sprintf(`Bearer error="%s", error_description="%s"`, code, description))
	c.AbortWithStatusJSON(httpStatus, map[string]string{
		"error":             code,
		"error_description": description,
	})
}

// ── 异步 touch ────────────────────────────────────────────────────────
//
// rctx 来自 AsyncRunner.fn —— 它是 runner 级 ctx(独立于请求 ctx),
// runner.Shutdown 时会 cancel 让本函数及时退出。另外套一层 2s 超时防止单条 DB
// 抖动挂太久拖慢 runner 整体吞吐。

func touchAccessToken(rctx context.Context, repo repository.Repository, id uint64, at time.Time, log logger.LoggerInterface) {
	ctx, cancel := context.WithTimeout(rctx, 2*time.Second)
	defer cancel()
	if err := repo.TouchAccessTokenLastUsed(ctx, id, at); err != nil && !errors.Is(err, context.Canceled) {
		log.WarnCtx(ctx, "bearer_auth: touch access_token failed", map[string]any{
			"token_id": id, "err": err.Error(),
		})
	}
}

func touchPAT(rctx context.Context, repo repository.Repository, id uint64, at time.Time, log logger.LoggerInterface) {
	ctx, cancel := context.WithTimeout(rctx, 2*time.Second)
	defer cancel()
	if err := repo.TouchPATLastUsed(ctx, id, at); err != nil && !errors.Is(err, context.Canceled) {
		log.WarnCtx(ctx, "bearer_auth: touch pat failed", map[string]any{
			"token_id": id, "err": err.Error(),
		})
	}
}
