// Package chunker 把文本拆成便于向量化的小段(chunk)。
//
// 架构(T1.3 起):Profile 接口 + Registry 路由。不同内容类型选不同策略:
//   - plain_text:递归分隔符(本文件内的 recursiveChunker),兼容任何文本。
//   - markdown_structured:按 heading 切段(markdown.go),保留结构信息到 metadata。
// 上层按 mime_type / 文件扩展名通过 Registry.Pick 选 Profile。
//
// 本文件保留的是所有 profile 复用的底层递归分隔符实现。纯函数,不依赖 DB / 网络 / tokenizer。
// TokenCount 是按 rune 数估算的近似值,上层要真实 token 计数可自己接 tiktoken,
// chunker 不内嵌模型特定分词器,保持通用。
package chunker

import (
	"strings"
	"unicode/utf8"
)

// rawChunk 递归分隔符输出的原始切片,仅包内使用(profile.Piece 是对外暴露的结构化形态)。
type rawChunk struct {
	Content    string
	TokenCount int
}

// Config 切块参数。
//
// MaxChars 是单 chunk 最大字符数(rune 数,不是字节);超过按递归分隔符继续拆。
// OverlapChars 是相邻 chunk 的重叠字符数,提供跨边界上下文(防止重要信息正好卡在分界处被搜索漏掉)。
// 两者必须 Overlap < Max,且 Overlap ≥ 0。
type Config struct {
	MaxChars     int
	OverlapChars int
}

// DefaultConfig 给 text-embedding-3-large 输入上限(8192 token)预留充足余量的保守配置。
//
// 英文 1500 chars ≈ 375 token;CJK 1500 runes ≈ 1500 token。两边都远小于 8192,
// 单次 embed 调用把整批 chunk 送去也不会爆 context。
func DefaultConfig() Config {
	return Config{MaxChars: 1500, OverlapChars: 150}
}

// newRecursive 构造递归分隔符切分器。Config 中异常值会被归一化到合理范围,不会 panic:
//
//   - MaxChars ≤ 0 → 回退 DefaultConfig.MaxChars
//   - OverlapChars < 0 → 0
//   - OverlapChars ≥ MaxChars → MaxChars/2(留一半做上下文,避免无限循环)
//
// 返回具体类型(非接口)让 plain_text / markdown profile 可以直接调用 chunkText。
// 对外的 Profile 接口才是上层 API。
func newRecursive(cfg Config) *recursiveChunker {
	def := DefaultConfig()
	if cfg.MaxChars <= 0 {
		cfg.MaxChars = def.MaxChars
	}
	if cfg.OverlapChars < 0 {
		cfg.OverlapChars = 0
	}
	if cfg.OverlapChars >= cfg.MaxChars {
		cfg.OverlapChars = cfg.MaxChars / 2
	}
	return &recursiveChunker{cfg: cfg}
}

// ─── recursive 实现 ────────────────────────────────────────────────────────────

// recursiveSeparators 递归分隔符,按语义粒度从粗到细排列。
//
// 策略:先拿第一个分隔符切;任何一段仍超 MaxChars 就用下一级分隔符再切;
// 末尾 "" 是 fallback —— 直接按字符窗口硬切,保证任何输入都能收敛。
var recursiveSeparators = []string{"\n\n", "\n", ". ", " ", ""}

type recursiveChunker struct {
	cfg Config
}

// chunkText 入口:normalize → recursive split → 贪心合并 → 加 overlap。
// 返回 rawChunk(纯内容 + token 估算),profile 层再把它包成带 metadata 的 Piece。
func (c *recursiveChunker) chunkText(text string) []rawChunk {
	text = normalize(text)
	if text == "" {
		return nil
	}
	// 若整段本身 ≤ MaxChars,单 chunk 直接出,不走合并/重叠路径,省复杂度。
	if utf8.RuneCountInString(text) <= c.cfg.MaxChars {
		return []rawChunk{{Content: text, TokenCount: utf8.RuneCountInString(text)}}
	}
	pieces := splitRecursive(text, recursiveSeparators, c.cfg.MaxChars)
	merged := mergeGreedy(pieces, c.cfg.MaxChars)
	return applyOverlap(merged, c.cfg.OverlapChars)
}

