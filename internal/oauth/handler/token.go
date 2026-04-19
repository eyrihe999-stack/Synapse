// token.go /oauth/token  两个 grant type(authorization_code / refresh_token)的入口。
//
// Body:application/x-www-form-urlencoded(RFC 6749 §4.1.3 / §6 强制要求)。
// 成功响应:JSON + Cache-Control: no-store(防 token 被缓存中间件留下)。
// 错误响应:OAuth error JSON(RFC 6749 §5.2)。
package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/oauth/service"
)

// Token POST /oauth/token。
func (h *Handler) Token(c *gin.Context) {
	grantType := c.PostForm("grant_type")
	switch grantType {
	case "authorization_code":
		h.tokenAuthCode(c)
	case "refresh_token":
		h.tokenRefresh(c)
	case "":
		writeOAuthError(c, service.ErrInvalidRequest)
	default:
		writeOAuthError(c, service.ErrUnsupportedGrantType)
	}
}

func (h *Handler) tokenAuthCode(c *gin.Context) {
	req := service.ExchangeAuthCodeReq{
		Code:         c.PostForm("code"),
		ClientID:     c.PostForm("client_id"),
		RedirectURI:  c.PostForm("redirect_uri"),
		CodeVerifier: c.PostForm("code_verifier"),
	}
	resp, err := h.svc.ExchangeAuthCode(req)
	if err != nil {
		h.log.WarnCtx(c.Request.Context(), "oauth: exchange auth code failed", map[string]any{
			"client_id": req.ClientID, "err": err.Error(),
		})
		writeOAuthError(c, err)
		return
	}
	writeTokenResponse(c, resp)
}

func (h *Handler) tokenRefresh(c *gin.Context) {
	req := service.RefreshReq{
		RefreshToken: c.PostForm("refresh_token"),
		ClientID:     c.PostForm("client_id"),
		Scope:        c.PostForm("scope"),
	}
	resp, err := h.svc.RefreshAccessToken(req)
	if err != nil {
		// refresh 失败可能是 reuse detection,单独记一条便于告警。
		h.log.WarnCtx(c.Request.Context(), "oauth: refresh token failed", map[string]any{
			"client_id": req.ClientID, "err": err.Error(),
		})
		writeOAuthError(c, err)
		return
	}
	writeTokenResponse(c, resp)
}

// writeTokenResponse 统一 token 响应。必须带 Cache-Control: no-store(RFC 6749 §5.1)。
func writeTokenResponse(c *gin.Context, resp *service.TokenResponse) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.JSON(http.StatusOK, resp)
}
