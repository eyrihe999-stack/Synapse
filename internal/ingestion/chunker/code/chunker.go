// chunker.go 通用代码 chunker。按 NormalizedDoc.Language 路由到对应 LanguageBackend,
// 把 ParsedFile 转成 []IngestedChunk。
//
// 流程:
//
//	1. 选 backend(没注册 → 返 ErrUnsupportedLanguage,装配层降级 plaintext)
//	2. backend.Parse(content) → *ParsedFile
//	3. emit preamble chunk(若非空)
//	4. 顺序 emit symbol chunk;超 budget 的 → parent + children
//	5. 回填 Index + ChunkerVersion
package code

import (
	"context"
	"strings"

	"github.com/eyrihe999-stack/Synapse/internal/ingestion"
	"github.com/eyrihe999-stack/Synapse/internal/ingestion/chunker/internal/tokens"
)

const (
	chunkerName    = "code"
	chunkerVersion = "v1"
)

// DefaultMaxTokens 单 chunk token 预算。500 是 embedder 单条 input 的甜区:
// 超过 500 token,3-large 嵌入质量边际下降明显;低于 200,语义信息不足。
const DefaultMaxTokens = 500

// Chunker 满足 ingestion.Chunker。无状态;按 Language 路由 backend。
type Chunker struct {
	maxTokens int
	maxBytes  int
	backends  map[string]LanguageBackend
}

// New 构造。maxTokens ≤ 0 → 走 DefaultMaxTokens。
//
// 至少需要注册一个 backend;调用 Chunk 时若 Language 未匹配会返 ErrUnsupportedLanguage,
// 装配侧的 ChunkerSelector 应在路由前判断"是否有对应 backend"以降级 plaintext。
func New(maxTokens int, backends ...LanguageBackend) *Chunker {
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	m := make(map[string]LanguageBackend, len(backends))
	for _, b := range backends {
		if b == nil {
			continue
		}
		m[b.Language()] = b
	}
	return &Chunker{
		maxTokens: maxTokens,
		maxBytes:  tokens.MaxChunkBytes(maxTokens),
		backends:  m,
	}
}

// Name 见 ingestion.Chunker。
func (c *Chunker) Name() string { return chunkerName }

// Version 见 ingestion.Chunker。
func (c *Chunker) Version() string { return chunkerVersion }

// Supports 装配侧选 chunker 时判断当前 language 是否有 backend 注册。
// 没注册 → 路由层降级 plaintext。
func (c *Chunker) Supports(language string) bool {
	_, ok := c.backends[language]
	return ok
}

// Chunk 见 ingestion.Chunker。
//
// 错误场景:
//   - doc 为 nil / Content 空 → 返 (nil, nil)
//   - Language 未注册 backend → 返 ErrUnsupportedLanguage(装配层应该先 Supports 判断)
//   - backend.Parse 返 error → 透传
//
// 健壮性:backend 对语法错源码应当返"已认得部分 + nil error",chunker 不背锅。
func (c *Chunker) Chunk(ctx context.Context, doc *ingestion.NormalizedDoc) ([]ingestion.IngestedChunk, error) {
	if doc == nil || len(doc.Content) == 0 {
		return nil, nil
	}
	backend, ok := c.backends[doc.Language]
	if !ok {
		return nil, ErrUnsupportedLanguage
	}

	parsed, err := backend.Parse(ctx, doc.Content)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	if parsed == nil {
		return nil, nil
	}

	out := make([]ingestion.IngestedChunk, 0, 1+len(parsed.Symbols))

	// preamble
	if pre := strings.TrimRight(parsed.Preamble, "\n"); pre != "" {
		out = append(out, ingestion.IngestedChunk{
			Content:     pre,
			TokenCount:  tokens.Approx(pre),
			ContentType: "preamble",
			Level:       0,
			Language:    doc.Language,
			LineStart:   parsed.PreambleLineStart,
			LineEnd:     parsed.PreambleLineEnd,
			Metadata:    metadataFor("", "", doc.Language, parsed.PreambleLineStart, parsed.PreambleLineEnd),
		})
	}

	// symbols(超 budget → parent-child)
	for _, sym := range parsed.Symbols {
		body := strings.TrimRight(sym.Body, "\n")
		if body == "" {
			continue
		}
		if len(body) <= c.maxBytes {
			out = append(out, c.symbolChunk(sym, body, doc.Language, nil))
			continue
		}
		// 超 budget:parent + children
		out = c.appendBigSymbol(out, sym, body, doc.Language)
	}

	// 回填 Index + ChunkerVersion
	for i := range out {
		out[i].Index = i
		out[i].ChunkerVersion = chunkerVersion
	}
	return out, nil
}

