// Package llm LLM chat completions 统一入口。
//
// 对上层(顶级系统 agent runtime、后续专项 agent)屏蔽 provider 差异,
// 调用方只看到 Chat.Completions / Model 两个方法。当前实现:
//   - azure:Azure OpenAI v1 surface,支持 tools(OpenAI function calling 协议)。
//
// 没有 fake provider —— 为了让 dev/staging/prod 行为完全一致。
// 测试替身走 mock Chat interface(如 testify/mock),不走 factory。
package llm

import (
	"context"
	"errors"
	"fmt"
)

// Provider 名称常量,和 config.LLMConfig.Provider 对齐。
const (
	ProviderAzure = "azure"
)

// Role LLM 消息角色。和 OpenAI chat completions 协议一致。
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Chat LLM 调用接口。
//
// Completions 单次请求 → 单次响应(非流式)。流式场景(SSE)后续需要时再加
// 独立方法,不硬塞进 Completions 签名。
//
// Model 返回落库用的模型标识(形如 "gpt-5.4@azure"),和 embedding.Model 同一套路
// —— 避免和未来其他 provider 的同名模型撞库(例如 OpenAI 原生 vs Azure 部署)。
type Chat interface {
	Completions(ctx context.Context, req Request) (*Response, error)
	Model() string
}

// Request 一次 chat completions 调用的入参。
//
// Tools 为 nil 或 len==0 时不启用 function calling;Tools 非空时 provider 会把
// OpenAI 协议的 tools 字段填进请求,由 LLM 决定是否返回 tool_calls。
//
// Temperature 0 表示采用 provider 默认值(OpenAI 默认 1.0);调用方想稳定输出
// 一般传 0.2~0.7。
//
// MaxOutputTokens 0 = provider 默认上限;对顶级 agent 建议传 1024~2048,
// 防止长回复烧钱。
type Request struct {
	Messages        []Message
	Tools           []ToolDef
	Temperature     float32
	MaxOutputTokens int
}

// Message chat completions 的一条对话消息。
//
// Role ∈ {system, user, assistant, tool}。
//
// Tool 角色消息必须填 ToolCallID(对应上一轮 assistant 消息返回的 tool_call.id),
// Content 为 tool 的执行结果(通常是 JSON 字符串)。
//
// Assistant 角色在 tool-loop 历史里回灌时:
//   - 若本轮 LLM 返回了 tool_calls,必须把这些 tool_calls **原样回灌**到 Message.ToolCalls
//     —— 否则后续 role=tool 消息不合法(provider 要求 tool result 必须有对应 assistant.tool_calls)
//   - Content 可以为空串,但不得为 null(azure.go 里 json:"content" 无 omitempty)
type Message struct {
	Role       string
	Content    string
	ToolCallID string     // 仅 Role=tool 有效
	ToolCalls  []ToolCall // 仅 Role=assistant 且本轮有调用时填;保证后续 role=tool 合法
}

// ToolDef 暴露给 LLM 的工具描述(OpenAI function calling 协议)。
//
// ParametersJSONSchema 必须是合法 JSON Schema(draft 2020-12 子集),
// 由调用方保证。provider 会直接塞进请求体,LLM 据此决定参数类型/必填。
//
// 顶级 agent 的工具白名单在 internal/agentsys/tools 里定义,**不得包含**
// org_id / channel_id 字段 —— 作用域由 Go 侧 ScopedServices 绑死。
type ToolDef struct {
	Name                string
	Description         string
	ParametersJSONSchema map[string]any
}

// Response chat completions 的返回。
//
// Content 是 assistant 的自然语言输出(无 tool_calls 时);ToolCalls 非空时
// Content 通常为空或一句"我要调用工具"的话,由 LLM 自主决定。
//
// Usage 精确到 prompt / completion tokens,写 llm_usage 表用。
type Response struct {
	Content   string
	ToolCalls []ToolCall
	Usage     Usage
}

// ToolCall LLM 请求执行的一次工具调用。
//
// ArgumentsJSON 是字符串形态的 JSON(即 OpenAI 回来的 arguments 字段原样),
// 调用方(tools.Dispatcher)自行 json.Unmarshal 到对应 tool 的入参结构。
type ToolCall struct {
	ID            string
	Name          string
	ArgumentsJSON string
}

// Usage 单次调用的 token 消耗(写 llm_usage 表 + 计费)。
type Usage struct {
	PromptTokens     int
	CompletionTokens int
}

// Config LLM 构造参数。顶层 config.LLMConfig 映射到这里(main.go 负责)。
type Config struct {
	Provider          string
	Azure             AzureConfig
	RequestTimeoutSec int
}

// AzureConfig Azure OpenAI chat completions 接入参数。
//
// Endpoint 支持两种形态:
//   - v1 surface:".../openai/v1/",走 OpenAI 兼容路径 {endpoint}chat/completions,model 传 deployment 名。
//   - 传统 surface:".../",走 /openai/deployments/{deployment}/chat/completions?api-version=X。
//
// 判断规则:endpoint 路径包含 /openai/v1 即视为 v1 surface(与 embedding 包对齐)。
type AzureConfig struct {
	Endpoint   string
	Deployment string
	APIKey     string
	APIVersion string
}

// ─── Sentinel 错误 ───────────────────────────────────────────────────────────
//
// 与 embedding 包分类对齐,让调用方(orchestrator)可按分类决定:
//   - Auth/Invalid:配置错,应该让人类介入(audit + channel 回错误);不要重试。
//   - RateLimited:provider 侧限流(和 org 预算限流是两回事);短 backoff 后重试。
//   - Server/Network:暂时性故障;不重试(避免反复烧钱),回 channel "暂时回不上来"。

var (
	ErrLLMInvalid     = errors.New("llm: invalid request")
	ErrLLMAuth        = errors.New("llm: authentication failed")
	ErrLLMRateLimited = errors.New("llm: rate limited")
	ErrLLMServer      = errors.New("llm: server error")
	ErrLLMNetwork     = errors.New("llm: network error")
)

// New 按 cfg.Provider 构造 Chat 实例。
//
// 失败场景:
//   - 未知 provider(含空串)→ 返回错误(wrap 名称便于排查)。
//   - provider 自身构造校验失败(azure 缺 endpoint/deployment/key)→ 原始错误。
//
// 与 embedding.New 不同的是:此处**不允许 fake** —— 空 provider 也 fatal,
// 强制调用方显式写 "azure" 表达意图。
func New(cfg Config) (Chat, error) {
	switch cfg.Provider {
	case ProviderAzure:
		return newAzure(cfg)
	case "":
		return nil, fmt.Errorf("llm: provider must be set explicitly (no fake fallback)")
	default:
		return nil, fmt.Errorf("llm: unknown provider %q", cfg.Provider)
	}
}
