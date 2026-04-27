package handler

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
)

// Login POST /oauth/login(form 表单)
//
// 入参(form):email / password + authorize 的 7 个 hidden field(透传)。
// 成功:Create OAuth session → Set-Cookie → 302 重建 /oauth/authorize 的 URL
//        (使用 hidden field 里的值,客户端看不到 JWT;session 只是 sid)。
// 失败:重渲染登录页 + error 消息(保留 authorize params)。
func (h *Handler) Login(c *gin.Context) {
	email := c.PostForm("email")
	password := c.PostForm("password")
	p := readAuthorizeParamsFromForm(c)

	if email == "" || password == "" {
		renderLoginPage(c, p, "Email and password required")
		return
	}
	if h.userAuthenticator == nil || h.sessionStore == nil {
		writeAuthorizeFatalError(c, "oauth login not configured")
		return
	}

	userID, err := h.userAuthenticator.AuthenticateByPassword(c.Request.Context(), email, password, clientIP(c), c.GetHeader("User-Agent"))
	if err != nil || userID == 0 {
		// 统一错误文案,防枚举
		renderLoginPage(c, p, "Invalid email or password")
		h.log.WarnCtx(c.Request.Context(), "oauth: login failed", map[string]any{
			"email": email, "err": errStr(err),
		})
		return
	}

	sid, err := h.sessionStore.Create(c.Request.Context(), userID)
	if err != nil {
		writeAuthorizeFatalError(c, "session creation failed")
		return
	}
	h.setSessionCookie(c, sid, 10*60) // 10min 够完成 consent

	// 重建 /oauth/authorize 的 URL 302 过去
	c.Redirect(http.StatusFound, buildAuthorizeURL(p))
}

// Logout GET /oauth/logout?return=xxx
//
// 清 session + 清 Cookie。return 参数若为空或不合法,回到根路径。
// 用户在 consent 页点"Sign out"走这条。
func (h *Handler) Logout(c *gin.Context) {
	sid, _ := c.Cookie(sessionCookieName)
	if sid != "" && h.sessionStore != nil {
		_ = h.sessionStore.Delete(c.Request.Context(), sid)
	}
	h.clearSessionCookie(c)
	ret := c.Query("return")
	if ret == "" {
		ret = "/"
	}
	c.Redirect(http.StatusFound, ret)
}

// ── helpers ──────────────────────────────────────────────────────────

// readAuthorizeParamsFromForm 从 POST form 里拎回 authorize 的 7 个 hidden。
func readAuthorizeParamsFromForm(c *gin.Context) authorizeParams {
	return authorizeParams{
		ResponseType:        c.PostForm("response_type"),
		ClientID:            c.PostForm("client_id"),
		RedirectURI:         c.PostForm("redirect_uri"),
		State:               c.PostForm("state"),
		Scope:               c.PostForm("scope"),
		CodeChallenge:       c.PostForm("code_challenge"),
		CodeChallengeMethod: c.PostForm("code_challenge_method"),
	}
}

// buildAuthorizeURL 从 params 组回 /oauth/authorize?... URL。
func buildAuthorizeURL(p authorizeParams) string {
	q := url.Values{}
	q.Set("response_type", p.ResponseType)
	q.Set("client_id", p.ClientID)
	q.Set("redirect_uri", p.RedirectURI)
	if p.State != "" {
		q.Set("state", p.State)
	}
	if p.Scope != "" {
		q.Set("scope", p.Scope)
	}
	q.Set("code_challenge", p.CodeChallenge)
	q.Set("code_challenge_method", p.CodeChallengeMethod)
	return fmt.Sprintf("/oauth/authorize?%s", q.Encode())
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// 预留供将来做 CSRF 的 no-op helper(form hidden token),暂无实现。
// 纯 SSR + SameSite=Lax cookie 对 OAuth consent 的 CSRF 威胁模型有限,MVP 不做。
var _ = errors.New
