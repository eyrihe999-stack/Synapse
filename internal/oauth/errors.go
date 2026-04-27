// errors.go OAuth 模块错误码 + 哨兵错误。
//
// 错误码格式 HHHSSCCCC:HHH=HTTP 状态码,SS=模块号 29(oauth),CCCC=业务码。
//
// 注意:部分 OAuth 端点遵循 RFC 6749 定义的标准错误响应 —— 那些端点的错误
// 由 handler 直接按 RFC 返(key: error/error_description),不走统一 BaseResponse。
// 本文件定义的哨兵只是 service / 管理 API 用。
package oauth

import "errors"

// ─── 400:请求 / 业务校验 ────────────────────────────────────────────────────

const (
	CodeOAuthInvalidRequest          = 400290010
	CodeClientNameInvalid            = 400290020
	CodeRedirectURIInvalid           = 400290021
	CodeRedirectURIsEmpty            = 400290022
	CodeGrantTypeInvalid             = 400290023
	CodeTokenAuthMethodInvalid       = 400290024
	CodePKCERequired                 = 400290030
	CodePKCEMethodInvalid            = 400290031
	CodePKCEVerifierMismatch         = 400290032
	CodeAuthorizationCodeExpired     = 400290040
	CodeAuthorizationCodeAlreadyUsed = 400290041
	CodeRefreshTokenExpired          = 400290050
	CodeRefreshTokenRevoked          = 400290051
	CodeAccessTokenExpired           = 400290060
	CodeAccessTokenRevoked           = 400290061
	CodeDCRRateLimited               = 400290070

	CodePATLabelInvalid = 400290080
)

// ─── 401:认证失败 ───────────────────────────────────────────────────────────

const (
	CodeInvalidClient = 401290010 // client_id 错 或 secret 不对
	CodeInvalidToken  = 401290020
)

// ─── 403 ────────────────────────────────────────────────────────────────────

const (
	CodeForbidden = 403290010
)

// ─── 404 ────────────────────────────────────────────────────────────────────

const (
	CodeClientNotFound = 404290010
	CodePATNotFound    = 404290020
)

// ─── 500 ────────────────────────────────────────────────────────────────────

const (
	CodeOAuthInternal = 500290010
)

// ─── 哨兵错误 ──────────────────────────────────────────────────────────────

var (
	ErrOAuthInternal = errors.New("oauth: internal error")

	ErrClientNameInvalid      = errors.New("oauth: client name invalid")
	ErrRedirectURIInvalid     = errors.New("oauth: redirect_uri invalid")
	ErrRedirectURIsEmpty      = errors.New("oauth: redirect_uris empty")
	ErrGrantTypeInvalid       = errors.New("oauth: grant_type invalid")
	ErrTokenAuthMethodInvalid = errors.New("oauth: token_endpoint_auth_method invalid")

	ErrPKCERequired         = errors.New("oauth: code_challenge required")
	ErrPKCEMethodInvalid    = errors.New("oauth: code_challenge_method must be S256")
	ErrPKCEVerifierMismatch = errors.New("oauth: pkce verifier mismatch")

	ErrAuthorizationCodeExpired     = errors.New("oauth: authorization_code expired")
	ErrAuthorizationCodeAlreadyUsed = errors.New("oauth: authorization_code already used")

	ErrRefreshTokenExpired = errors.New("oauth: refresh_token expired")
	ErrRefreshTokenRevoked = errors.New("oauth: refresh_token revoked")
	ErrAccessTokenExpired  = errors.New("oauth: access_token expired")
	ErrAccessTokenRevoked  = errors.New("oauth: access_token revoked")

	ErrInvalidClient = errors.New("oauth: invalid client credentials")
	ErrInvalidToken  = errors.New("oauth: invalid token")

	ErrClientNotFound = errors.New("oauth: client not found")
	ErrPATNotFound    = errors.New("oauth: pat not found")
	ErrPATLabelInvalid = errors.New("oauth: pat label invalid")

	ErrDCRRateLimited = errors.New("oauth: dcr rate limited")
	ErrForbidden      = errors.New("oauth: forbidden")
)
