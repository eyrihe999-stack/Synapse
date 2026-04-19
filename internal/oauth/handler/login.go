// login.go /oauth/login 登录页 + 提交。
//
// 是 oauth 模块的"自带"登录 UI —— 和 web app 的 JS 登录 API 分开。目的是让 OAuth 流程
// 端到端自包含:Claude Desktop 打开浏览器 → 登录 → consent → 回客户端,整条路径不依赖前端。
//
// 认证本身不自己做,通过 LoginAdapter 调用现有 UserService(main.go 里包 adapter)。
package handler

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

// Login GET /oauth/login?return_to=...
func (h *Handler) Login(c *gin.Context) {
	returnTo := c.Query("return_to")
	h.renderLogin(c, returnTo, "")
}

// LoginSubmit POST /oauth/login。
func (h *Handler) LoginSubmit(c *gin.Context) {
	email := strings.TrimSpace(c.PostForm("email"))
	password := c.PostForm("password")
	returnTo := c.PostForm("return_to")

	if email == "" || password == "" {
		h.renderLogin(c, returnTo, "Email and password required")
		return
	}

	userID, err := h.login.VerifyCredentials(c.Request.Context(), email, password)
	if err != nil {
		// 统一显示"invalid credentials",不区分"用户不存在" vs "密码错" —— 防账号枚举。
		h.log.WarnCtx(c.Request.Context(), "oauth: login failed", map[string]any{"email": email, "err": err.Error()})
		h.renderLogin(c, returnTo, "Invalid email or password")
		return
	}

	// 登录成功 → 签 flow cookie → 302 去 return_to。
	h.flowCookie.Issue(c, userID)

	// return_to 安全:只允许本站 path(同源),不允许跳外站 —— 防 open redirect。
	target := sanitizeReturnTo(returnTo)
	c.Redirect(http.StatusFound, target)
}

func (h *Handler) renderLogin(c *gin.Context, returnTo, errMsg string) {
	c.Header("Cache-Control", "no-store")
	if errMsg != "" {
		c.Status(http.StatusUnauthorized)
	}
	_ = loginTpl.Execute(c.Writer, loginData{
		ReturnTo: returnTo,
		Error:    errMsg,
	})
}

// sanitizeReturnTo 只接受本站相对 URL,防 phisher 登录成功后跳外站。
// 规则:必须以 "/oauth/" 开头,parse 合法;否则 fallback 到 "/oauth/authorize" 根路径。
func sanitizeReturnTo(raw string) string {
	if raw == "" {
		return "/oauth/authorize"
	}
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" {
		// 绝对 URL(含 scheme)或带 host 一律拒
		return "/oauth/authorize"
	}
	if !strings.HasPrefix(u.Path, "/oauth/") {
		return "/oauth/authorize"
	}
	return raw
}
