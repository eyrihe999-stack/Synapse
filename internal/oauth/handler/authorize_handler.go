package handler

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/oauth"
)

// Authorize GET /oauth/authorize
//
// OAuth 2.1 authorization endpoint。浏览器重定向进来:
//   ?response_type=code&client_id=...&redirect_uri=...&state=...
//   &code_challenge=...&code_challenge_method=S256&scope=mcp:invoke
//
// 流程:
//  1. 校验参数(client/redirect_uri/response_type/PKCE);**致命错**(client_id 不对、
//     redirect_uri 不匹配)直接 HTML 报错,不重定向到可疑 URL;其他错按 RFC 6749 §4.1.2.1
//     重定向到 redirect_uri 带 error 参数
//  2. 读 Cookie synapse_oauth_session -> userID
//     未登录:渲染 HTML 登录表单(POST /oauth/login),表单带 return=<原 URL>
//     已登录:渲染 HTML consent 表单(POST /oauth/authorize/consent)
func (h *Handler) Authorize(c *gin.Context) {
	q := c.Request.URL.Query()
	params := authorizeParams{
		ResponseType:        q.Get("response_type"),
		ClientID:            q.Get("client_id"),
		RedirectURI:         q.Get("redirect_uri"),
		State:               q.Get("state"),
		Scope:               q.Get("scope"),
		CodeChallenge:       q.Get("code_challenge"),
		CodeChallengeMethod: q.Get("code_challenge_method"),
	}

	// 1. 找 client + 校验 redirect_uri —— 这两项失败不重定向(防 open redirect)
	client, err := h.svc.Client.FindActiveByClientID(c.Request.Context(), params.ClientID)
	if err != nil || client == nil {
		writeAuthorizeFatalError(c, "unknown or disabled client_id")
		return
	}
	var allowedRedirects []string
	if err := json.Unmarshal([]byte(client.RedirectURIsJSON), &allowedRedirects); err != nil {
		writeAuthorizeFatalError(c, "client config corrupt")
		return
	}
	if !contains(allowedRedirects, params.RedirectURI) {
		writeAuthorizeFatalError(c, "redirect_uri does not match any registered URI")
		return
	}

	// 2. 其他参数错 → 按 RFC 6749 重定向带 error
	if params.ResponseType != oauth.ResponseTypeCode {
		redirectWithError(c, params.RedirectURI, params.State, "unsupported_response_type", "")
		return
	}
	if params.Scope != "" && params.Scope != oauth.ScopeMCPInvoke {
		redirectWithError(c, params.RedirectURI, params.State, "invalid_scope", "only mcp:invoke is supported")
		return
	}
	if params.CodeChallenge == "" {
		redirectWithError(c, params.RedirectURI, params.State, "invalid_request", "code_challenge required (PKCE)")
		return
	}
	if params.CodeChallengeMethod == "" {
		params.CodeChallengeMethod = oauth.PKCEMethodS256
	}
	if params.CodeChallengeMethod != oauth.PKCEMethodS256 {
		redirectWithError(c, params.RedirectURI, params.State, "invalid_request", "code_challenge_method must be S256")
		return
	}

	// 3. session 状态
	sid, _ := c.Cookie(sessionCookieName)
	var userID uint64
	if sid != "" && h.sessionStore != nil {
		userID, _ = h.sessionStore.Resolve(c.Request.Context(), sid)
	}

	if userID == 0 {
		renderLoginPage(c, params, "")
		return
	}

	// 4. 已登录 → consent 页
	renderConsentPage(c, params, client.ClientName, userID)
}

// authorizeParams 从 GET /oauth/authorize 的 query 里拎出来的参数。
type authorizeParams struct {
	ResponseType        string
	ClientID            string
	RedirectURI         string
	State               string
	Scope               string
	CodeChallenge       string
	CodeChallengeMethod string
}

// encodeToForm 把 authorize params 写成 hidden input,方便 login / consent 表单 POST 时带回来。
func (p authorizeParams) encodeToForm() string {
	return hiddenInput("response_type", p.ResponseType) +
		hiddenInput("client_id", p.ClientID) +
		hiddenInput("redirect_uri", p.RedirectURI) +
		hiddenInput("state", p.State) +
		hiddenInput("scope", p.Scope) +
		hiddenInput("code_challenge", p.CodeChallenge) +
		hiddenInput("code_challenge_method", p.CodeChallengeMethod)
}

// ── Cookie 约定 ─────────────────────────────────────────────────────

const (
	sessionCookieName = "synapse_oauth_session"
	sessionCookiePath = "/" // OAuth 内部跨 /oauth/authorize、/oauth/login、/oauth/authorize/consent
)

