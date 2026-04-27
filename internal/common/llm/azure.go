package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// azureDefaultHTTPTimeout 单次 chat completions 请求的默认超时(构造时从 Config 拿,兜底 60s)。
//
// Azure chat 比 embedding 慢得多:gpt-5.4 长回复 + tool-loop 单轮可能 5~30s,
// 这里给个稳妥上限,调用方想更紧需自己 ctx.WithTimeout。
const azureDefaultHTTPTimeout = 60 * time.Second

// azure 是 Azure OpenAI 的 chat completions provider 实现。
//
// Endpoint 处理与 embedding 包一致:路径含 "/openai/v1" → v1 surface(OpenAI 兼容);
// 否则走传统 surface,拼 /openai/deployments/{deployment}/chat/completions?api-version=X。
type azure struct {
	cfg      AzureConfig
	url      string
	client   *http.Client
	modelTag string // 落 llm_usage.model 用:deployment + "@azure"
}

// newAzure 校验必填参数后构造实例(构造期不发请求)。
func newAzure(cfg Config) (*azure, error) {
	if cfg.Azure.Endpoint == "" {
		return nil, fmt.Errorf("azure endpoint required: %w", ErrLLMInvalid)
	}
	if cfg.Azure.Deployment == "" {
		return nil, fmt.Errorf("azure deployment required: %w", ErrLLMInvalid)
	}
	if cfg.Azure.APIKey == "" {
		return nil, fmt.Errorf("azure api_key required: %w", ErrLLMInvalid)
	}
	timeout := azureDefaultHTTPTimeout
	if cfg.RequestTimeoutSec > 0 {
		timeout = time.Duration(cfg.RequestTimeoutSec) * time.Second
	}
	return &azure{
		cfg:      cfg.Azure,
		url:      buildAzureChatURL(cfg.Azure),
		client:   &http.Client{Timeout: timeout},
		modelTag: cfg.Azure.Deployment + "@azure",
	}, nil
}

func (a *azure) Model() string { return a.modelTag }

// ─── 请求 / 响应结构体(OpenAI chat completions 协议) ───────────────────────

// azureChatRequest chat completions 请求体。
//
// 字段兼容性注意:
//   - MaxCompletionTokens 用 OpenAI o1 / gpt-5 系列的新字段名(2024-09 起);传统 gpt-4o
//     也接受新字段。gpt-5.4 **只认** max_completion_tokens,传旧 max_tokens 会 400
//     ("Unsupported parameter")。为统一写新字段,旧模型若不识别会忽略(不会报错)。
//   - Temperature 写 0 时,OpenAI 侧不会当作"明确传入 0"对待(omitempty),走 provider
//     默认。调用方想固定 0 需要传个极小非零值(如 1e-6)或后续扩 *float32。
type azureChatRequest struct {
	Model               string             `json:"model"`
	Messages            []azureChatMessage `json:"messages"`
	Tools               []azureChatTool    `json:"tools,omitempty"`
	ToolChoice          string             `json:"tool_choice,omitempty"` // "auto" / "none" / 具体 tool
	Temperature         float32            `json:"temperature,omitempty"`
	MaxCompletionTokens int                `json:"max_completion_tokens,omitempty"`
}

// azureChatMessage 一条 chat 消息。
//
// 字段 Content 刻意**不加 omitempty** —— assistant 在有 tool_calls 时常返回空字符串 content,
// 后续轮次把这条历史回灌给 provider 时,字段必须显式存在为 "" 才合法;omitempty 会让
// JSON 里直接缺失该字段,OpenAI / Azure 按 null 校验失败 400("expected a string, got null")。
// 其它角色(system/user/tool)的 content 也总是有值,去 omitempty 不影响它们。
type azureChatMessage struct {
	Role       string                 `json:"role"`
	Content    string                 `json:"content"`
	ToolCallID string                 `json:"tool_call_id,omitempty"`
	ToolCalls  []azureChatToolCallOut `json:"tool_calls,omitempty"`
}

// azureChatToolCallOut 历史消息回填时,assistant 消息的 tool_calls 字段。
// 这里只在序列化时用;我们的上层 Message 结构不直接暴露 —— 当前 PR 的 handler
// 在一次调用内消化完所有 tool-loop,不需要把 assistant 的 tool_calls 历史回填
// 给 LLM;未来如果要跨 stream / 断点续聊,再扩展。
type azureChatToolCallOut struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"` // "function"
	Function azureChatFuncOut `json:"function"`
}

type azureChatFuncOut struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // OpenAI 规定是字符串形态的 JSON
}

