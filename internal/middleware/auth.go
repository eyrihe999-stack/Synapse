package middleware

import (
	"strings"

	oauthsvc "github.com/eyrihe999-stack/Synapse/internal/oauth/service"
	"github.com/eyrihe999-stack/Synapse/internal/user"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
	"github.com/eyrihe999-stack/Synapse/pkg/utils"
	"github.com/gin-gonic/gin"
)

// JWTAuth is a middleware that validates JWT access tokens
func JWTAuth(jwtManager *utils.JWTManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			response.Unauthorized(c, "Missing authorization header", "")
			c.Abort()
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			response.Unauthorized(c, "Invalid authorization header format", "Expected format: Bearer <token>")
			c.Abort()
			return
		}

		token := parts[1]

		claims, err := jwtManager.ValidateAccessToken(token)
		if err != nil {
			if err == utils.ErrExpiredToken {
				response.Unauthorized(c, "Token has expired", "Please refresh your token")
			} else {
				response.Unauthorized(c, "Invalid token", err.Error())
			}
			c.Abort()
			return
		}

		c.Set("user_id", claims.UserID)
		c.Set("user_email", claims.Email)
		c.Set("device_id", claims.DeviceID)
		c.Set("jwt_claims", claims)

		c.Next()
	}
}

// OptionalJWTAuth validates JWT tokens but doesn't require them
func OptionalJWTAuth(jwtManager *utils.JWTManager) gin.HandlerFunc {
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
		}

		c.Next()
	}
}

func GetUserID(c *gin.Context) (uint64, bool) {
	userID, exists := c.Get("user_id")
	if !exists {
		return 0, false
	}
	return userID.(uint64), true
}

func GetUserEmail(c *gin.Context) (string, bool) {
	email, exists := c.Get("user_email")
	if !exists {
		return "", false
	}
	return email.(string), true
}

func GetDeviceID(c *gin.Context) (string, bool) {
	deviceID, exists := c.Get("device_id")
	if !exists {
		return "", false
	}
	return deviceID.(string), true
}

// JWTAuthWithSession 在 JWT 校验通过后,额外检查 Redis session 是否存在。
// 如果 session 已被删除(设备被踢下线),返回 401。
func JWTAuthWithSession(jwtManager *utils.JWTManager, sessionStore user.SessionStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			response.Unauthorized(c, "Missing authorization header", "")
			c.Abort()
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			response.Unauthorized(c, "Invalid authorization header format", "Expected format: Bearer <token>")
			c.Abort()
			return
		}

		claims, err := jwtManager.ValidateAccessToken(parts[1])
		if err != nil {
			if err == utils.ErrExpiredToken {
				response.Unauthorized(c, "Token has expired", "Please refresh your token")
			} else {
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
		if _, err := sessionStore.Get(c.Request.Context(), claims.UserID, deviceID); err != nil {
			response.Unauthorized(c, "Session revoked", "")
			c.Abort()
			return
		}

		c.Set("user_id", claims.UserID)
		c.Set("user_email", claims.Email)
		c.Set("device_id", deviceID)
		c.Set("jwt_claims", claims)

		c.Next()
	}
}

// BearerAuth 双向兼容:先尝试 OAuth access token(agent CLI 走这条),失败再试 web JWT(浏览器 / webapp)。
//
// 为什么需要:
//   - agent CLI 用 OAuth desktop flow 拿到的是 oauth-access+jwt(自己的 signing key)
//   - web UI 登录拿到的是 web JWT(JWT.SecretKey 签)
// 两者签名密钥不同,只认一种的 middleware 会拒另一种。
//
// oauthSvc 为 nil 时退化等同 JWTAuth(OAuth 模块没启用时的安全兜底)。
//
// 上下文注入:
//   - OAuth 路径:user_id 来自 claims.Subject,另设 oauth_claims(含 org_id / scope)
//   - JWT 路径:和 JWTAuth 保持一致(user_id / user_email / device_id / jwt_claims)
func BearerAuth(jwtManager *utils.JWTManager, oauthSvc oauthsvc.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			response.Unauthorized(c, "Missing authorization header", "")
			c.Abort()
			return
		}
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			response.Unauthorized(c, "Invalid authorization header format", "Expected: Bearer <token>")
			c.Abort()
			return
		}
		token := parts[1]

		// 1) 先试 OAuth(若启用)。ValidateAccessToken 内部强制 typ=oauth-access+jwt,
		// 传来的 web JWT 会因 typ 不匹配被拒,不会误通过。
		if oauthSvc != nil {
			if claims, err := oauthSvc.ValidateAccessToken(token); err == nil {
				c.Set("user_id", claims.Subject)
				c.Set("oauth_claims", claims)
				c.Next()
				return
			}
		}

		// 2) 回落 web JWT
		claims, err := jwtManager.ValidateAccessToken(token)
		if err != nil {
			if err == utils.ErrExpiredToken {
				response.Unauthorized(c, "Token has expired", "Please refresh your token")
			} else {
				response.Unauthorized(c, "Invalid token", err.Error())
			}
			c.Abort()
			return
		}
		c.Set("user_id", claims.UserID)
		c.Set("user_email", claims.Email)
		c.Set("device_id", claims.DeviceID)
		c.Set("jwt_claims", claims)
		c.Next()
	}
}

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
