package middleware

import (
	"errors"
	"strings"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/user"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	"github.com/eyrihe999-stack/Synapse/internal/common/jwt"
	"github.com/gin-gonic/gin"
)

// defaultAbsoluteSessionTTL 当上游没配 cfg.JWT.AbsoluteSessionTTL 时使用。
// 30 天:和 config 默认值保持一致。
const defaultAbsoluteSessionTTL = 30 * 24 * time.Hour

// AuthRejectReason M2.7 401/403 拒绝原因枚举,snake_case 稳定不变,供告警 / 仪表盘 / 指标聚合用。
type AuthRejectReason string

const (
	ReasonMissingHeader  AuthRejectReason = "missing_header"
	ReasonBadFormat      AuthRejectReason = "bad_format"
	ReasonInvalidToken   AuthRejectReason = "invalid_token"
	ReasonTokenExpired   AuthRejectReason = "token_expired"
	ReasonSessionRevoked AuthRejectReason = "session_revoked"
	ReasonSessionExpired AuthRejectReason = "session_expired"
)

// authLog 供本包所有 middleware 写拒绝日志用。默认 SimpleLogger(stderr)兜底防 nil panic,
// main.go 启动时调 SetLogger 注入全局 logger,让告警走同一条 SLS / lumberjack 管道。
var authLog logger.LoggerInterface = logger.NewSimpleLogger()

// SetLogger 注入主 logger。调用时机:gin router 构造后、第一次挂中间件之前。
// 多次调以最后一次为准(通常只调一次)。
func SetLogger(l logger.LoggerInterface) {
	if l != nil {
		authLog = l
	}
}

// logAuthReject 在 401/403 Abort 前统一调一次,标准字段:reason + path + method + ip。
// 不打 token 明文、不打 Authorization header —— 日志阅读者拿不到登录凭证。
func logAuthReject(c *gin.Context, reason AuthRejectReason) {
	authLog.WarnCtx(c.Request.Context(), "auth rejected", map[string]any{
		"reason": string(reason),
		"method": c.Request.Method,
		"path":   c.Request.URL.Path,
		"ip":     c.ClientIP(),
	})
}

// isSessionExpired 根据 SessionStartAt(session 首次创建时间,refresh 不更新)判断是否超绝对 TTL。
// 历史 session 没写 SessionStartAt(值为 0) → fallback 用 LoginAt,避免老 session 在升级后立刻被踢。
func isSessionExpired(info *user.SessionInfo, absoluteTTL time.Duration) bool {
	if info == nil {
		return false
	}
	start := info.SessionStartAt
	if start == 0 {
		start = info.LoginAt // 兼容升级前无 SessionStartAt 的老 session
	}
	if start == 0 {
		return false
	}
	age := time.Now().UTC().Unix() - start
	return age > int64(absoluteTTL.Seconds())
}

// injectAuthCtx 把 user_id 和 device_id(作为 session_id)塞进 request context,
// 之后各模块调 *Ctx 日志方法时会自动带上这两个字段。device_id 为空不写 session_id。
func injectAuthCtx(c *gin.Context, userID uint64, deviceID string) {
	ctx := logger.WithUserID(c.Request.Context(), userID)
	if deviceID != "" {
		ctx = logger.WithSessionID(ctx, deviceID)
	}
	c.Request = c.Request.WithContext(ctx)
}

// JWTAuth is a middleware that validates JWT access tokens
func JWTAuth(jwtManager *jwt.JWTManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			logAuthReject(c, ReasonMissingHeader)
			response.Unauthorized(c, "Missing authorization header", "")
			c.Abort()
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			logAuthReject(c, ReasonBadFormat)
			response.Unauthorized(c, "Invalid authorization header format", "Expected format: Bearer <token>")
			c.Abort()
			return
		}

		token := parts[1]

		claims, err := jwtManager.ValidateAccessToken(token)
		if err != nil {
			if errors.Is(err, jwt.ErrExpiredToken) {
				logAuthReject(c, ReasonTokenExpired)
				response.Unauthorized(c, "Token has expired", "Please refresh your token")
			} else {
				logAuthReject(c, ReasonInvalidToken)
				response.Unauthorized(c, "Invalid token", err.Error())
			}
			c.Abort()
			return
		}

		c.Set("user_id", claims.UserID)
		c.Set("user_email", claims.Email)
		c.Set("device_id", claims.DeviceID)
		c.Set("jwt_claims", claims)
		injectAuthCtx(c, claims.UserID, claims.DeviceID)

		c.Next()
	}
}

