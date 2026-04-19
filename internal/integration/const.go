// Package integration 外部系统 OAuth 集成(飞书、Google、Slack 等)的 token 持久化与刷新编排。
//
// 一个 UserIntegration 记录一个 (user_id, provider) 对 —— 该用户通过 OAuth 授权了该 provider 的
// refresh_token 给 synapse。asyncjob runner(见 internal/asyncjob/runners/feishusync)或后续
// 任何同步逻辑拿 refresh_token 构造对应 Adapter,以用户身份调外部 API 拉内容入库
// (走 document.Service.Upload + source_type/source_ref upsert)。
//
// 职责切分:
//   - 本包只管 token 存储、刷新编排、查询 —— 不知道 provider 的 API 细节
//   - 具体 API 调用在 pkg/sourceadapter/{feishu,git,...} 里
//   - OAuth HTTP 入口在 internal/integration/handler 里
package integration

// Provider 常量。取值写入 user_integrations.provider 列,和各 adapter 的 Type() 对应。
// 命名可以和 SourceAdapter Type 一致(feishu_doc 简化成 feishu),避免 adapter type 膨胀变成 provider key。
const (
	ProviderFeishu = "feishu"
	ProviderGitLab = "gitlab"
	// ProviderGitHub = "github" // Slice 5+
	// ProviderJira   = "jira"
)
