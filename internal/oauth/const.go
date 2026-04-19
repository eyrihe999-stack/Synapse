// Package oauth 最小实现 OAuth 2.1 Authorization Server,覆盖 MCP 远端接入所需子集:
//
//   - authorization_code grant + PKCE(S256)
//   - refresh_token grant + rotation
//   - Dynamic Client Registration (DCR, RFC 7591)
//   - /.well-known/oauth-authorization-server (RFC 8414)
//   - /.well-known/oauth-protected-resource (MCP 2025-03-26 要求)
//
// 不实现:implicit flow(OAuth 2.1 已废弃)、password grant(同)、client_credentials
// (MCP 场景不需要,用户身份是强制的)、token introspection(JWT 自带信息足够)。
//
// 客户端范围:Claude Desktop / Cursor / Anthropic SDK / 受控内部工具。
// DCR redirect_uri 白名单硬编码在 handler 层,非公网任意注册。
package oauth

import "time"

// 客户端状态。
const (
	ClientStatusActive    = "active"
	ClientStatusSuspended = "suspended" // admin 撤 or 安全事件
)

// DCR 允许的 redirect_uri 前缀白名单。新增客户端类型时改这里。
// 关键约束(OAuth 2.1):
//   - 除 localhost 外必须 HTTPS
//   - 不允许 http:// 非 localhost
//   - 不允许 file:// / data:// 等危险 scheme
var AllowedRedirectURIPrefixes = []string{
	"claude-desktop://",     // Claude Desktop 的 custom scheme
	"cursor://",             // Cursor 的 custom scheme
	"https://",              // 标准 web 客户端
	"http://localhost:",     // 本地开发(RFC 8252 native app exception)
	"http://127.0.0.1:",     // 同上
}

// 授权码生命周期。短越好 —— 只用来做一次交换。RFC 6749 推荐 ≤ 10 min;
// 我们设 5 min,让 Claude Desktop 等客户端的网络抖动仍能成功交换。
const AuthCodeTTL = 5 * time.Minute

// Access token(JWT)生命周期。MCP 客户端长期驻留场景下 15 分钟 + refresh rotation
// 是"安全 vs 体验"的平衡点 —— 太长则撤销滞后,太短则频繁 refresh 加压。
const AccessTokenTTL = 15 * time.Minute

// Refresh token 生命周期。30 天是 Claude Desktop 一类桌面客户端的合理长度;
// 过期后用户需重新走 authorize 流程(浏览器一键 consent)。
const RefreshTokenTTL = 30 * 24 * time.Hour

// PKCE code_challenge_method。OAuth 2.1 要求 S256(SHA256 + base64url,no padding);
// plain 已废弃,本实现直接拒绝。
const PKCEMethodS256 = "S256"

// Scope 常量。当前只用 1 个 —— "mcp" 代表"可调用 MCP endpoint 下所有 tools"。
// 粒度够不够细?够:tools 本身已按 modality 拆分,多租户隔离由 token 的 org_id 做。
// 未来若要按 modality 细分权限(如只允许 code 检索),再加 "mcp:code" / "mcp:document"。
const ScopeMCP = "mcp"

// JWT 里 access token 的 typ header,防和登录 JWT 混用。
const AccessTokenJWTType = "oauth-access+jwt"

// OAuth 错误码(RFC 6749 §5.2)。统一用字符串常量,避免 typo。
const (
	ErrCodeInvalidRequest          = "invalid_request"
	ErrCodeInvalidClient           = "invalid_client"
	ErrCodeInvalidGrant            = "invalid_grant"
	ErrCodeUnauthorizedClient      = "unauthorized_client"
	ErrCodeUnsupportedGrantType    = "unsupported_grant_type"
	ErrCodeInvalidScope            = "invalid_scope"
	ErrCodeAccessDenied            = "access_denied"
	ErrCodeUnsupportedResponseType = "unsupported_response_type"
	ErrCodeServerError             = "server_error"
	ErrCodeInvalidRedirectURI      = "invalid_redirect_uri"
	ErrCodeInvalidClientMetadata   = "invalid_client_metadata"
)