// OptionalJWTAuth validates JWT tokens but doesn't require them
func OptionalJWTAuth(jwtManager *jwt.JWTManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.Next()
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.Next()
			return
		}

		token := parts[1]
		claims, err := jwtManager.ValidateAccessToken(token)
		if err == nil {
			c.Set("user_id", claims.UserID)
			c.Set("user_email", claims.Email)
			c.Set("device_id", claims.DeviceID)
			c.Set("jwt_claims", claims)
			injectAuthCtx(c, claims.UserID, claims.DeviceID)
		}

		c.Next()
	}
}

func GetUserID(c *gin.Context) (uint64, bool) {
	userID, exists := c.Get("user_id")
	if !exists {
		return 0, false
	}
	v, ok := userID.(uint64)
	return v, ok
}

//sayso-lint:ignore unused-export
func GetUserEmail(c *gin.Context) (string, bool) {
	email, exists := c.Get("user_email")
	if !exists {
		return "", false
	}
	v, ok := email.(string)
	return v, ok
}

//sayso-lint:ignore unused-export
func GetDeviceID(c *gin.Context) (string, bool) {
	deviceID, exists := c.Get("device_id")
	if !exists {
		return "", false
	}
	v, ok := deviceID.(string)
	return v, ok
}

// JWTAuthWithSession 在 JWT 校验通过后,额外检查 Redis session 是否存在 + 是否超绝对 TTL。
// 如果 session 已被删除(设备被踢下线),返回 401 revoked;
// 如果 session.SessionStartAt + 绝对 TTL < now,返回 401 session expired。
//
// absoluteTTL 可选(variadic 减少跨模块签名扩散):不传 / 传 0 走代码默认 30d,
// user 模块的 user handler 会在装配时显式传入 cfg.JWT.AbsoluteSessionTTL。
func JWTAuthWithSession(jwtManager *jwt.JWTManager, sessionStore user.SessionStore, absoluteTTL ...time.Duration) gin.HandlerFunc {
	ttl := defaultAbsoluteSessionTTL
	if len(absoluteTTL) > 0 && absoluteTTL[0] > 0 {
		ttl = absoluteTTL[0]
	}
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			logAuthReject(c, ReasonMissingHeader)
			response.Unauthorized(c, "Missing authorization header", "")
			c.Abort()
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			logAuthReject(c, ReasonBadFormat)
			response.Unauthorized(c, "Invalid authorization header format", "Expected format: Bearer <token>")
			c.Abort()
			return
		}

		claims, err := jwtManager.ValidateAccessToken(parts[1])
		if err != nil {
			if errors.Is(err, jwt.ErrExpiredToken) {
				logAuthReject(c, ReasonTokenExpired)
				response.Unauthorized(c, "Token has expired", "Please refresh your token")
			} else {
				logAuthReject(c, ReasonInvalidToken)
				response.Unauthorized(c, "Invalid token", err.Error())
			}
			c.Abort()
			return
		}

		// 检查 Redis session 是否存在(设备是否被踢下线)
		deviceID := claims.DeviceID
		if deviceID == "" {
			deviceID = "default"
		}
		info, err := sessionStore.Get(c.Request.Context(), claims.UserID, deviceID)
		if err != nil {
			logAuthReject(c, ReasonSessionRevoked)
			response.Unauthorized(c, "Session revoked", "")
			c.Abort()
			return
		}
		// 绝对 TTL 校验:超过就是超过,哪怕 refresh token 还有效也不认。
		if isSessionExpired(info, ttl) {
			logAuthReject(c, ReasonSessionExpired)
			response.Unauthorized(c, "Session expired", "please login again")
			c.Abort()
			return
		}

		c.Set("user_id", claims.UserID)
		c.Set("user_email", claims.Email)
		c.Set("device_id", deviceID)
		c.Set("jwt_claims", claims)
		injectAuthCtx(c, claims.UserID, deviceID)

		c.Next()
	}
}

//sayso-lint:ignore unused-export
func RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		_, exists := c.Get("user_id")
		if !exists {
			response.Unauthorized(c, "Authentication required", "")
			c.Abort()
			return
		}
		c.Next()
	}
}
