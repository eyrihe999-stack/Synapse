// bge_tei.go BGE-reranker 走 HuggingFace TEI(text-embeddings-inference)服务的 HTTP 客户端。
//
// TEI 是 HF 官方的高性能 embedding / reranker 服务(Rust 写的,比 Python 版快 2-5x),
// 通过 `docker run ghcr.io/huggingface/text-embeddings-inference --model-id BAAI/bge-reranker-v2-m3`
// 起来后暴露 /rerank 端点:
//
//   POST /rerank
//   { "query": "...", "texts": ["doc1", "doc2", ...], "raw_scores": true }
//   → 200 OK
//   [{ "index": 0, "score": 3.2 }, { "index": 2, "score": 1.1 }, { "index": 1, "score": -0.4 }]
//
// 响应按 score 降序排列,index 指回输入 texts 的原位置。
//
// 选 BGE-reranker-v2-m3:开源、中英双语 SOTA、跨 MTEB / C-MTEB 基准与 Cohere rerank-3 同档。
// 本包只做 HTTP 客户端;模型 / 容器生命周期由 docker-compose 或 ops 负责。
package reranker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// BGEConfig TEI 端点配置。Timeout 建议 2-3s:local GPU 单批 50 条 ~50-150ms,
// 预留余量给网络抖动 + TEI 冷启动 warm-up(第一次请求会加载模型到显存,偶发 >1s)。
type BGEConfig struct {
	// BaseURL TEI 服务入口,如 "http://127.0.0.1:8082"。末尾有无 / 都可,自动归一化。
	BaseURL string
	// Timeout 单次 rerank 请求的超时。0 = 30s(保守兜底,不是推荐值)。
	Timeout time.Duration
	// Name 覆盖默认 "bge_tei" 的实现标识。多实例部署时区分。空 = 用默认值。
	Name string
}

// NewBGETEI 构造 BGE-via-TEI reranker。BaseURL 为空返 error —— 这是配置失误,不是 runtime 问题。
// 客户端 HTTP transport 用 keep-alive,同一 reranker 实例的多次调用复用 TCP。
func NewBGETEI(cfg BGEConfig) (Reranker, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("bge_tei: BaseURL required")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	name := cfg.Name
	if name == "" {
		name = "bge_tei"
	}
	// 单独一个 client 便于将来独立调 transport 参数。默认 http.DefaultTransport 对小流量足够。
	return &bgeTEI{
		baseURL: trimSlash(cfg.BaseURL),
		http:    &http.Client{Timeout: timeout},
		name:    name,
	}, nil
}

type bgeTEI struct {
	baseURL string
	http    *http.Client
	name    string
}

func (b *bgeTEI) Name() string { return b.name }

// Rerank 调用 TEI /rerank。空输入直接返空,不发网络请求。
func (b *bgeTEI) Rerank(ctx context.Context, query string, docs []string) ([]Result, error) {
	if len(docs) == 0 {
		return nil, nil
	}
	reqBody := teiRerankRequest{
		Query:     query,
		Texts:     docs,
		RawScores: true, // 拿原始 score 不做 sigmoid 归一化;上层只看相对排序,归一化反而丢精度。
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		// 几乎不可能:所有字段都是标准 JSON 兼容类型。防御性处理。
		return nil, fmt.Errorf("bge_tei: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/rerank", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("bge_tei: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bge_tei: http do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// TEI 错误响应:{"error": "..."} 或纯文本。读前 512 字节做诊断,不 Unmarshal 免得解析失败叠加。
		buf := make([]byte, 512)
		n, _ := resp.Body.Read(buf)
		return nil, fmt.Errorf("bge_tei: http %d: %s", resp.StatusCode, string(buf[:n]))
	}

	var parsed []teiRerankItem
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("bge_tei: decode response: %w", err)
	}

	out := make([]Result, len(parsed))
	for i, item := range parsed {
		out[i] = Result{Index: item.Index, Score: item.Score}
	}
	return out, nil
}

// teiRerankRequest / teiRerankItem 对应 TEI 的 HTTP schema。字段名和 TEI 版本强耦合;
// TEI 主版本升级时要来这里校对。
type teiRerankRequest struct {
	Query     string   `json:"query"`
	Texts     []string `json:"texts"`
	RawScores bool     `json:"raw_scores"`
}

type teiRerankItem struct {
	Index int     `json:"index"`
	Score float32 `json:"score"`
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
