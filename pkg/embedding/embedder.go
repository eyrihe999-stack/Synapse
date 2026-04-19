// Package embedding 文本向量化统一入口。
//
// 对上层(document 模块、未来的 code/bug/image 模块)屏蔽 provider 差异,
// 调用方只看到 Embedder.Embed/Dim/Model 三个方法。当前实现:
//   - fake :离线确定性向量(sha256 → 高斯 → 单位化),dev/test 用。
//   - azure:Azure OpenAI v1 surface,支持 dimensions 参数截断。
//
// 新增 provider 只要实现 Embedder + 往 New() 里加 case,不影响上层。
package embedding

import (
	"context"
	"errors"
	"fmt"
)

// Provider 名称常量,和 config.EmbeddingProviderConfig.Provider 对齐。
const (
	ProviderFake  = "fake"
	ProviderAzure = "azure"
)

// Embedder 向量化器统一接口。
//
// Embed 返回的 [][]float32 与 inputs 顺序一致,长度相等;任一元素长度 = Dim()。
// ctx 负责超时/取消;实现层不允许无限重试,失败直接返回带分类的 error,由调用方决定是否重试。
type Embedder interface {
	Embed(ctx context.Context, inputs []string) ([][]float32, error)
	Dim() int
	Model() string
}

// Config 向量化器构造参数。顶层 config 的 embedding.text 映射到这里。
//
// ModelDim 用于两道校验:(1) factory 在构造时比对 provider 实际维度;
// (2) fake 生成的维度也以此为准,避免 dev 和生产的 schema 对不齐。
type Config struct {
	Provider string
	ModelDim int
	Azure    AzureConfig
}

// AzureConfig Azure OpenAI embedding 接入参数。
//
// Endpoint 支持两种形态:
//   - v1 surface:".../openai/v1/",走 OpenAI 兼容路径 {endpoint}embeddings,model 传 deployment 名。
//   - 传统 surface:".../",走 /openai/deployments/{deployment}/embeddings?api-version=X。
//
// 判断规则:endpoint 路径包含 /openai/v1 即视为 v1 surface。
type AzureConfig struct {
	Endpoint   string
	Deployment string
	APIKey     string
	APIVersion string
}

// ─── Sentinel 错误 ───────────────────────────────────────────────────────────

// 上层按错误分类决定是否重试/降级:
//   - Auth/Invalid:配置错,重试无用,应 fatal 或让 operator 介入。
//   - RateLimited:可退避重试,Azure 会回 Retry-After,此处包好语义即可。
//   - Server/Network:暂时性故障,可 backoff 重试。
var (
	ErrEmbeddingInvalid     = errors.New("embedding: invalid request")
	ErrEmbeddingAuth        = errors.New("embedding: authentication failed")
	ErrEmbeddingRateLimited = errors.New("embedding: rate limited")
	ErrEmbeddingServer      = errors.New("embedding: server error")
	ErrEmbeddingNetwork     = errors.New("embedding: network error")
	ErrEmbeddingDimMismatch = errors.New("embedding: provider returned unexpected dimension")
)

// New 按 cfg.Provider 构造 Embedder 实例。
//
// 失败场景:
//   - 未知 provider → 返回错误(wrap 名称,便于 operator 排查)。
//   - provider 自身构造校验失败(azure 缺 endpoint/key 等) → 原始错误。
//
// 注:New 只做参数校验 + 构造,不发网络请求;第一次真实调用 Embed 时才会打到 provider。
func New(cfg Config) (Embedder, error) {
	if cfg.ModelDim <= 0 {
		return nil, fmt.Errorf("embedding: model_dim must be > 0, got %d", cfg.ModelDim)
	}
	switch cfg.Provider {
	case ProviderFake, "":
		return newFake(cfg.ModelDim), nil
	case ProviderAzure:
		return newAzure(cfg.Azure, cfg.ModelDim)
	default:
		return nil, fmt.Errorf("embedding: unknown provider %q", cfg.Provider)
	}
}
