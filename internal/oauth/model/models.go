// Package model OAuth AS 模块的数据模型。
//
// 表:
//   - oauth_clients                    注册的 client(Claude Desktop 等)
//   - oauth_authorization_codes        一次性 code(authorize→token 之间)
//   - oauth_access_tokens              已签发的 access token(sha256 hash 存 DB)
//   - oauth_refresh_tokens             对应的 refresh token
//   - user_pats                        personal access tokens(curl / Cursor / Codex 用)
//
// 所有 token 明文**只在签发那一次返给客户端**,DB 存 sha256 hash。
package model

import "time"

// OAuthClient 注册的 MCP client。
//
// 字段语义:
//   - ClientID              公开 id,client 在 token/authorize 请求里带过来
//   - ClientSecretHash      sha256(明文 secret);public client 时为空串
//   - RedirectURIsJSON      JSON array of string;authorize 时校验 redirect_uri ∈ 此列表
//   - GrantTypesJSON        JSON array,当前允许 ["authorization_code","refresh_token"]
//   - TokenAuthMethod       client_secret_basic / client_secret_post / none(public client)
//   - RegisteredVia         'manual'(管理员建)/ 'dcr'(RFC 7591 动态注册)
//   - RegisteredByUserID    manual 时填建者;DCR 时 0
//   - Disabled              一键禁用,相当于 revoke 所有该 client 的 token(中间件拦)
type OAuthClient struct {
	ID                   uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	ClientID             string    `gorm:"column:client_id;size:64;not null;uniqueIndex:uk_oauth_clients_client_id" json:"client_id"`
	ClientSecretHash     string    `gorm:"column:client_secret_hash;size:128;not null" json:"-"`
	ClientName           string    `gorm:"column:client_name;size:128;not null" json:"client_name"`
	RedirectURIsJSON     string    `gorm:"column:redirect_uris;type:json;not null" json:"redirect_uris_json"` // JSON array
	GrantTypesJSON       string    `gorm:"column:grant_types;type:json;not null" json:"grant_types_json"`     // JSON array
	TokenAuthMethod      string    `gorm:"column:token_auth_method;size:32;not null" json:"token_endpoint_auth_method"`
	RegisteredVia        string    `gorm:"column:registered_via;size:16;not null;index:idx_oauth_clients_via" json:"registered_via"`
	RegisteredByUserID   uint64    `gorm:"column:registered_by_user_id;not null;default:0" json:"registered_by_user_id,omitempty"`
	Disabled             bool      `gorm:"not null;default:false;index:idx_oauth_clients_disabled" json:"disabled"`
	CreatedAt            time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt            time.Time `gorm:"not null" json:"updated_at"`
}

// TableName 固定表名。
func (OAuthClient) TableName() string { return "oauth_clients" }

// OAuthAuthorizationCode 一次性授权码。
//
// 生命周期:authorize 同意时创建 → token 端点 exchange 时 ConsumedAt 非空 → 过期
// 或消费后 service 层直接视为 invalid。
//
// 消费必须原子:UPDATE ... SET consumed_at=NOW() WHERE id=? AND consumed_at IS NULL
// RowsAffected=0 说明并发 / 重放,service 拒绝。
//
// 字段语义:
//   - CodeHash             sha256(明文 code),UNIQUE
//   - ClientID             签发时的 client_id(字符串,非 FK)
//   - UserID               授权用户
//   - AgentID              consent 时自动建的 agent.id;token exchange 时带出放进 access_token
//   - RedirectURI          授权时传的 redirect_uri,token 端点要重复校验一致
//   - Scope                当前只 ScopeMCPInvoke
//   - PKCEChallenge        PKCE code_challenge(必填,S256 强制)
//   - PKCEChallengeMethod  "S256"
//   - ExpiresAt            ~10 分钟 TTL
//   - ConsumedAt           非空 = 已用,再次 exchange 要按 RFC 6749 视为错误并吊销后续 tokens
type OAuthAuthorizationCode struct {
	ID                   uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	CodeHash             string     `gorm:"column:code_hash;size:128;not null;uniqueIndex:uk_oauth_codes_hash" json:"-"`
	ClientID             string     `gorm:"column:client_id;size:64;not null;index:idx_oauth_codes_client" json:"client_id"`
	UserID               uint64     `gorm:"column:user_id;not null" json:"user_id"`
	AgentID              uint64     `gorm:"column:agent_id;not null" json:"agent_id"`
	RedirectURI          string     `gorm:"column:redirect_uri;size:512;not null" json:"redirect_uri"`
	Scope                string     `gorm:"column:scope;size:128;not null" json:"scope"`
	PKCEChallenge        string     `gorm:"column:pkce_challenge;size:256;not null" json:"-"`
	PKCEChallengeMethod  string     `gorm:"column:pkce_challenge_method;size:16;not null" json:"-"`
	ExpiresAt            time.Time  `gorm:"column:expires_at;not null;index:idx_oauth_codes_expires" json:"expires_at"`
	ConsumedAt           *time.Time `gorm:"column:consumed_at" json:"consumed_at,omitempty"`
	CreatedAt            time.Time  `gorm:"not null" json:"created_at"`
}

