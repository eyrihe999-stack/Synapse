// router.go OAuth + PAT 管理路由注册。
//
// 端点分组:
//
//	/.well-known/                            匿名(RFC 8414 + oauth-protected-resource)
//	  GET /oauth-authorization-server        AS metadata
//	  GET /oauth-protected-resource          指向 AS 的 resource metadata
//
//	/oauth/                                  匿名(标准 OAuth flow 本身不需要预鉴权)
//	  GET  /authorize                        浏览器重定向;返 HTML 登录 / consent
//	  POST /login                            表单;email+password → OAuth session cookie
//	  GET  /logout                           清 session cookie
//	  POST /authorize/consent                表单;同意 → 302 到 redirect_uri?code=
//	  POST /token                            OAuth 2.1 token endpoint
//	  POST /revoke                           OAuth 2.1 revoke
//	  POST /register                         RFC 7591 DCR(匿名 + per-IP 限速)
//
//	/api/v2/oauth/clients                    要 JWT(user session)—— 当前 user 自助管理
//	  POST   /                               建自己的 OAuth client(返明文 secret 一次)
//	  GET    /                               列自己建的 client
//	  POST   /:id/disable                    禁用 + 级联吊销 token
package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/common/jwt"
	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/user"
)

// RegisterRoutes 把 OAuth 模块的所有 HTTP endpoint 挂到 gin.Engine。
func RegisterRoutes(
	router *gin.Engine,
	h *Handler,
	jwtManager *jwt.JWTManager,
	sessionStore user.SessionStore,
) {
	// .well-known —— 匿名
	wk := router.Group("/.well-known")
	{
		wk.GET("/oauth-authorization-server", h.WellKnownAuthorizationServer)
		wk.GET("/oauth-protected-resource", h.WellKnownProtectedResource)
		// RFC 9728 允许 "per-resource" metadata:
		// /.well-known/oauth-protected-resource/<resource-path>
		// Claude Desktop 会按此规则探测 <issuer>/.well-known/oauth-protected-resource/api/v2/mcp
		// 同一个 handler 返同一份 metadata;子路径 = hint,body 不变
		wk.GET("/oauth-protected-resource/*path", h.WellKnownProtectedResource)
	}

	// /oauth/* —— 标准 OAuth flow,匿名(单个端点有自己的认证方式:client creds / user session cookie / 等)
	o := router.Group("/oauth")
	{
		o.GET("/authorize", h.Authorize)
		o.POST("/login", h.Login)
		o.GET("/logout", h.Logout)
		o.POST("/authorize/consent", h.Consent)
		o.POST("/token", h.Token)
		o.POST("/revoke", h.Revoke)
		o.POST("/register", h.DynamicClientRegister)
	}

	// /api/v2/oauth/clients —— 用户自助管理,走 web JWT
	admin := router.Group("/api/v2/oauth/clients")
	admin.Use(middleware.JWTAuthWithSession(jwtManager, sessionStore))
	{
		admin.POST("", h.CreateOAuthClient)
		admin.GET("", h.ListOAuthClients)
		admin.POST("/:id/disable", h.DisableOAuthClient)
	}

	// /api/v2/users/me/pats —— 用户 PAT 自助管理
	pats := router.Group("/api/v2/users/me/pats")
	pats.Use(middleware.JWTAuthWithSession(jwtManager, sessionStore))
	{
		pats.POST("", h.CreatePAT)
		pats.GET("", h.ListPATs)
		pats.DELETE("/:id", h.RevokePAT)
	}
}
