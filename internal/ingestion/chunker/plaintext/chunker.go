// Package plaintext 是 SourceType=document 下 MIMEType 非 markdown 的兜底 chunker。
//
// 策略(决策 6):P1 —— 段落优先 + 合并小段 + 切大段,无 overlap。
// 实现上直接把正文丢给 tokens.SplitByBudget 走 paragraph→sentence→rune 优先级。
// 产出的所有 chunk:
//
//	ContentType = "text"
//	HeadingPath = []
//	Level       = 0
//	ParentIndex = nil
package plaintext

import (
	"context"

	"github.com/eyrihe999-stack/Synapse/internal/ingestion"
	"github.com/eyrihe999-stack/Synapse/internal/ingestion/chunker/internal/tokens"
)

const (
	chunkerName    = "plaintext"
	chunkerVersion = "v1"
)

// Chunker plaintext 切分器。无状态,可并发调用。
type Chunker struct {
	maxTokens int
	maxBytes  int
}

// New 构造一个 Chunker。maxTokens 传 ingestion 层约定的 500;传 ≤0 走默认 500。
func New(maxTokens int) *Chunker {
	if maxTokens <= 0 {
		maxTokens = 500
	}
	return &Chunker{
		maxTokens: maxTokens,
		maxBytes:  tokens.MaxChunkBytes(maxTokens),
	}
}

// Name 返回 chunker 的名称标识("plaintext"),用于持久层记录切分策略来源。
func (c *Chunker) Name() string { return chunkerName }

// Version 返回 chunker 的版本标识("v1"),chunker 算法变更时递增;持久层据此判断是否需要重切。
func (c *Chunker) Version() string { return chunkerVersion }

// Chunk 将 NormalizedDoc.Content 按段落 → 句子 → rune 优先级切成若干 IngestedChunk。
//
// 空或全空白 Content 返 (nil, nil),和 ingestion.Chunker 约定一致。
// 每个产出 chunk 的 ChunkerVersion 填本 chunker 的 v1 tag。
//
// 错误场景:当前实现不会返 error(切分纯字符串操作,无外部依赖);签名保留 error 是为和接口对齐、
// 给未来可能引入的 tokenizer/外部 NLP 依赖留位。
func (c *Chunker) Chunk(_ context.Context, doc *ingestion.NormalizedDoc) ([]ingestion.IngestedChunk, error) {
	if doc == nil || len(doc.Content) == 0 {
		return nil, nil
	}
	pieces := tokens.SplitByBudget(string(doc.Content), c.maxBytes)
	if len(pieces) == 0 {
		return nil, nil
	}
	out := make([]ingestion.IngestedChunk, 0, len(pieces))
	for i, p := range pieces {
		out = append(out, ingestion.IngestedChunk{
			Index:          i,
			Content:        p,
			TokenCount:     tokens.Approx(p),
			ContentType:    "text",
			Level:          0,
			HeadingPath:    nil,
			ChunkerVersion: chunkerVersion,
		})
	}
	return out, nil
}
