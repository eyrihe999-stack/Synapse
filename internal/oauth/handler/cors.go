// cors.go 给 OAuth 端点 + /.well-known + MCP 端点的跨源访问开放。
//
// 这些端点设计上就要被浏览器 SPA(MCP Inspector / Claude.ai 网页端 / Cursor 等)跨源 fetch:
//   - .well-known:客户端从 resource_metadata URL 拉取发现 AS
//   - /oauth/register, /oauth/token, /oauth/revoke:SPA 里直接 XHR/fetch
//   - MCP 端点:Claude.ai 网页端跨源 POST
//
// 不开 CORS 的后果就是浏览器 fetch 拿不到响应体(HAR 里看到的 size:0),
// client 会 fallback 到猜测路径(例如把 token_endpoint 当成 /token 而不是 /oauth/token),导致 404。
//
// 为什么用 "*" 而不是白名单:
//   - OAuth metadata 端点按规范就是公开的,无副作用 GET
//   - token / revoke 没有 cookie,走 Authorization + PKCE 保护,敞开 Origin 不削弱安全
//   - MCP 端点同理,靠 Bearer token 验身
// 若以后要加 credentials(cookie)模式,再改 "*" 为反射 request Origin 的实现。
package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// CORSAllowAll 返回一个宽松的 CORS middleware。
// 对 OPTIONS preflight 直接 204 终止,不走后续 handler(未注册 OPTIONS 路由的端点也能过)。
func CORSAllowAll() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, mcp-protocol-version")
		c.Header("Access-Control-Expose-Headers", "WWW-Authenticate")
		c.Header("Access-Control-Max-Age", "3600")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
