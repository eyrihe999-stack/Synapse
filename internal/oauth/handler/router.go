// router.go oauth 模块路由注册。
//
// 和其他模块不同点:.well-known 端点必须挂在根路径(RFC 8414 / 9728 规定),
// 不能放 /api/v2/ 下面 —— 所以 RegisterRoutes 要直接用 *gin.Engine。
//
// 当前注册:
//   - /.well-known/oauth-authorization-server
//   - /.well-known/oauth-protected-resource
//   - POST /oauth/register  (DCR, 无 auth)
//   - POST /oauth/token     (无 auth — auth 靠 code/refresh token 自身)
//   - POST /oauth/revoke    (无 auth — 同 token)
//
// 未注册(task 6):
//   - GET /oauth/authorize  (需 web session middleware,下一轮加)
//   - POST /oauth/authorize (consent submit)
package handler

import "github.com/gin-gonic/gin"

// RegisterRoutes 挂 oauth 全部端点。
//
// CORS 策略:
//   - .well-known 和 /oauth/{register,token,revoke}:跨源 SPA 需要(Inspector / Claude.ai),挂 CORSAllowAll
//   - /oauth/{login,authorize}:浏览器直接导航访问,不涉及跨源 XHR,不需要 CORS
func RegisterRoutes(r *gin.Engine, h *Handler) {
	cors := CORSAllowAll()

	// 发现端点 — 根路径(RFC 8414 / 9728 强制)。跨源 fetch,必须 CORS。
	r.GET("/.well-known/oauth-authorization-server", cors, h.AuthorizationServerMetadata)
	r.OPTIONS("/.well-known/oauth-authorization-server", cors)
	r.GET("/.well-known/oauth-protected-resource", cors, h.ProtectedResourceMetadata)
	r.OPTIONS("/.well-known/oauth-protected-resource", cors)
	// RFC 9728 §3.1 path-suffixed 构造:client 对资源 URL `https://host/api/v2/retrieval/mcp`
	// 会构造 metadata URL `https://host/.well-known/oauth-protected-resource/api/v2/retrieval/mcp`。
	// 我们当前只有一个受保护资源,返回同一份 metadata。*path 是 gin 的 catch-all。
	r.GET("/.well-known/oauth-protected-resource/*path", cors, h.ProtectedResourceMetadata)
	r.OPTIONS("/.well-known/oauth-protected-resource/*path", cors)

	// OAuth 跨源组:跨源 XHR 发起,需要 CORS。
	xorig := r.Group("/oauth", cors)
	xorig.POST("/register", h.Register)
	xorig.OPTIONS("/register")
	xorig.POST("/token", h.Token)
	xorig.OPTIONS("/token")
	xorig.POST("/revoke", h.Revoke)
	xorig.OPTIONS("/revoke")

	// OAuth 浏览器导航组:同源或 top-level 跳转,不需要 CORS。
	og := r.Group("/oauth")
	og.GET("/login", h.Login)
	og.POST("/login", h.LoginSubmit)
	og.GET("/authorize", h.Authorize)
	og.POST("/authorize", h.AuthorizeSubmit)
}