// symbolChunk 把 Symbol 转成单个 IngestedChunk。parentIdx 非 nil 时填 ParentIndex。
func (c *Chunker) symbolChunk(sym Symbol, body, language string, parentIdx *int) ingestion.IngestedChunk {
	return ingestion.IngestedChunk{
		Content:     body,
		TokenCount:  tokens.Approx(body),
		ContentType: sym.Kind,
		Level:       1,
		SymbolName:  sym.Name,
		Signature:   sym.Signature,
		Language:    language,
		LineStart:   sym.LineStart,
		LineEnd:     sym.LineEnd,
		ParentIndex: parentIdx,
		Metadata:    metadataFor(sym.Name, sym.Signature, language, sym.LineStart, sym.LineEnd),
	}
}

// appendBigSymbol 大符号 parent-child 拆分。
//
//   - parent: 签名 + 头 N 行 + "// ..."(让 LLM 检索命中 child 时,顺 ParentIndex 拿到 parent 看签名)
//   - children: 函数体按 tokens.SplitByBudget 切;每段一个 chunk,ParentIndex 指 parent
//
// children 的 LineStart/LineEnd 不做精确切分(行号近似为 sym 的整段范围),
// 因为 SplitByBudget 走段落 / 句子 / rune 切点不感知行号 —— 后续 PR-D 检索若需要精确行号,
// 在 backend 层产出"已按行切好的子节点"即可,framework 不强约。
func (c *Chunker) appendBigSymbol(out []ingestion.IngestedChunk, sym Symbol, body, language string) []ingestion.IngestedChunk {
	parentIdx := len(out)
	parentBody := buildParentSummary(sym, body, c.maxBytes/2) // parent 占 budget 一半,留空间给签名 + 标记
	out = append(out, ingestion.IngestedChunk{
		Content:     parentBody,
		TokenCount:  tokens.Approx(parentBody),
		ContentType: sym.Kind,
		Level:       1,
		SymbolName:  sym.Name,
		Signature:   sym.Signature,
		Language:    language,
		LineStart:   sym.LineStart,
		LineEnd:     sym.LineEnd,
		Metadata:    metadataFor(sym.Name, sym.Signature, language, sym.LineStart, sym.LineEnd),
	})

	parts := tokens.SplitByBudget(body, c.maxBytes)
	pi := parentIdx
	for _, p := range parts {
		out = append(out, ingestion.IngestedChunk{
			Content:     p,
			TokenCount:  tokens.Approx(p),
			ContentType: sym.Kind,
			Level:       2,
			SymbolName:  sym.Name,
			Signature:   sym.Signature,
			Language:    language,
			LineStart:   sym.LineStart,
			LineEnd:     sym.LineEnd,
			ParentIndex: &pi,
			Metadata:    metadataFor(sym.Name, sym.Signature, language, sym.LineStart, sym.LineEnd),
		})
	}
	return out
}

// buildParentSummary 大符号 parent chunk 的内容:签名(若有)+ 头几行 + "..." 提示。
// budget 是字节预算,留出空间给截断标记。
func buildParentSummary(sym Symbol, body string, budget int) string {
	const ellipsis = "\n// ... (大符号已拆分,详见后续片段)"
	if budget <= len(ellipsis)+len(sym.Signature)+8 {
		// 极端预算太小:返签名(或 body 头几字节)兜底,不做 ellipsis 拼接
		if sym.Signature != "" {
			return sym.Signature + ellipsis
		}
		return safePrefix(body, budget)
	}
	head := safePrefix(body, budget-len(ellipsis))
	return head + ellipsis
}

// safePrefix 截字节预算前缀,落在 UTF-8 rune 边界上。
func safePrefix(s string, budget int) string {
	if budget <= 0 {
		return ""
	}
	if len(s) <= budget {
		return s
	}
	cut := budget
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut]
}

// metadataFor 把代码元字段塞进 jsonb metadata。
//
// 字段约定(供 PR-D 检索 / GIN 索引使用):
//   - symbol_name / signature / language: 字符串
//   - line_start / line_end: 整数(JSON number)
//
// 空字段不写入,减小 jsonb 体积。
func metadataFor(symbolName, signature, language string, lineStart, lineEnd int) map[string]any {
	m := map[string]any{}
	if symbolName != "" {
		m["symbol_name"] = symbolName
	}
	if signature != "" {
		m["signature"] = signature
	}
	if language != "" {
		m["language"] = language
	}
	if lineStart > 0 {
		m["line_start"] = lineStart
	}
	if lineEnd > 0 {
		m["line_end"] = lineEnd
	}
	if len(m) == 0 {
		return nil
	}
	return m
}
