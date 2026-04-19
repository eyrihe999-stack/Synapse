// types.go service 层输入输出类型 + Service 接口。
package service

import (
	"encoding/json"
	"time"
)

// Service oauth 业务层对外接口。handler 只调这些方法,不直接碰 repo。
type Service interface {
	// RegisterClient DCR(RFC 7591)自注册入口。
	// 强制 token_endpoint_auth_method="none"(仅 PKCE public client);
	// redirect_uris 每条过白名单;失败返 ErrInvalidClientMetadata。
	RegisterClient(req ClientRegistrationReq) (*ClientRegistrationResp, error)

	// ValidateAuthRequest /authorize 流程前置:校验 client / redirect_uri / response_type /
	// PKCE params 是否合法。供 handler 在显示 consent 页前用 —— 参数错就直接给 client 看错误页,
	// 不让用户在登录到一半时才掉坑。
	//
	// 返回解析出的 client(用 client_name 等字段填 consent UI)。
	ValidateAuthRequest(req AuthorizeRequest) (*ClientInfo, error)

	// IssueAuthCode consent 通过后,签发一次性授权码。
	// 调用方必须保证 UserID / OrgID 来自已认证 session + 用户在 consent 页显式选的 org。
	IssueAuthCode(req IssueAuthCodeReq) (code string, err error)

	// ExchangeAuthCode /token 端点 authorization_code grant。
	// 原子单用 + PKCE 校验 + 签发 access(JWT)+ refresh(opaque,持久)。
	ExchangeAuthCode(req ExchangeAuthCodeReq) (*TokenResponse, error)

	// RefreshAccessToken /token 端点 refresh_token grant。
	// 轮换策略(RFC 6749 §10.4):旧 token revoke + 链 parent 指回 + 插入新 token。
	// reuse detection:旧 token 再次使用 → 整链 revoke + 拒绝。
	RefreshAccessToken(req RefreshReq) (*TokenResponse, error)

	// Revoke /oauth/revoke(RFC 7009)。
	// 当前只支持 refresh_token 撤销;access_token 由于是无状态 JWT,撤销只能等自然过期。
	Revoke(token, tokenTypeHint string) error

	// ValidateAccessToken middleware 用。纯密码学校验(签名 + exp + typ header + iss),不查 DB。
	// 返权限信息供中间件注入 gin context。
	ValidateAccessToken(token string) (*AccessTokenClaims, error)
}

// ─── 请求/响应类型 ──────────────────────────────────────────────────────────

// ClientRegistrationReq DCR 请求体,对应 RFC 7591 §2 client metadata。
type ClientRegistrationReq struct {
	ClientName              string          `json:"client_name"`
	RedirectURIs            []string        `json:"redirect_uris"`
	GrantTypes              []string        `json:"grant_types,omitempty"`               // 省略 = 默认 ["authorization_code","refresh_token"]
	ResponseTypes           []string        `json:"response_types,omitempty"`            // 省略 = 默认 ["code"]
	TokenEndpointAuthMethod string          `json:"token_endpoint_auth_method,omitempty"` // 只接受 "none"
	Scope                   string          `json:"scope,omitempty"`                      // 空格分隔,空 = 允许所有支持的 scope
	Metadata                json.RawMessage `json:"-"`                                    // 原始请求 body,留档;不 JSON-marshal 回去

	// 注册者身份 —— 不在 DCR spec 里,是我们内部审计用的。handler 从登录 session 注入。
	// nil = 匿名 DCR(走硬白名单 scheme 的客户端,如 claude-desktop://),允许
	CreatedByUserID *uint64 `json:"-"`
}

// ClientRegistrationResp DCR 返回体。client_secret 仅在 confidential client 有;
// 本实现只允许 public client,client_secret 永远为空。
type ClientRegistrationResp struct {
	ClientID                string   `json:"client_id"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	Scope                   string   `json:"scope,omitempty"`
	CreatedAt               int64    `json:"client_id_issued_at"` // RFC 7591 字段名
}

// ClientInfo consent 页渲染 + token endpoint 校验用的客户端公开信息。不含 secret。
type ClientInfo struct {
	ClientID     string
	ClientName   string
	RedirectURIs []string
	Scope        string // client 允许的 scope(白名单)
}

// AuthorizeRequest /authorize 进来时的参数集合(OAuth 2.1 §4.1.1)。
type AuthorizeRequest struct {
	ClientID            string
	RedirectURI         string
	ResponseType        string // 必须 "code"
	Scope               string // 空格分隔
	State               string // CSRF 防护,原样回传给 client,服务器不解释
	CodeChallenge       string
	CodeChallengeMethod string // 必须 "S256"
}

// IssueAuthCodeReq 发码输入。handler 在 consent 通过后填充。
type IssueAuthCodeReq struct {
	ClientID            string
	UserID              uint64
	OrgID               uint64 // 用户在 consent 页选定的 org
	Scope               string // 实际授权的(可能是请求 scope 的子集)
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
}

// ExchangeAuthCodeReq /token authorization_code grant 入参。
type ExchangeAuthCodeReq struct {
	Code         string
	ClientID     string
	RedirectURI  string
	CodeVerifier string // PKCE 明文 verifier,服务端算 sha256 和 challenge 比
}

// RefreshReq /token refresh_token grant 入参。
type RefreshReq struct {
	RefreshToken string
	ClientID     string
	Scope        string // 可选;若给,必须是原 scope 的子集
}

// TokenResponse /token 端点返回(RFC 6749 §5.1)。
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`  // 始终 "Bearer"
	ExpiresIn    int    `json:"expires_in"`  // access token 剩余秒数
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// AccessTokenClaims ValidateAccessToken 返回,middleware 注入 gin context 用。
type AccessTokenClaims struct {
	Subject  uint64    // user_id
	OrgID    uint64
	ClientID string
	Scope    string
	JTI      string    // 未来 blocklist 扩展用
	IssuedAt time.Time
	ExpiresAt time.Time
}
