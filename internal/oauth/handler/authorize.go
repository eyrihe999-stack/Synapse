// authorize.go /oauth/authorize 流程(GET 渲染 consent / POST 处理用户选择)。
//
// GET 流程:
//   1. 解析 OAuth 参数 + svc.ValidateAuthRequest 校验合法性
//   2. 读 flow cookie → 未登录跳 /oauth/login 带 return_to
//   3. 列用户 org → 渲染 consent 页(含 hidden 字段保留所有 OAuth 参数)
//
// POST 流程(consent 提交):
//   1. 读 flow cookie → 未登录跳 /oauth/login(防 cookie 过期后盲目发码)
//   2. 重新 ValidateAuthRequest(defense-in-depth,hidden 字段可能被改过)
//   3. 校验 action == allow;否则 302 回 client 带 error=access_denied
//   4. 校验用户对所选 org 有成员资格(防 hidden org_id 篡改)
//   5. svc.IssueAuthCode → 302 回 redirect_uri 带 code + state
package handler

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/oauth"
	"github.com/eyrihe999-stack/Synapse/internal/oauth/service"
)

// Authorize GET /oauth/authorize。
func (h *Handler) Authorize(c *gin.Context) {
	req := parseAuthorizeFromQuery(c)

	// 先校验 OAuth 参数。如果连参数都不合法,根本不该让用户登录 —— 浪费用户一次登录成本。
	clientInfo, err := h.svc.ValidateAuthRequest(req)
	if err != nil {
		h.renderError(c, "Invalid authorization request", err.Error())
		return
	}

	// 检查登录态
	userID, ok := h.flowCookie.Read(c)
	if !ok {
		h.redirectToLogin(c)
		return
	}

	orgs, err := h.org.ListUserOrgs(c.Request.Context(), userID)
	if err != nil {
		h.log.ErrorCtx(c.Request.Context(), "oauth: list user orgs failed", err, map[string]any{"user_id": userID})
		h.renderError(c, "Server error", "Failed to load your organizations")
		return
	}
	if len(orgs) == 0 {
		h.renderError(c, "No organizations", "You are not a member of any organization, so there's nothing to grant access to.")
		return
	}

	c.Header("Cache-Control", "no-store")
	_ = consentTpl.Execute(c.Writer, consentData{
		ClientName:          clientInfo.ClientName,
		Orgs:                orgs,
		Scope:               pickScope(req.Scope, clientInfo.Scope),
		ClientID:            req.ClientID,
		RedirectURI:         req.RedirectURI,
		ResponseType:        req.ResponseType,
		State:               req.State,
		CodeChallenge:       req.CodeChallenge,
		CodeChallengeMethod: req.CodeChallengeMethod,
	})
}

// AuthorizeSubmit POST /oauth/authorize(consent 表单提交)。
func (h *Handler) AuthorizeSubmit(c *gin.Context) {
	req := parseAuthorizeFromForm(c)
	action := c.PostForm("action")
	orgIDStr := c.PostForm("org_id")

	// 登录态必须还在 —— cookie 过期就不能发码(防伪造)。
	userID, ok := h.flowCookie.Read(c)
	if !ok {
		h.redirectToLogin(c)
		return
	}

	// defense-in-depth:hidden 字段可能被改,重新校验 client + redirect_uri + PKCE 合法性。
	if _, err := h.svc.ValidateAuthRequest(req); err != nil {
		// 这条路径上 redirect_uri 不可信(可能就是被改坏的),稳妥做法是渲染错误页而非 302。
		h.renderError(c, "Invalid authorization request", err.Error())
		return
	}

	// 用户点 Deny(或按钮值异常)→ 302 回 client 带 access_denied(RFC 6749 §4.1.2.1)。
	if action != "allow" {
		redirectWithError(c, req.RedirectURI, oauth.ErrCodeAccessDenied, "user denied consent", req.State)
		return
	}

	orgID, err := strconv.ParseUint(orgIDStr, 10, 64)
	if err != nil || orgID == 0 {
		h.renderError(c, "Invalid org selection", "Please go back and choose a valid organization.")
		return
	}
	member, err := h.org.IsMember(c.Request.Context(), userID, orgID)
	if err != nil {
		h.log.ErrorCtx(c.Request.Context(), "oauth: check member failed", err, nil)
		h.renderError(c, "Server error", "Failed to verify organization membership")
		return
	}
	if !member {
		// 用户篡改 hidden 字段企图授权到非成员 org。这里对 client 也返 access_denied。
		h.log.WarnCtx(c.Request.Context(), "oauth: user not member of selected org", map[string]any{
			"user_id": userID, "org_id": orgID,
		})
		redirectWithError(c, req.RedirectURI, oauth.ErrCodeAccessDenied, "not a member of selected organization", req.State)
		return
	}

	code, err := h.svc.IssueAuthCode(service.IssueAuthCodeReq{
		ClientID:            req.ClientID,
		UserID:              userID,
		OrgID:               orgID,
		Scope:               req.Scope,
		RedirectURI:         req.RedirectURI,
		CodeChallenge:       req.CodeChallenge,
		CodeChallengeMethod: req.CodeChallengeMethod,
	})
	if err != nil {
		redirectWithError(c, req.RedirectURI, mapErrorCode(err), err.Error(), req.State)
		return
	}

	// 流程结束,清 cookie —— OAuth tokens 从此接管身份,浏览器 session 留着没用。
	h.flowCookie.Clear(c)

	redirectWithCode(c, req.RedirectURI, code, req.State)
}