func (h *Handler) setSessionCookie(c *gin.Context, sid string, maxAgeSeconds int) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(sessionCookieName, sid, maxAgeSeconds, sessionCookiePath, "", h.cookieSecure, true)
}

func (h *Handler) clearSessionCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(sessionCookieName, "", -1, sessionCookiePath, "", h.cookieSecure, true)
}

// ── 错误响应 ──────────────────────────────────────────────────────────

// writeAuthorizeFatalError 致命错(client/redirect_uri 不对)直接给 HTML 错误页。
// 不重定向 —— 防止 open redirect 滥用。
func writeAuthorizeFatalError(c *gin.Context, msg string) {
	const page = `<!doctype html>
<html><head><meta charset="utf-8"><title>Authorization error</title></head>
<body style="font-family:sans-serif;max-width:480px;margin:48px auto">
<h2>Authorization error</h2>
<p>%s</p>
</body></html>`
	c.Data(http.StatusBadRequest, "text/html; charset=utf-8",
		[]byte(fmt.Sprintf(page, html.EscapeString(msg))))
}

// redirectWithError RFC 6749 §4.1.2.1 错误重定向。
func redirectWithError(c *gin.Context, redirectURI, state, code, description string) {
	u, err := url.Parse(redirectURI)
	if err != nil {
		writeAuthorizeFatalError(c, "invalid redirect_uri")
		return
	}
	q := u.Query()
	q.Set("error", code)
	if description != "" {
		q.Set("error_description", description)
	}
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	c.Redirect(http.StatusFound, u.String())
}

// ── 页面渲染 ──────────────────────────────────────────────────────────

func renderLoginPage(c *gin.Context, p authorizeParams, errMsg string) {
	var errBlock string
	if errMsg != "" {
		errBlock = fmt.Sprintf(`<div style="color:#b00;margin-bottom:16px">%s</div>`, html.EscapeString(errMsg))
	}
	body := fmt.Sprintf(`<!doctype html>
<html><head><meta charset="utf-8"><title>Sign in to Synapse</title></head>
<body style="font-family:sans-serif;max-width:400px;margin:64px auto">
<h2>Sign in to Synapse</h2>
<p>Authorize <strong>%s</strong> to access your Synapse account.</p>
%s
<form method="POST" action="/oauth/login">
%s
<div style="margin-bottom:12px">
  <label>Email<br><input name="email" type="email" required style="width:100%%;padding:8px"></label>
</div>
<div style="margin-bottom:12px">
  <label>Password<br><input name="password" type="password" required style="width:100%%;padding:8px"></label>
</div>
<button type="submit" style="padding:8px 16px">Sign in & continue</button>
</form>
</body></html>`, html.EscapeString(p.ClientID), errBlock, p.encodeToForm())
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(body))
}

func renderConsentPage(c *gin.Context, p authorizeParams, clientName string, userID uint64) {
	scope := p.Scope
	if scope == "" {
		scope = oauth.ScopeMCPInvoke
	}
	body := fmt.Sprintf(`<!doctype html>
<html><head><meta charset="utf-8"><title>Authorize access</title></head>
<body style="font-family:sans-serif;max-width:480px;margin:64px auto">
<h2>Authorize access</h2>
<p><strong>%s</strong> is requesting access to your Synapse account.</p>
<p>This will create a dedicated personal agent on your behalf. Granted scope: <code>%s</code>.</p>
<p style="color:#666;font-size:13px">Signed in as user #%d.
<a href="/oauth/logout?return=%s">Sign out</a></p>
<form method="POST" action="/oauth/authorize/consent" style="display:inline">
%s
<input type="hidden" name="approved" value="true">
<button type="submit" style="padding:8px 16px;background:#0a7;color:#fff;border:0">Approve</button>
</form>
<form method="POST" action="/oauth/authorize/consent" style="display:inline;margin-left:12px">
%s
<input type="hidden" name="approved" value="false">
<button type="submit" style="padding:8px 16px">Deny</button>
</form>
</body></html>`,
		html.EscapeString(clientName),
		html.EscapeString(scope),
		userID,
		url.QueryEscape(c.Request.URL.String()),
		p.encodeToForm(),
		p.encodeToForm(),
	)
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(body))
}

// ── helpers ─────────────────────────────────────────────────────────

func hiddenInput(name, value string) string {
	return fmt.Sprintf(`<input type="hidden" name="%s" value="%s">`,
		html.EscapeString(name), html.EscapeString(value))
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// 防止 strings 包意外被删除后编译不过。
var _ = strings.TrimSpace
