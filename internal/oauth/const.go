// Package oauth Synapse 的 OAuth 2.1 Authorization Server,服务于 MCP remote
// connector 接入(主要对接 Claude Desktop)。
//
// 只一个 scope `mcp:invoke`;授权对象是 (user, client, auto-created agent) 三元组;
// MCP tool 权限统一走 channel_members / principal RBAC,不靠 OAuth scope 细化。
//
// 设计对齐 docs/collaboration-design.md §3.6.2。本模块只签发凭证,不做 MCP 业务
// 逻辑;业务层由 internal/mcp + channel / task / KB 模块承担。
package oauth

import "time"

// ─── OAuth 标准常量 ─────────────────────────────────────────────────────────

const (
	// Scope Synapse 只有一个 scope。MCP tool 权限靠 principal ACL,不靠 OAuth scope 细分。
	ScopeMCPInvoke = "mcp:invoke"

	// GrantType 支持的 grant 类型。
	GrantTypeAuthorizationCode = "authorization_code"
	GrantTypeRefreshToken      = "refresh_token"

	// ResponseType 只支持 code(授权码 flow)。
	ResponseTypeCode = "code"

	// TokenType 发出的 access token 类型,永远是 Bearer。
	TokenTypeBearer = "Bearer"

	// PKCE 签名算法,MCP 2025-11-25 规范要求强制 S256(SHA-256 BASE64URL)。
	PKCEMethodS256 = "S256"

	// TokenEndpointAuthMethod 客户端身份认证方式。
	TokenAuthClientSecretBasic = "client_secret_basic" // HTTP Basic header
	TokenAuthClientSecretPost  = "client_secret_post"  // form body 里的 client_id/secret
	TokenAuthNone              = "none"                // public client,只靠 PKCE 防 code 截获
)

// ─── Token / Code 前缀 ──────────────────────────────────────────────────────

const (
	// 可读前缀便于日志 / 抓包一眼区分。实际存 DB 的是 sha256 hash,不存明文。
	AccessTokenPrefix     = "syn_at_"
	RefreshTokenPrefix    = "syn_rt_"
	AuthorizationCodePfx  = "syn_ac_"
	ClientSecretPrefix    = "syn_cs_"
	ClientIDPrefix        = "syn_ci_"
)

// ─── 随机熵字节数 ───────────────────────────────────────────────────────────

const (
	TokenRandomBytes     = 32 // 256 bit,sha256 hash 足够
	ClientIDRandomBytes  = 16
	ClientSecretBytes    = 32
	AgentSlugRandomBytes = 6 // 生成 syn-auto-<hex> 的 agent_id 用
)

// ─── 默认 TTL ───────────────────────────────────────────────────────────────

const (
	DefaultAccessTokenTTL       = 24 * time.Hour
	DefaultRefreshTokenTTL      = 30 * 24 * time.Hour
	DefaultAuthorizationCodeTTL = 10 * time.Minute
)

// ─── Client 注册来源 ─────────────────────────────────────────────────────────

const (
	RegisteredViaManual = "manual" // 管理员后台建
	RegisteredViaDCR    = "dcr"    // RFC 7591 动态注册
)

// ─── DCR 限速默认值 ──────────────────────────────────────────────────────────

const (
	DefaultDCRRateLimitWindow = 60 // seconds
	DefaultDCRRateLimitMax    = 10 // 单 IP 每窗口最多注册次数
)

// ─── 资源尺寸 ───────────────────────────────────────────────────────────────

const (
	ClientNameMaxLen       = 128
	RedirectURIMaxLen      = 512
	RedirectURIsMaxCount   = 10
	PATLabelMaxLen         = 128
)
