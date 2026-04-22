// router.go user 模块路由注册。
package handler

import (
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/user"
	"github.com/eyrihe999-stack/Synapse/internal/common/jwt"
	"github.com/gin-gonic/gin"
)

// authIPRateLimitPerMinute 单 IP 每分钟允许的 auth 请求数(注册/登录/刷新合计)。
// 比 chat 的 60/min 严,因为这些是无鉴权入口,主要防爆破。
const authIPRateLimitPerMinute = 30

// RegisterRoutes 注册 user 模块所有路由。
//
// 路由分组:
//   - /api/v1/auth — 注册/登录/刷新(无需鉴权,加 IP 维度兜底限流)
//   - /api/v1/users — 个人资料与 session 管理(需 JWT + session 校验 + 绝对 TTL)
//
// absoluteSessionTTL 由 main.go 从 cfg.JWT.AbsoluteSessionTTL 传入;其他模块的 RegisterRoutes
// 不透传这个值,走 middleware 默认 30d 即可(统一的业务口径在这里),避免跨模块签名扩散。
func RegisterRoutes(r *gin.Engine, h *Handler, jwtManager *jwt.JWTManager, sessionStore user.SessionStore, absoluteSessionTTL time.Duration) {
	auth := r.Group("/api/v1/auth", middleware.IPRateLimit(authIPRateLimitPerMinute, time.Minute))
	{
		auth.POST("/register", h.Register)
		auth.POST("/login", h.Login)
		auth.POST("/refresh", h.RefreshToken)
		// 邮箱验证码发送。消费在 /register 和 /login 内部完成(请求体里带 code 字段),
		// 不暴露独立的 verify 接口以免前端误用吃掉码。
		auth.POST("/email/send-code", h.SendEmailCode)
		// 密码重置:request 发邮件(含 token),confirm 凭 token 改密后踢所有 session。
		// 两端都无鉴权,走上面的 IP 限流兜底防爆破;token 本身是 32B 随机串,猜不到。
		auth.POST("/password-reset/request", h.RequestPasswordReset)
		auth.POST("/password-reset/confirm", h.ConfirmPasswordReset)
		// M1.1 邮箱激活(点邮件里的激活链接落到前端,前端拿 token 调这个接口)。
		// 公开无鉴权,靠 token 不可猜 + 一次性消费自我保护。
		auth.POST("/email/verify", h.VerifyEmail)
		// M1.6 第三方登录(Google OIDC)。
		// start/callback 是浏览器跳转端点,exchange 是 SPA XHR 兑换 tokens。
		// IP 限流已足够兜底;state + nonce cookie 防 CSRF 和 id_token 重放。
		auth.GET("/oauth/google/start", h.GoogleOAuthStart)
		auth.GET("/oauth/google/callback", h.GoogleOAuthCallback)
		auth.POST("/oauth/exchange", h.OAuthExchange)
	}

	users := r.Group("/api/v1/users", middleware.JWTAuthWithSession(jwtManager, sessionStore, absoluteSessionTTL))
	{
		users.GET("/me", h.GetProfile)
		users.PATCH("/me", h.UpdateProfile)
		// M1.7 自助注销:pseudo 化 + 清 identity/session,不可恢复(GDPR purge 由另行定时任务推进)
		users.DELETE("/me", h.DeleteAccount)
		// 账号安全自助:改密 + 改邮箱。两者成功后都会 LogoutAll,前端需处理回登录页。
		users.POST("/me/password", h.ChangePassword)
		users.POST("/me/email", h.ChangeEmail)
		// M1.1 已登录用户重发激活邮件(主要给 OAuth email_verified=false 的账号补救)
		users.POST("/me/email/resend-verification", h.ResendEmailVerification)
		users.GET("/me/sessions", h.ListSessions)
		users.DELETE("/me/sessions/:device_id", h.KickSession)
		users.POST("/me/sessions/logout-all", h.LogoutAll)
	}
}
