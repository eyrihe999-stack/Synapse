// oauth_login.go M1.6 第三方登录 handler(当前仅 Google)。
//
// 三个端点(路由在 router.go):
//
//	GET  /auth/oauth/google/start      → 302 到 Google authorize URL,签 state cookie
//	GET  /auth/oauth/google/callback   → 读 state cookie + code,完 OIDC + 业务合并 →
//	                                     302 到 "{fe_base}/auth/oauth/callback?exchange={code}"
//	POST /auth/oauth/exchange          → 前端用 exchange code 换 AuthResponse(一次性)
//
// 一次正常流程涉及 3 次 HTTP 往返:用户浏览器 → Google → Synapse callback → 前端 → Synapse exchange。
package handler

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/eyrihe999-stack/Synapse/internal/user"
	"github.com/eyrihe999-stack/Synapse/internal/user/service"
	"github.com/eyrihe999-stack/Synapse/internal/common/oidcclient"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	"github.com/gin-gonic/gin"
)

// GoogleOAuthStart 启动 Google 登录流程。GET /api/v1/auth/oauth/google/start
//
// 可选 query:
//
//	device_id   前端 getDeviceId() 生成的 uuid;省略时 callback 用 "default"
//	device_name 展示在"已登录设备"列表;省略时为空
//
// 无 JWT 中间件,匿名可访问。生成 state/nonce 写 HttpOnly cookie,
// 302 到 Google authorize URL 带上 state。
func (h *Handler) GoogleOAuthStart(c *gin.Context) {
	if h.googleOIDC == nil {
		h.log.WarnCtx(c.Request.Context(), "google oauth 未启用", nil)
		response.BadRequest(c, "Google login not enabled", "")
		return
	}

	payload, err := oidcclient.NewStatePayload(c.Query("device_id"), c.Query("device_name"))
	if err != nil {
		h.log.ErrorCtx(c.Request.Context(), "生成 oauth state 失败", err, nil)
		response.InternalServerError(c, "Internal server error", "")
		return
	}
	signed, err := h.googleOIDC.Sign(payload)
	if err != nil {
		h.log.ErrorCtx(c.Request.Context(), "签 oauth state 失败", err, nil)
		response.InternalServerError(c, "Internal server error", "")
		return
	}
	h.googleOIDC.SetStateCookie(c.Writer, signed, h.cookieSecure)
	// 302 到 Google
	c.Redirect(http.StatusFound, h.googleOIDC.AuthorizeURL(payload.State, payload.Nonce))
}

// GoogleOAuthCallback Google 回调。GET /api/v1/auth/oauth/google/callback?code=...&state=...
//
// 失败分支统一 302 到 "{fe_base}/auth/oauth/callback?error={code}",前端自己渲染错误 UI,
// 这样即使 Synapse 本体出错用户也能回到熟悉的页面而不是看 JSON。
func (h *Handler) GoogleOAuthCallback(c *gin.Context) {
	ctx := c.Request.Context()
	if h.googleOIDC == nil {
		h.redirectOAuthError(c, "provider_disabled")
		return
	}
	// 无论成功失败,清 state cookie —— 一次性
	defer oidcclient.ClearStateCookie(c.Writer, h.cookieSecure)

	// 1. 读 cookie + 校验 URL state
	cookie, err := c.Cookie(oidcclient.StateCookieName)
	if err != nil || cookie == "" {
		h.log.WarnCtx(ctx, "oauth callback 无 state cookie", nil)
		h.redirectOAuthError(c, "state_invalid")
		return
	}
	payload, err := h.googleOIDC.Verify(cookie)
	if err != nil {
		h.log.WarnCtx(ctx, "oauth state 校验失败", map[string]interface{}{"err": err.Error()})
		h.redirectOAuthError(c, "state_invalid")
		return
	}
	if payload.State != c.Query("state") {
		h.log.WarnCtx(ctx, "oauth URL state 与 cookie 不匹配", nil)
		h.redirectOAuthError(c, "state_invalid")
		return
	}

	// 2. 检查 IdP 返的 error(用户取消授权 / consent 失败等)
	if errParam := c.Query("error"); errParam != "" {
		h.log.InfoCtx(ctx, "oauth provider 返回 error", map[string]interface{}{"error": errParam})
		h.redirectOAuthError(c, errParam)
		return
	}

	code := c.Query("code")
	if code == "" {
		h.redirectOAuthError(c, "missing_code")
		return
	}

	// 3. 凭 code 换 id_token 并校验
	claims, err := h.googleOIDC.Exchange(ctx, code, payload.Nonce)
	if err != nil {
		h.log.ErrorCtx(ctx, "oauth token exchange 失败", err, nil)
		h.redirectOAuthError(c, "exchange_failed")
		return
	}

	// 4. 交给 service 做 identity / user 合并 + 生成 AuthResponse
	auth, err := h.svc.LoginWithOAuth(ctx, service.OAuthLoginRequest{
		Provider:      service.OAuthProviderGoogle,
		Subject:       claims.Sub,
		Email:         claims.Email,
		EmailVerified: claims.EmailVerified,
		DisplayName:   claims.Name,
		AvatarURL:     claims.Picture,
		DeviceID:      payload.DeviceID,
		DeviceName:    payload.DeviceName,
		LoginIP:       c.ClientIP(),
		UserAgent:     c.Request.UserAgent(),
	})
	if err != nil {
		h.log.ErrorCtx(ctx, "oauth service 登录失败", err, nil)
		// 细分 email unverified -> 给前端更友好的提示
		if isOAuthEmailUnverified(err) {
			h.redirectOAuthError(c, "email_unverified")
			return
		}
		h.redirectOAuthError(c, "login_failed")
		return
	}

	// 5. 存 exchange code,前端拿到 code 后再调 /exchange 兑换 tokens
	exchangeCode, err := h.svc.StoreOAuthExchange(ctx, auth)
	if err != nil {
		h.log.ErrorCtx(ctx, "oauth 存 exchange 失败", err, nil)
		h.redirectOAuthError(c, "internal")
		return
	}

	h.redirectOAuthSuccess(c, exchangeCode)
}

// OAuthExchange 前端用 exchange code 换 AuthResponse。POST /api/v1/auth/oauth/exchange
//
// 唯一的写状态点:兑换后 code 立即作废。前端应在 OAuth 回调页上 mount 时调一次,成功后 setAuth。
func (h *Handler) OAuthExchange(c *gin.Context) {
	var req struct {
		Code string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request", err.Error())
		return
	}
	auth, err := h.svc.PickupOAuthExchange(c.Request.Context(), req.Code)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	response.Success(c, "Login successful", auth)
}

// redirectOAuthSuccess 成功 302 到前端 /auth/oauth/callback?exchange=xxx
func (h *Handler) redirectOAuthSuccess(c *gin.Context, exchangeCode string) {
	u := h.googleFERedir + "/auth/oauth/callback?exchange=" + url.QueryEscape(exchangeCode)
	c.Redirect(http.StatusFound, u)
}

// redirectOAuthError 失败 302 到前端 /auth/oauth/callback?error=reason
func (h *Handler) redirectOAuthError(c *gin.Context, reason string) {
	u := h.googleFERedir + "/auth/oauth/callback?error=" + url.QueryEscape(reason)
	c.Redirect(http.StatusFound, u)
}

// isOAuthEmailUnverified 判断 service 返的 error 是否是 ErrOAuthEmailUnverified。
func isOAuthEmailUnverified(err error) bool {
	return errors.Is(err, user.ErrOAuthEmailUnverified)
}