// TableName 固定表名。
func (OAuthAuthorizationCode) TableName() string { return "oauth_authorization_codes" }

// OAuthAccessToken 已签发的 access token 记录。
//
// 字段语义:
//   - TokenHash         sha256(明文 token),UNIQUE
//   - ClientID / UserID / AgentID / Scope   授权三元组 + scope
//   - ExpiresAt         TTL 内有效
//   - RevokedAt         revoke / client-disabled / user-logout 时填
//   - LastUsedAt        中间件命中时异步更新(best-effort,用于活跃度展示)
type OAuthAccessToken struct {
	ID          uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	TokenHash   string     `gorm:"column:token_hash;size:128;not null;uniqueIndex:uk_oauth_at_hash" json:"-"`
	ClientID    string     `gorm:"column:client_id;size:64;not null;index:idx_oauth_at_client" json:"client_id"`
	UserID      uint64     `gorm:"column:user_id;not null;index:idx_oauth_at_user" json:"user_id"`
	AgentID     uint64     `gorm:"column:agent_id;not null" json:"agent_id"`
	Scope       string     `gorm:"column:scope;size:128;not null" json:"scope"`
	ExpiresAt   time.Time  `gorm:"column:expires_at;not null;index:idx_oauth_at_expires" json:"expires_at"`
	RevokedAt   *time.Time `gorm:"column:revoked_at" json:"revoked_at,omitempty"`
	LastUsedAt  *time.Time `gorm:"column:last_used_at" json:"last_used_at,omitempty"`
	CreatedAt   time.Time  `gorm:"not null" json:"created_at"`
}

// TableName 固定表名。
func (OAuthAccessToken) TableName() string { return "oauth_access_tokens" }

// OAuthRefreshToken 对应 access_token 的 refresh token。
//
// 字段语义:
//   - TokenHash                sha256(明文 refresh token),UNIQUE
//   - AccessTokenID            关联的 access_token.id(refresh 成功后 revoke 原 access_token)
//   - RotatedToTokenHash       已轮换到下一个 refresh token 时填。用于检测重放攻击
//                              (revoked + rotated 到同一个后代 → 安全;否则 = refresh token 泄露,
//                              链式撤销当前 client 的所有 tokens)
type OAuthRefreshToken struct {
	ID                  uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	TokenHash           string     `gorm:"column:token_hash;size:128;not null;uniqueIndex:uk_oauth_rt_hash" json:"-"`
	AccessTokenID       uint64     `gorm:"column:access_token_id;not null;index:idx_oauth_rt_access" json:"access_token_id"`
	ClientID            string     `gorm:"column:client_id;size:64;not null;index:idx_oauth_rt_client" json:"client_id"`
	UserID              uint64     `gorm:"column:user_id;not null" json:"user_id"`
	AgentID             uint64     `gorm:"column:agent_id;not null" json:"agent_id"`
	Scope               string     `gorm:"column:scope;size:128;not null" json:"scope"`
	ExpiresAt           time.Time  `gorm:"column:expires_at;not null" json:"expires_at"`
	RevokedAt           *time.Time `gorm:"column:revoked_at" json:"revoked_at,omitempty"`
	RotatedToTokenHash  string     `gorm:"column:rotated_to_token_hash;size:128" json:"-"`
	CreatedAt           time.Time  `gorm:"not null" json:"created_at"`
}

// TableName 固定表名。
func (OAuthRefreshToken) TableName() string { return "oauth_refresh_tokens" }

// UserPAT Personal Access Token(走 Cursor / Codex / curl 等非 OAuth 路径)。
//
// PAT 语义和 OAuthAccessToken 一致(Bearer → user+agent),但绕过 OAuth flow
// (用户管理页生成 / 吊销)。发一次看明文,以后只有 hash。
type UserPAT struct {
	ID         uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	TokenHash  string     `gorm:"column:token_hash;size:128;not null;uniqueIndex:uk_user_pats_hash" json:"-"`
	UserID     uint64     `gorm:"column:user_id;not null;index:idx_user_pats_user" json:"user_id"`
	AgentID    uint64     `gorm:"column:agent_id;not null" json:"agent_id"`
	Label      string     `gorm:"column:label;size:128;not null" json:"label"`
	ExpiresAt  *time.Time `gorm:"column:expires_at" json:"expires_at,omitempty"` // NULL = 不过期
	LastUsedAt *time.Time `gorm:"column:last_used_at" json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `gorm:"column:revoked_at" json:"revoked_at,omitempty"`
	CreatedAt  time.Time  `gorm:"not null" json:"created_at"`
}

// TableName 固定表名。
func (UserPAT) TableName() string { return "user_pats" }
