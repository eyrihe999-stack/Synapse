package handler

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/oauth"
	"github.com/eyrihe999-stack/Synapse/internal/oauth/service"
)

// tokenSuccess RFC 6749 §5.1 token 响应。
type tokenSuccess struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"` // "Bearer"
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// tokenError RFC 6749 §5.2 token 错误响应。
type tokenError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
}

// Token POST /oauth/token
//
// grant_type=authorization_code:
//   - 入参(application/x-www-form-urlencoded):grant_type/code/redirect_uri
//     /client_id/client_secret?/code_verifier
//
// grant_type=refresh_token:
//   - refresh_token/client_id/client_secret?
func (h *Handler) Token(c *gin.Context) {
	grantType := c.PostForm("grant_type")

	clientID, clientSecret := extractClientCredentials(c)
	if clientID == "" {
		writeTokenError(c, http.StatusUnauthorized, "invalid_client", "missing client_id")
		return
	}

	switch grantType {
	case oauth.GrantTypeAuthorizationCode:
		code := c.PostForm("code")
		redirectURI := c.PostForm("redirect_uri")
		codeVerifier := c.PostForm("code_verifier")
		if code == "" || redirectURI == "" {
			writeTokenError(c, http.StatusBadRequest, "invalid_request", "code and redirect_uri required")
			return
		}
		pair, err := h.svc.Authorization.ExchangeCode(c.Request.Context(), service.ExchangeCodeInput{
			Code:         code,
			RedirectURI:  redirectURI,
			ClientID:     clientID,
			ClientSecret: clientSecret,
			CodeVerifier: codeVerifier,
			AccessTTL:    h.accessTokenTTL,
			RefreshTTL:   h.refreshTokenTTL,
		})
		if err != nil {
			writeTokenErrorFromService(c, err)
			return
		}
		writeTokenSuccess(c, pair)

	case oauth.GrantTypeRefreshToken:
		refreshToken := c.PostForm("refresh_token")
		if refreshToken == "" {
			writeTokenError(c, http.StatusBadRequest, "invalid_request", "refresh_token required")
			return
		}
		pair, err := h.svc.Authorization.RefreshToken(c.Request.Context(), service.RefreshTokenInput{
			RefreshToken: refreshToken,
			ClientID:     clientID,
			ClientSecret: clientSecret,
			AccessTTL:    h.accessTokenTTL,
			RefreshTTL:   h.refreshTokenTTL,
		})
		if err != nil {
			writeTokenErrorFromService(c, err)
			return
		}
		writeTokenSuccess(c, pair)

	default:
		writeTokenError(c, http.StatusBadRequest, "unsupported_grant_type", "grant_type must be authorization_code or refresh_token")
	}
}

// Revoke POST /oauth/revoke
//
// RFC 7009。入参:token / token_type_hint?(access_token / refresh_token)。
// 按规范即使 token 不存在 / 无效也返 200(避免让 client 探测 token 状态)。
func (h *Handler) Revoke(c *gin.Context) {
	token := c.PostForm("token")
	tokenTypeHint := c.PostForm("token_type_hint")
	clientID, clientSecret := extractClientCredentials(c)

	if token == "" || clientID == "" {
		writeTokenError(c, http.StatusBadRequest, "invalid_request", "token and client credentials required")
		return
	}

	err := h.svc.Authorization.RevokeByToken(c.Request.Context(), service.RevokeTokenInput{
		Token:        token,
		TokenType:    tokenTypeHint,
		ClientID:     clientID,
		ClientSecret: clientSecret,
	})
	if err != nil {
		// RFC 7009 §2.2 — 除了 invalid_client,其它错误都返 200。
		if errors.Is(err, oauth.ErrInvalidClient) {
			writeTokenError(c, http.StatusUnauthorized, "invalid_client", "")
			return
		}
		h.log.WarnCtx(c.Request.Context(), "oauth: revoke failed (non-fatal)", map[string]any{
			"client_id": clientID, "err": err.Error(),
		})
	}
	c.Status(http.StatusOK)
}

// ── helpers ──────────────────────────────────────────────────────────

// extractClientCredentials 按 OAuth 2.0 标准从 Basic header 或 form 里拎 client 凭证。
// 优先 Basic header(RFC 6749 推荐),没有时退到 form(client_secret_post)。
func extractClientCredentials(c *gin.Context) (clientID, clientSecret string) {
	auth := c.GetHeader("Authorization")
	if strings.HasPrefix(auth, "Basic ") {
		raw, err := base64.StdEncoding.DecodeString(auth[len("Basic "):])
		if err == nil {
			parts := strings.SplitN(string(raw), ":", 2)
			if len(parts) == 2 {
				return parts[0], parts[1]
			}
		}
	}
	return c.PostForm("client_id"), c.PostForm("client_secret")
}

func writeTokenSuccess(c *gin.Context, pair *service.TokenPair) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.JSON(http.StatusOK, tokenSuccess{
		AccessToken:  pair.AccessToken,
		TokenType:    pair.TokenType,
		ExpiresIn:    pair.ExpiresIn,
		RefreshToken: pair.RefreshToken,
		Scope:        pair.Scope,
	})
}

func writeTokenError(c *gin.Context, httpStatus int, code, description string) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.JSON(httpStatus, tokenError{Error: code, ErrorDescription: description})
}

// writeTokenErrorFromService 把 service 层哨兵错误翻译成 RFC 6749 token error。
func writeTokenErrorFromService(c *gin.Context, err error) {
	switch {
	case errors.Is(err, oauth.ErrInvalidClient):
		writeTokenError(c, http.StatusUnauthorized, "invalid_client", "")
	case errors.Is(err, oauth.ErrAuthorizationCodeExpired):
		writeTokenError(c, http.StatusBadRequest, "invalid_grant", "authorization_code expired")
	case errors.Is(err, oauth.ErrAuthorizationCodeAlreadyUsed):
		writeTokenError(c, http.StatusBadRequest, "invalid_grant", "authorization_code already used")
	case errors.Is(err, oauth.ErrPKCERequired), errors.Is(err, oauth.ErrPKCEMethodInvalid), errors.Is(err, oauth.ErrPKCEVerifierMismatch):
		writeTokenError(c, http.StatusBadRequest, "invalid_grant", "pkce verification failed")
	case errors.Is(err, oauth.ErrRefreshTokenRevoked):
		writeTokenError(c, http.StatusBadRequest, "invalid_grant", "refresh_token revoked")
	case errors.Is(err, oauth.ErrRefreshTokenExpired):
		writeTokenError(c, http.StatusBadRequest, "invalid_grant", "refresh_token expired")
	default:
		writeTokenError(c, http.StatusInternalServerError, "server_error", err.Error())
	}
}