// normalize CRLF → LF、去两端空白,统一输入形态。
// 不去内部多余空格,否则 Markdown 的缩进 / 代码块会被破坏。
func normalize(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.TrimSpace(text)
}

// splitRecursive 递归切分。
//
// 对当前 separators[0] 做 Split,得到若干片:
//   - 片 ≤ maxChars:作为原子块保留;
//   - 片 > maxChars:用 separators[1:] 继续递归。
//
// 末尾空分隔符 "" 意味着硬切 —— 按 rune 窗口截断。
func splitRecursive(text string, separators []string, maxChars int) []string {
	if utf8.RuneCountInString(text) <= maxChars {
		return []string{text}
	}
	// 如果分隔符用完了,硬切(按 rune 保证 UTF-8 不破坏字符)。
	if len(separators) == 0 || separators[0] == "" {
		return hardSplit(text, maxChars)
	}

	sep := separators[0]
	parts := strings.Split(text, sep)
	var out []string
	for i, p := range parts {
		if p == "" {
			continue
		}
		// 把分隔符粘回去,除了最后一段(分隔符在段之间,不在段尾)。
		// 保留分隔符让后续 merge 能重建接近原文的文本。
		if i < len(parts)-1 {
			p += sep
		}
		if utf8.RuneCountInString(p) <= maxChars {
			out = append(out, p)
		} else {
			out = append(out, splitRecursive(p, separators[1:], maxChars)...)
		}
	}
	return out
}

// hardSplit 按 rune 窗口截断。tail-safe:不会在多字节字符中间切断。
func hardSplit(text string, maxChars int) []string {
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}
	var out []string
	for i := 0; i < len(runes); i += maxChars {
		end := min(i+maxChars, len(runes))
		out = append(out, string(runes[i:end]))
	}
	return out
}

// mergeGreedy 贪心合并相邻片:能塞下就继续加,塞不下新起一个 chunk。
//
// 结果:每个 chunk 尽量接近 MaxChars,减少总 chunk 数(降低 embed 调用次数),
// 同时保持片的原始分隔符(因 splitRecursive 把 sep 粘回了片尾),不丢语义。
func mergeGreedy(pieces []string, maxChars int) []string {
	if len(pieces) == 0 {
		return nil
	}
	var out []string
	var cur strings.Builder
	curLen := 0
	for _, p := range pieces {
		pLen := utf8.RuneCountInString(p)
		if curLen+pLen > maxChars && curLen > 0 {
			out = append(out, cur.String())
			cur.Reset()
			curLen = 0
		}
		cur.WriteString(p)
		curLen += pLen
	}
	if curLen > 0 {
		out = append(out, cur.String())
	}
	return out
}

// applyOverlap 相邻 chunk 之间附加 overlapChars 的末尾回溯。
//
// 关键点:先把所有 chunk 做 TrimSpace 得到"可见内容",再从可见内容的尾部取 overlap,
// 避免 splitRecursive 粘回去的分隔符(换行/空格)被当作 overlap 语义。
// 第一个 chunk 不加前缀;overlapChars=0 时退化为纯 trim 输出。
func applyOverlap(chunks []string, overlapChars int) []rawChunk {
	trimmed := make([]string, len(chunks))
	for i, s := range chunks {
		trimmed[i] = strings.TrimSpace(s)
	}
	out := make([]rawChunk, 0, len(trimmed))
	for i, s := range trimmed {
		if s == "" {
			continue
		}
		if i > 0 && overlapChars > 0 {
			prev := []rune(trimmed[i-1])
			start := max(0, len(prev)-overlapChars)
			s = string(prev[start:]) + s
		}
		out = append(out, rawChunk{Content: s, TokenCount: utf8.RuneCountInString(s)})
	}
	return out
}
