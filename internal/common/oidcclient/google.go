// Package oidcclient 封装 Synapse 作为 OAuth 客户端(RP)接入第三方 IdP 登录所需的原语。
//
// 当前仅支持 Google OIDC。其他 provider(Feishu/GitHub/...)将来按同一风格加。
//
// 职责:
//   - 做 OIDC discovery 拿 endpoints + JWKS(NewGoogleClient 内完成)
//   - 构造 authorize URL + 生成 state/nonce 用的不可预测随机串
//   - 签/验 HMAC 状态 cookie(承载 state/nonce/device_id/device_name 过用户浏览器来回)
//   - 凭 code 换 token、验 id_token 签名和 claims
//
// 不管的事(留给上层 service):
//   - AuthResponse 组装、identity/user 合并、Redis exchange code、前端跳转
package oidcclient

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// googleIssuer 固定 issuer,go-oidc 走 /.well-known/openid-configuration 拉 endpoints + JWKS。
const googleIssuer = "https://accounts.google.com"

// stateCookieTTL 状态 cookie 有效期 —— 用户点"Google 登录"到 callback 回来的窗口。
// 10 分钟够完成 IdP 授权,再长意义不大,过短会让卡在 Google consent 页太久的用户失败。
const stateCookieTTL = 10 * time.Minute

// StateCookieName 状态 cookie 名,path 作用域限定在 /api/v1/auth/oauth 减少无关请求带 cookie。
const StateCookieName = "synapse_oauth_state"

// StateCookiePath cookie path 作用域。
const StateCookiePath = "/api/v1/auth/oauth"

// GoogleClient 封装和 Google OIDC 交互所需的 endpoint / verifier / oauth2 配置。
// 构造走 NewGoogleClient,Close 目前 no-op(连接池由 net/http 自管)。
type GoogleClient struct {
	provider      *oidc.Provider
	verifier      *oidc.IDTokenVerifier
	oauthConfig   *oauth2.Config
	cookieSecret  []byte // HMAC-SHA256 key
}

// StatePayload 状态 cookie 解码后的明文内容。
//
// state / nonce 分别防 CSRF 和 id_token 重放,device 字段让 OAuth 登录也能生成绑定到设备的 session。
type StatePayload struct {
	State      string `json:"state"`
	Nonce      string `json:"nonce"`
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`
}

// IDTokenClaims id_token 解码出的用户信息,字段和 Google 返的一致。
type IDTokenClaims struct {
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
}

// NewGoogleClient 构造一个 Google OIDC 客户端。
//
// ctx 参与 oidc discovery 的网络请求;启动时传 context.Background() 即可。
// cookieSecret 建议 ≥ 32 字节强随机;dev 走 applyDefaults 的默认值,生产必须由 env 覆盖。
//
// 返回 error 的典型场景:
//   - clientID / clientSecret / redirectURI / cookieSecret 任一为空
//   - Google discovery endpoint 不可达(启动 fatal,让部署方立刻察觉)
func NewGoogleClient(ctx context.Context, clientID, clientSecret, redirectURI string, cookieSecret []byte) (*GoogleClient, error) {
	if clientID == "" || clientSecret == "" || redirectURI == "" {
		return nil, errors.New("oidcclient: clientID / clientSecret / redirectURI 必填")
	}
	if len(cookieSecret) < 16 {
		return nil, errors.New("oidcclient: cookieSecret 至少 16 字节")
	}

	provider, err := oidc.NewProvider(ctx, googleIssuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}

	return &GoogleClient{
		provider: provider,
		verifier: provider.Verifier(&oidc.Config{ClientID: clientID}),
		oauthConfig: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  redirectURI,
			Endpoint:     provider.Endpoint(),
			Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
		},
		cookieSecret: cookieSecret,
	}, nil
}

// AuthorizeURL 构造跳 Google 的 URL,附带 state + nonce。返回的 payload 需要和 cookie 一起签发给用户浏览器。
func (c *GoogleClient) AuthorizeURL(state, nonce string) string {
	return c.oauthConfig.AuthCodeURL(state, oidc.Nonce(nonce))
}

// Exchange 凭 code 换 token + id_token;id_token 签名 + audience + nonce 全部校验。
// 不验 nonce 会留下 id_token 重放窗口;调用方必须把自己 cookie 里的 nonce 传进来对齐。
func (c *GoogleClient) Exchange(ctx context.Context, code, expectedNonce string) (*IDTokenClaims, error) {
	token, err := c.oauthConfig.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("oauth2 exchange: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return nil, errors.New("oidcclient: id_token 缺失,可能 scope 里少了 openid")
	}
	idToken, err := c.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("verify id_token: %w", err)
	}
	if idToken.Nonce != expectedNonce {
		return nil, errors.New("oidcclient: nonce 不匹配,拒绝 id_token")
	}
	var claims IDTokenClaims
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("parse id_token claims: %w", err)
	}
	if claims.Sub == "" {
		return nil, errors.New("oidcclient: id_token sub 为空")
	}
	return &claims, nil
}

// ─── state cookie ─────────────────────────────────────────────────────────────

// NewStatePayload 生成一条新的 state + nonce + 可选设备字段,返回待持久化的 StatePayload。
// state / nonce 各用 16 字节 crypto/rand,hex 编码后 32 字符,无法预测。
func NewStatePayload(deviceID, deviceName string) (*StatePayload, error) {
	state, err := randHex(16)
	if err != nil {
		return nil, err
	}
	nonce, err := randHex(16)
	if err != nil {
		return nil, err
	}
	return &StatePayload{
		State: state, Nonce: nonce,
		DeviceID: deviceID, DeviceName: deviceName,
	}, nil
}

// Sign 把 payload 压成可放 cookie 的 "payloadB64.hmacB64" 字符串。
//sayso-lint:ignore godoc-error-undoc
func (c *GoogleClient) Sign(payload *StatePayload) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal state: %w", err)
	}
	body := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, c.cookieSecret)
	mac.Write([]byte(body))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return body + "." + sig, nil
}

// Verify 校验 cookie 值的签名,返回 payload。签名不通过直接拒,防篡改。
func (c *GoogleClient) Verify(cookieValue string) (*StatePayload, error) {
	parts := strings.SplitN(cookieValue, ".", 2)
	if len(parts) != 2 {
		return nil, errors.New("oidcclient: state cookie 格式非法")
	}
	body, sig := parts[0], parts[1]
	mac := hmac.New(sha256.New, c.cookieSecret)
	mac.Write([]byte(body))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		return nil, errors.New("oidcclient: state cookie 签名不通过")
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return nil, fmt.Errorf("decode state body: %w", err)
	}
	var p StatePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("unmarshal state: %w", err)
	}
	return &p, nil
}

// SetStateCookie 把签好的 payload 塞进 http.ResponseWriter 作为 HttpOnly / SameSite=Lax cookie。
// Secure 跟着 cookie_secure 配置走(oauth AS 的 CookieSecure;login 场景复用同一部署判断)。
func (c *GoogleClient) SetStateCookie(w http.ResponseWriter, signed string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     StateCookieName,
		Value:    signed,
		Path:     StateCookiePath,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(stateCookieTTL.Seconds()),
	})
}

// ClearStateCookie 让用户浏览器立刻丢弃 state cookie。callback 成功/失败后都应调。
func ClearStateCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     StateCookieName,
		Value:    "",
		Path:     StateCookiePath,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// randHex 返回 n 字节随机 hex 串。crypto/rand 保证不可预测。
func randHex(n int) (string, error) {
	b := make([]byte, n)
	//sayso-lint:ignore err-swallow
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}
