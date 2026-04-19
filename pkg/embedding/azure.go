package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// azureHTTPTimeout Embed 单次请求默认超时。
//
// Azure text-embedding 通常 <1s,批量下 ≤3s,给 20s 头足够覆盖尾延迟 + TLS 抖动;
// 真正需要严格超时的调用方应自己 ctx.WithTimeout,此处只是兜底,不取代 ctx。
const azureHTTPTimeout = 20 * time.Second

// azure 是 Azure OpenAI 的 embedding provider 实现。
//
// Endpoint 处理:路径含 "/openai/v1" 走 v1 surface(OpenAI 兼容);否则走传统 surface,
// 拼 /openai/deployments/{deployment}/embeddings?api-version=X。两种路径均用 api-key 头鉴权。
type azure struct {
	cfg      AzureConfig
	dim      int
	url      string
	client   *http.Client
	modelTag string // 落库用:deployment + "@azure",避免和 fake 撞名
}

// newAzure 校验必填参数后构造实例。
//
// 失败场景:缺 Endpoint / Deployment / APIKey → 返回错误(wrap ErrEmbeddingInvalid)。
// 注意:此处不发请求,第一次 Embed 时才会打 Azure;构造期验证只看参数完整性。
func newAzure(cfg AzureConfig, dim int) (*azure, error) {
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("azure endpoint required: %w", ErrEmbeddingInvalid)
	}
	if cfg.Deployment == "" {
		return nil, fmt.Errorf("azure deployment required: %w", ErrEmbeddingInvalid)
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("azure api_key required: %w", ErrEmbeddingInvalid)
	}
	return &azure{
		cfg:      cfg,
		dim:      dim,
		url:      buildAzureURL(cfg),
		client:   &http.Client{Timeout: azureHTTPTimeout},
		modelTag: cfg.Deployment + "@azure",
	}, nil
}

func (a *azure) Dim() int      { return a.dim }
func (a *azure) Model() string { return a.modelTag }

// azureEmbedRequest Azure OpenAI Embeddings 请求体(v1 + 传统 surface 同构)。
//
// Dimensions 让 text-embedding-3-large 直接输出 1536 维 Matryoshka 截断,
// 省掉本地降维代价,和 schema 的 vector(1536) 锁死。
type azureEmbedRequest struct {
	Input      []string `json:"input"`
	Model      string   `json:"model"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type azureEmbedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Model string `json:"model"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

type azureErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Code    string `json:"code"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Embed 批量把 inputs 送去 Azure,返回同序向量。
//
// 失败场景:
//   - 空 inputs → 直接返回空切片,不发请求。
//   - 请求构造失败 → wrap ErrEmbeddingInvalid。
//   - 网络错误 → wrap ErrEmbeddingNetwork(调用方可退避重试)。
//   - 401/403 → wrap ErrEmbeddingAuth。
//   - 400/422 → wrap ErrEmbeddingInvalid(payload/参数不合法,重试无用)。
//   - 429 → wrap ErrEmbeddingRateLimited。
//   - 5xx/其他 → wrap ErrEmbeddingServer。
//   - 返回维度 ≠ Dim() → wrap ErrEmbeddingDimMismatch(通常是 deployment 配错)。
//
// 本层不做重试/退避:失败交上层按错误分类决定是否重排队。
func (a *azure) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return [][]float32{}, nil
	}
	body := azureEmbedRequest{
		Input:      inputs,
		Model:      a.cfg.Deployment,
		Dimensions: a.dim,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w: %w", err, ErrEmbeddingInvalid)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("build request: %w: %w", err, ErrEmbeddingInvalid)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("api-key", a.cfg.APIKey)

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http do: %w: %w", err, ErrEmbeddingNetwork)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, classifyAzureError(resp)
	}

	var parsed azureEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode response: %w: %w", err, ErrEmbeddingServer)
	}
	if len(parsed.Data) != len(inputs) {
		return nil, fmt.Errorf("azure: expected %d vectors, got %d: %w", len(inputs), len(parsed.Data), ErrEmbeddingServer)
	}

	// 防御性排序:规范说 index 升序,但按 index 取再保一次,避免个别 gateway 改顺序。
	sort.SliceStable(parsed.Data, func(i, j int) bool { return parsed.Data[i].Index < parsed.Data[j].Index })

	out := make([][]float32, len(inputs))
	for i, d := range parsed.Data {
		if len(d.Embedding) != a.dim {
			return nil, fmt.Errorf("azure: vector %d has dim %d, expected %d: %w", i, len(d.Embedding), a.dim, ErrEmbeddingDimMismatch)
		}
		out[i] = d.Embedding
	}
	return out, nil
}

// classifyAzureError 把非 200 响应映射为带分类的 error。
//
// 原始 body 最多 4KB 回贴到 error message 里(方便 operator 直接看),超出截断;
// 网络已断/body 读失败当 server error 算 —— 头都拿到了至少不是 DNS 类问题。
func classifyAzureError(resp *http.Response) error {
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	var parsed azureErrorResponse
	_ = json.Unmarshal(bodyBytes, &parsed)
	msg := strings.TrimSpace(parsed.Error.Message)
	if msg == "" {
		msg = strings.TrimSpace(string(bodyBytes))
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("azure %d: %s: %w", resp.StatusCode, msg, ErrEmbeddingAuth)
	case resp.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("azure %d: %s: %w", resp.StatusCode, msg, ErrEmbeddingRateLimited)
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return fmt.Errorf("azure %d: %s: %w", resp.StatusCode, msg, ErrEmbeddingInvalid)
	default:
		return fmt.Errorf("azure %d: %s: %w", resp.StatusCode, msg, ErrEmbeddingServer)
	}
}

// buildAzureURL 按 endpoint 形态决定走哪种 surface。
//
// 用户提供的 dev endpoint 形如 "https://{res}.openai.azure.com/openai/v1/",
// 含 "/openai/v1" 段即走 OpenAI 兼容路径(model 传 deployment);否则拼传统 path + api-version。
// TrimRight "/" 让两边拼接不会出现双斜杠。
func buildAzureURL(cfg AzureConfig) string {
	base := strings.TrimRight(cfg.Endpoint, "/")
	if strings.Contains(base, "/openai/v1") {
		return base + "/embeddings"
	}
	return fmt.Sprintf("%s/openai/deployments/%s/embeddings?api-version=%s", base, cfg.Deployment, cfg.APIVersion)
}