// ─── helpers ────────────────────────────────────────────────────────────────

func parseAuthorizeFromQuery(c *gin.Context) service.AuthorizeRequest {
	return service.AuthorizeRequest{
		ClientID:            c.Query("client_id"),
		RedirectURI:         c.Query("redirect_uri"),
		ResponseType:        c.Query("response_type"),
		Scope:               c.Query("scope"),
		State:               c.Query("state"),
		CodeChallenge:       c.Query("code_challenge"),
		CodeChallengeMethod: c.Query("code_challenge_method"),
	}
}

func parseAuthorizeFromForm(c *gin.Context) service.AuthorizeRequest {
	return service.AuthorizeRequest{
		ClientID:            c.PostForm("client_id"),
		RedirectURI:         c.PostForm("redirect_uri"),
		ResponseType:        c.PostForm("response_type"),
		Scope:               c.PostForm("scope"),
		State:               c.PostForm("state"),
		CodeChallenge:       c.PostForm("code_challenge"),
		CodeChallengeMethod: c.PostForm("code_challenge_method"),
	}
}

// pickScope client 未声明 scope 白名单时,用户请求什么就展示什么;否则取请求 scope(已在
// ValidateAuthRequest 里验证过是 allowed 的子集)。
func pickScope(requested, allowed string) string {
	if requested != "" {
		return requested
	}
	return allowed
}

// redirectToLogin 未登录时跳 /oauth/login,原 URL encode 进 return_to。
// 登录完跳回,在 /authorize 再走一次校验 + 渲染 consent。
func (h *Handler) redirectToLogin(c *gin.Context) {
	returnTo := c.Request.URL.RequestURI() // 保留 path + query
	loginURL := "/oauth/login?return_to=" + url.QueryEscape(returnTo)
	c.Redirect(http.StatusFound, loginURL)
}

// renderError 通用错误页。详情展示给用户,不 leak 内部 err 细节。
func (h *Handler) renderError(c *gin.Context, title, detail string) {
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusBadRequest)
	_ = errorTpl.Execute(c.Writer, errorData{Title: title, Detail: detail})
}

// redirectWithCode 成功发码,302 回 redirect_uri,code + state 放 query。
func redirectWithCode(c *gin.Context, redirectURI, code, state string) {
	u, err := url.Parse(redirectURI)
	if err != nil {
		// redirect_uri 走到这里已在 ValidateAuthRequest 校验过,正常不会 parse 失败。
		c.String(http.StatusInternalServerError, "internal error: bad redirect_uri")
		return
	}
	q := u.Query()
	q.Set("code", code)
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	c.Redirect(http.StatusFound, u.String())
}

// redirectWithError 302 回 redirect_uri,error + error_description + state 放 query。
func redirectWithError(c *gin.Context, redirectURI, errCode, errDesc, state string) {
	u, err := url.Parse(redirectURI)
	if err != nil {
		c.String(http.StatusBadRequest, "invalid redirect_uri")
		return
	}
	q := u.Query()
	q.Set("error", errCode)
	if errDesc != "" {
		q.Set("error_description", errDesc)
	}
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	c.Redirect(http.StatusFound, u.String())
}
