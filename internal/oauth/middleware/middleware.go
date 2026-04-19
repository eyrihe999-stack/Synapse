// Package middleware 验 OAuth access token 的 gin middleware。
//
// 行为:读 Authorization: Bearer <token> → 验签 + exp + typ + iss(委托 service.Service)→
// 校验 scope 必须含 "mcp" → 把 claims 注入 gin context 给 handler 用。
//
// 失败响应按 RFC 6750 §3:HTTP 401 + WWW-Authenticate Bearer error="..." 头。
// scope 不足则 403(OAuth 客户端据此知道"token 有效但不够权",别盲目 refresh)。
package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/oauth"
	"github.com/eyrihe999-stack/Synapse/internal/oauth/service"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
)

// ctxKeyOAuthClaims gin context 里放 claims 的 key。外部通过 ClaimsFromContext 取。
const ctxKeyOAuthClaims = "oauth_claims"

// AccessToken 返一个 gin middleware,挂在需要 OAuth 保护的路由组前。
//
// resourceMetadataURL 是 /.well-known/oauth-protected-resource 的绝对 URL,
// 401 时塞进 WWW-Authenticate 的 resource_metadata 参数(RFC 9728 / MCP 2025-03-26)——
// 没这条 hint,Inspector / Claude 等 MCP 客户端不知道去哪发现 AS,会卡在死循环。
//
// log 目前只给未来扩展位用;每次失败 token 不 log,防被脚本刷爆。
func AccessToken(svc service.Service, resourceMetadataURL string, _ logger.LoggerInterface) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractBearer(c.GetHeader("Authorization"))
		if token == "" {
			writeBearerChallenge(c, http.StatusUnauthorized, "invalid_request", "missing bearer token", resourceMetadataURL)
			return
		}
		claims, err := svc.ValidateAccessToken(token)
		if err != nil {
			writeBearerChallenge(c, http.StatusUnauthorized, "invalid_token", "invalid or expired access token", resourceMetadataURL)
			return
		}
		if !containsScope(claims.Scope, oauth.ScopeMCP) {
			// 403 而非 401 —— token 合法但没足够权限,client 应让用户重走 consent 加 scope。
			writeBearerChallenge(c, http.StatusForbidden, "insufficient_scope", "mcp scope required", resourceMetadataURL)
			return
		}
		c.Set(ctxKeyOAuthClaims, claims)
		c.Next()
	}
}

// ClaimsFromContext 取 middleware 注入的 claims。下游 handler / mcpserver 按此拿 orgID / userID / scope。
func ClaimsFromContext(c *gin.Context) (*service.AccessTokenClaims, bool) {
	v, ok := c.Get(ctxKeyOAuthClaims)
	if !ok {
		return nil, false
	}
	claims, ok := v.(*service.AccessTokenClaims)
	return claims, ok
}

// extractBearer "Bearer xxx" → "xxx";格式不对返 ""。
// 对大小写不敏感(RFC 7235 auth scheme 本就 case-insensitive,某些 client 发 "bearer" 小写)。
func extractBearer(authHeader string) string {
	if authHeader == "" {
		return ""
	}
	const prefix = "Bearer "
	if len(authHeader) > len(prefix) && strings.EqualFold(authHeader[:len(prefix)], prefix) {
		return strings.TrimSpace(authHeader[len(prefix):])
	}
	return ""
}

// containsScope "mcp" 在空格分隔的 scope 字段里是否存在。
func containsScope(scope, required string) bool {
	for s := range strings.FieldsSeq(scope) {
		if s == required {
			return true
		}
	}
	return false
}

// writeBearerChallenge RFC 6750 §3 + RFC 9728 规范的 WWW-Authenticate 头。
// resourceMetadataURL 非空时带 resource_metadata="..." 参数 —— MCP 客户端据此发现 AS。
// 注意 error_description / URL 里不得有 " 字符 —— 这里传的都是硬编码安全串,不做 escape。
func writeBearerChallenge(c *gin.Context, status int, errCode, errDesc, resourceMetadataURL string) {
	header := `Bearer error="` + errCode + `", error_description="` + errDesc + `"`
	if resourceMetadataURL != "" {
		header += `, resource_metadata="` + resourceMetadataURL + `"`
	}
	c.Header("WWW-Authenticate", header)
	c.AbortWithStatusJSON(status, gin.H{
		"error":             errCode,
		"error_description": errDesc,
	})
}
