// Package model oauth 模块 MySQL 表结构。
//
// 设计取向:
//   - 三张表,每张表各管一个短/中/长寿命资源,生命周期清晰不交叉
//   - access token 不落表 —— JWT 自带 sub / scope / exp / org_id,网关无状态校验
//   - auth code / refresh token 都只存 SHA256 哈希,即便 DB 泄露也拿不到原 token
//   - refresh token 轮换链(parent / replaced_by)支持 RFC 6749 §10.4 reuse detection
package model

import (
	"time"

	"gorm.io/datatypes"
)

const (
	tableOAuthClients            = "oauth_clients"
	tableOAuthAuthorizationCodes = "oauth_authorization_codes"
	tableOAuthRefreshTokens      = "oauth_refresh_tokens"
)

// OAuthClient 已注册的 MCP 客户端。
//
// 注册路径:
//   - DCR(/oauth/register):自助注册,CreatedByUserID 填调用者
//   - admin 预建:用户在 UI 里点"添加内部 agent",redirect_uri 可不受白名单限制
//
// ClientID 对外展示,ClientSecret(confidential client)只存哈希 —— 返回一次后丢失无法找回,和 GitHub PAT 一致。
// Public client(PKCE-only,如 Claude Desktop)ClientSecretHash 为空。
type OAuthClient struct {
	ID uint64 `gorm:"primaryKey;autoIncrement"`

	// ClientID 对外 id,opaque 随机串。长度 32 够抗碰撞 + URL-safe。
	ClientID string `gorm:"size:64;not null;uniqueIndex:uk_oauth_clients_cid"`

	// ClientName 用户可见名字,展示在 consent 页上。从 DCR client_name 字段取。
	ClientName string `gorm:"size:128;not null;default:''"`

	// RedirectURIs JSON 数组。严格等式匹配,不允许子串/通配。
	RedirectURIs datatypes.JSON `gorm:"type:json;not null"`

	// GrantTypes / ResponseTypes JSON 数组。当前仅支持 ["authorization_code","refresh_token"] / ["code"]。
	GrantTypes    datatypes.JSON `gorm:"type:json;not null"`
	ResponseTypes datatypes.JSON `gorm:"type:json;not null"`

	// TokenEndpointAuthMethod 客户端在 /token 的认证方式。
	// "none" = public client(PKCE-only,无 secret;Claude Desktop 用此)
	// "client_secret_basic" = confidential client(需要 Basic auth 头)
	TokenEndpointAuthMethod string `gorm:"size:32;not null;default:'none'"`

	// ClientSecretHash bcrypt 哈希。public client 为空字符串。
	ClientSecretHash string `gorm:"size:255;not null;default:''"`

	// Scope 允许申请的 scope 白名单,空格分隔。留空表示"任何合法 scope 都允许"。
	// 配合 /authorize 时传入的 scope 做交集。
	Scope string `gorm:"size:255;not null;default:''"`

	// Status: active / suspended。suspended 客户端的 token 校验直接拒绝。
	Status string `gorm:"size:16;not null;default:'active';index:idx_oauth_clients_status"`

	// CreatedByUserID 谁注册的;DCR 路径下是发起 authorize 的用户,admin 路径下是 admin。
	// NULL 表示系统/迁移预置。
	CreatedByUserID *uint64

	// Metadata 原始 DCR 请求 JSON,留档参考(未来可读出展示更多元信息)。
	Metadata datatypes.JSON `gorm:"type:json"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

func (OAuthClient) TableName() string { return tableOAuthClients }

// OAuthAuthorizationCode 短寿命授权码。
//
// 生命周期:/authorize 发码 → 5min 内 /token 凭 code 交换一次 → used_at 置位,不可再用。
// 单用强制:Exchange 走 tx + SELECT FOR UPDATE,避免并发重放。
//
// PKCE 字段必填(OAuth 2.1 规定 authorization_code grant 必须 PKCE)。
type OAuthAuthorizationCode struct {
	ID uint64 `gorm:"primaryKey;autoIncrement"`

	// CodeHash SHA256 hex(64 char)。code 明文只发给 client,DB 里不留原值。
	CodeHash string `gorm:"size:64;not null;uniqueIndex:uk_oauth_auth_codes_hash"`

	// ClientID 发给哪个 client。和 /token 来的 client_id 必须一致,否则 invalid_grant。
	ClientID string `gorm:"size:64;not null;index:idx_oauth_auth_codes_client"`

	// UserID / OrgID 用户在 consent 页选的 org,将来 access token 的 sub / org 固化到此。
	UserID uint64 `gorm:"not null"`
	OrgID  uint64 `gorm:"not null"`

	// Scope 用户在 consent 页上同意的 scope(可能是 client 请求 scope 的子集)。
	Scope string `gorm:"size:255;not null;default:''"`

	// RedirectURI /authorize 时使用的 redirect_uri。/token 交换时必须精确相等(RFC 6749 §4.1.3)。
	RedirectURI string `gorm:"size:512;not null"`

	// PKCE 绑定。ChallengeMethod 目前仅允许 "S256"。
	CodeChallenge       string `gorm:"size:128;not null"`
	CodeChallengeMethod string `gorm:"size:16;not null;default:'S256'"`

	// ExpiresAt 过期时间。/token 交换时若 now > expires_at 或 used_at != NULL → invalid_grant。
	ExpiresAt time.Time `gorm:"not null;index:idx_oauth_auth_codes_expires"`
	UsedAt    *time.Time

	CreatedAt time.Time
}

func (OAuthAuthorizationCode) TableName() string { return tableOAuthAuthorizationCodes }

// OAuthRefreshToken 可轮换的长寿命 refresh token。
//
// 生命周期:/token(authorization_code)发一条 → client 用 refresh_token grant 轮换
// 旧的置 RevokedAt + ReplacedByHash,新的 ParentTokenHash 指向旧的 → 形成链。
// 若 client 复用已轮换(revoked_at != NULL)的 token,视为泄露信号 —— 整条链全部 revoke。
type OAuthRefreshToken struct {
	ID uint64 `gorm:"primaryKey;autoIncrement"`

	// TokenHash SHA256 hex。和 auth code 同策略,明文只发给 client。
	TokenHash string `gorm:"size:64;not null;uniqueIndex:uk_oauth_refresh_tokens_hash"`

	// ClientID / UserID / OrgID / Scope 和发行 access token 时一致,refresh 签新 access token 直接复用。
	ClientID string `gorm:"size:64;not null;index:idx_oauth_refresh_tokens_client"`
	UserID   uint64 `gorm:"not null;index:idx_oauth_refresh_tokens_user"`
	OrgID    uint64 `gorm:"not null"`
	Scope    string `gorm:"size:255;not null;default:''"`

	// ParentTokenHash 本 token 的上一级(轮换链)。根 token(首次发)为 ""。
	// 用来在 reuse detection 里回溯整条链。
	ParentTokenHash string `gorm:"size:64;not null;default:'';index:idx_oauth_refresh_tokens_parent"`

	// ReplacedByHash 被哪个新 token 替换了。非空即表示本 token 已被轮换过,不应再被使用。
	ReplacedByHash string `gorm:"size:64;not null;default:''"`

	ExpiresAt time.Time `gorm:"not null;index:idx_oauth_refresh_tokens_expires"`
	RevokedAt *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

func (OAuthRefreshToken) TableName() string { return tableOAuthRefreshTokens }
