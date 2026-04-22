// Package user_integration 用户对接第三方平台(飞书 / Notion / GitLab / ...)的凭据持久层。
//
// 职责边界:
//
//   - 只存"某用户在某 provider 的一套 OAuth / PAT 凭据",不管订阅粒度
//   - 不做 OAuth 协议握手 / token 刷新(那是上层 service 的事,需要 provider 客户端)
//   - 也不做多 org 共用:当前字段 org_id 可空,表示"账号级连接",由 ingestion runner 构造时自选 org
//
// 安全备注(决策 5):本期 access_token / refresh_token **明文存 MySQL**。字段名没加 `_enc`
// 后缀给后续升级留的空间 —— 将来真要上加密,新加 `access_token_enc / access_token_nonce` 两列,
// service 层 preferred 读 enc → fallback 明文,灰度后删明文列。
package user_integration

// ─── 表名 ────────────────────────────────────────────────────────────────────

const (
	TableUserIntegrations = "user_integrations"
)

// ─── Provider 常量 ───────────────────────────────────────────────────────────
//
// 允许集:新增一个 provider 加常量即可。service 层按 provider 路由到具体 OAuth 客户端。
const (
	ProviderFeishu = "feishu"
	ProviderNotion = "notion"
	ProviderGitLab = "gitlab"
)

// ─── 状态 ────────────────────────────────────────────────────────────────────

const (
	// StatusActive 凭据有效,sync runner 可以直接用。
	StatusActive = "active"
	// StatusExpired refresh_token 也不可用了,需要用户重新授权。
	StatusExpired = "expired"
	// StatusRevoked 用户在对方平台撤销了授权 / 主动断开,不会自动变回 active。
	StatusRevoked = "revoked"
)
