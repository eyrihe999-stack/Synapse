package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/oauth/service"
)

// Consent POST /oauth/authorize/consent(form)
//
// 流程:
//  1. 从 Cookie 拿 session → userID;未登录 → 重建 login 页
//  2. approved=false → 按 RFC 6749 重定向 error=access_denied
//  3. approved=true:
//     a. 重新验 client_id + redirect_uri(防表单参数被篡改)
//     b. 自动建 agent(AgentBootstrapper)
//     c. 生成 authorization_code(写 DB)
//     d. 302 到 redirect_uri?code=...&state=...
//  4. consent 完成后立刻 delete session(降低暴露窗口)
func (h *Handler) Consent(c *gin.Context) {
	p := readAuthorizeParamsFromForm(c)
	approved := c.PostForm("approved") == "true"

	// 1. session
	sid, _ := c.Cookie(sessionCookieName)
	if sid == "" || h.sessionStore == nil {
		renderLoginPage(c, p, "Session expired, please sign in again")
		return
	}
	userID, _ := h.sessionStore.Resolve(c.Request.Context(), sid)
	if userID == 0 {
		renderLoginPage(c, p, "Session expired, please sign in again")
		return
	}

	// 2. client + redirect_uri 重验(防表单篡改)
	client, err := h.svc.Client.FindActiveByClientID(c.Request.Context(), p.ClientID)
	if err != nil || client == nil {
		writeAuthorizeFatalError(c, "unknown or disabled client_id")
		return
	}
	var allowed []string
	_ = json.Unmarshal([]byte(client.RedirectURIsJSON), &allowed)
	if !contains(allowed, p.RedirectURI) {
		writeAuthorizeFatalError(c, "redirect_uri does not match")
		return
	}

	// 3. 拒绝 → access_denied
	if !approved {
		_ = h.sessionStore.Delete(c.Request.Context(), sid)
		h.clearSessionCookie(c)
		redirectWithError(c, p.RedirectURI, p.State, "access_denied", "user denied the request")
		return
	}

	// 4. 同意 → 建 agent
	if h.agentBootstrapper == nil {
		writeAuthorizeFatalError(c, "agent bootstrapper not configured")
		return
	}
	displayName := client.ClientName // 用 client 名当新 agent 的 display_name
	_, agentPrincipalID, err := h.agentBootstrapper.CreateUserAgent(c.Request.Context(), userID, displayName)
	if err != nil {
		h.log.ErrorCtx(c.Request.Context(), "oauth: create user agent failed", err, map[string]any{
			"user_id": userID, "client_id": p.ClientID,
		})
		writeAuthorizeFatalError(c, "failed to create agent identity")
		return
	}

	// 5. 生成 authorization_code
	scope := p.Scope
	if scope == "" {
		scope = "mcp:invoke"
	}
	code, err := h.svc.Authorization.IssueCode(c.Request.Context(), service.IssueCodeInput{
		ClientID:            p.ClientID,
		UserID:              userID,
		AgentID:             agentPrincipalID,
		RedirectURI:         p.RedirectURI,
		Scope:               scope,
		PKCEChallenge:       p.CodeChallenge,
		PKCEChallengeMethod: p.CodeChallengeMethod,
		CodeTTL:             h.authorizationCodeTTL,
	})
	if err != nil {
		h.log.ErrorCtx(c.Request.Context(), "oauth: issue code failed", err, map[string]any{
			"user_id": userID, "client_id": p.ClientID,
		})
		writeAuthorizeFatalError(c, "failed to issue authorization code")
		return
	}

	// 6. 清 session + Cookie(consent 后不再需要)
	_ = h.sessionStore.Delete(c.Request.Context(), sid)
	h.clearSessionCookie(c)

	// 7. 重定向到 redirect_uri?code=xxx&state=xxx
	target := buildRedirectWithCode(p.RedirectURI, p.State, code)
	c.Redirect(http.StatusFound, target)
}

// buildRedirectWithCode 拼 redirect_uri?code=xxx&state=xxx。
// redirect_uri 可能已有 query,用 url.Parse 保留。
func buildRedirectWithCode(redirectURI, state, code string) string {
	u, err := url.Parse(redirectURI)
	if err != nil {
		// 异常 fallback(不应发生,authorize 已校验过)
		return fmt.Sprintf("%s?code=%s&state=%s", redirectURI, url.QueryEscape(code), url.QueryEscape(state))
	}
	q := u.Query()
	q.Set("code", code)
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	return u.String()
}