type azureChatTool struct {
	Type     string          `json:"type"` // "function"
	Function azureChatFuncIn `json:"function"`
}

type azureChatFuncIn struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type azureChatResponse struct {
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role      string                  `json:"role"`
			Content   string                  `json:"content"`
			ToolCalls []azureChatToolCallOut  `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type azureErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Code    string `json:"code"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Completions 发送一次 chat completions 请求并返回 assistant 回复 + tool_calls + usage。
func (a *azure) Completions(ctx context.Context, req Request) (*Response, error) {
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("empty messages: %w", ErrLLMInvalid)
	}

	body := azureChatRequest{
		Model:               a.cfg.Deployment,
		Messages:            toAzureMessages(req.Messages),
		Temperature:         req.Temperature,
		MaxCompletionTokens: req.MaxOutputTokens,
	}
	if len(req.Tools) > 0 {
		body.Tools = toAzureTools(req.Tools)
		body.ToolChoice = "auto"
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w: %w", err, ErrLLMInvalid)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("build request: %w: %w", err, ErrLLMInvalid)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("api-key", a.cfg.APIKey)

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http do: %w: %w", err, ErrLLMNetwork)
	}
	//sayso-lint:ignore defer-err
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, classifyAzureChatError(resp)
	}

	var parsed azureChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode response: %w: %w", err, ErrLLMServer)
	}
	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("azure returned zero choices: %w", ErrLLMServer)
	}

	choice := parsed.Choices[0].Message
	out := &Response{
		Content: choice.Content,
		Usage: Usage{
			PromptTokens:     parsed.Usage.PromptTokens,
			CompletionTokens: parsed.Usage.CompletionTokens,
		},
	}
	if len(choice.ToolCalls) > 0 {
		out.ToolCalls = make([]ToolCall, 0, len(choice.ToolCalls))
		for _, tc := range choice.ToolCalls {
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				ID:            tc.ID,
				Name:          tc.Function.Name,
				ArgumentsJSON: tc.Function.Arguments,
			})
		}
	}
	return out, nil
}

func toAzureMessages(msgs []Message) []azureChatMessage {
	out := make([]azureChatMessage, len(msgs))
	for i, m := range msgs {
		out[i] = azureChatMessage{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
		// 回灌 assistant 的 tool_calls —— 保证后续 role=tool 合法
		if len(m.ToolCalls) > 0 {
			calls := make([]azureChatToolCallOut, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				calls = append(calls, azureChatToolCallOut{
					ID:   tc.ID,
					Type: "function",
					Function: azureChatFuncOut{
						Name:      tc.Name,
						Arguments: tc.ArgumentsJSON,
					},
				})
			}
			out[i].ToolCalls = calls
		}
	}
	return out
}

func toAzureTools(tools []ToolDef) []azureChatTool {
	out := make([]azureChatTool, len(tools))
	for i, t := range tools {
		out[i] = azureChatTool{
			Type: "function",
			Function: azureChatFuncIn{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.ParametersJSONSchema,
			},
		}
	}
	return out
}

// classifyAzureChatError 非 200 响应映射为带分类的 error。
// 原始 body 最多 4KB 回贴到 error message(便于 operator 直接看),超出截断。
func classifyAzureChatError(resp *http.Response) error {
	//sayso-lint:ignore err-swallow
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	var parsed azureErrorResponse
	//sayso-lint:ignore err-swallow
	_ = json.Unmarshal(bodyBytes, &parsed)
	msg := strings.TrimSpace(parsed.Error.Message)
	if msg == "" {
		msg = strings.TrimSpace(string(bodyBytes))
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("azure %d: %s: %w", resp.StatusCode, msg, ErrLLMAuth)
	case resp.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("azure %d: %s: %w", resp.StatusCode, msg, ErrLLMRateLimited)
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return fmt.Errorf("azure %d: %s: %w", resp.StatusCode, msg, ErrLLMInvalid)
	default:
		return fmt.Errorf("azure %d: %s: %w", resp.StatusCode, msg, ErrLLMServer)
	}
}

// buildAzureChatURL 按 endpoint 形态决定走哪种 surface。
// 与 embedding.buildAzureURL 共用判定规则,保证同一 endpoint 两种场景路径一致。
func buildAzureChatURL(cfg AzureConfig) string {
	base := strings.TrimRight(cfg.Endpoint, "/")
	if strings.Contains(base, "/openai/v1") {
		return base + "/chat/completions"
	}
	return fmt.Sprintf("%s/openai/deployments/%s/chat/completions?api-version=%s", base, cfg.Deployment, cfg.APIVersion)
}
