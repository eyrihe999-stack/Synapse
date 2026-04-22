// Package tokens chunker 共享的 token 估算 + 切点工具。
//
// 设计取舍(决策 3):
//
//   - token 估算用 bytes/4 粗估,和 IngestedChunk.TokenCount 的约定口径一致
//   - 不引入 tiktoken-go 真实 tokenizer:Docker 镜像膨胀 / 启动慢 / 热路径开销,
//     换取的召回精度 <2%,不划算
//   - 误差方向安全:中文 bytes/4 倾向高估 token → 实际切出的 chunk 偏小 → 绝不撞 embed 上限
//
// 切点优先级(决策 2):段落(双换行) > 句子(中英文句号) > UTF-8 rune 硬切。
// 对齐此优先级后,markdown chunker 与 plaintext chunker 共享同一套 SplitByBudget。
package tokens

import (
	"slices"
	"strings"
	"unicode/utf8"
)

// BytesPerTokenApprox 粗估口径。不 export 为常量是让调用方只通过 Approx 函数访问,
// 避免别处直接 `len(s)/4` 散落出去。改口径只改这里。
const bytesPerTokenApprox = 4

// Approx 按 bytes/4 估算 token 数。
// 空串返 0;最小返值不强制为 1(小片段可以合法 = 0 token,emit 时由调用方决定要不要丢)。
func Approx(s string) int {
	return len(s) / bytesPerTokenApprox
}

// MaxChunkBytes 把 "最大 token 数" 翻成 "最大 bytes",供切分逻辑直接拿字节长度比较。
func MaxChunkBytes(maxTokens int) int {
	return maxTokens * bytesPerTokenApprox
}

// SplitByBudget 按 bytes 预算把输入切成多段,保证每段 ≤ maxBytes(除非单片本身就超限 —— 见下方单行兜底)。
//
// 切点优先级:段落(\n\n) > 句子(。/./?/!/?/!) > UTF-8 rune 硬切。
//
// 返回的段之间不重叠,拼起来等于原输入(但首尾 whitespace 会被 trim)。
// 空输入返 nil。
//
// 注意:此函数**只切**,不做任何内容转换。markdown 的 heading / 代码 / 表格识别交给调用方,
// 传进来的应该是一段"纯正文"(没有 heading 行、没有代码围栏,已经去掉)。
func SplitByBudget(s string, maxBytes int) []string {
	s = strings.TrimSpace(s)
	if s == "" || maxBytes <= 0 {
		return nil
	}
	if len(s) <= maxBytes {
		return []string{s}
	}

	// 先按段落边界粗切;不超限的段直接作为一段,超限的段再按句子切,句子再超限按 rune 切。
	paragraphs := splitParagraphs(s)
	var out []string
	var buf strings.Builder

	flush := func() {
		if buf.Len() == 0 {
			return
		}
		out = append(out, strings.TrimSpace(buf.String()))
		buf.Reset()
	}

	for _, p := range paragraphs {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// 单段本身超限 → 先 flush 当前 buf,再把这段切成若干
		if len(p) > maxBytes {
			flush()
			sents := splitSentencesOrRune(p, maxBytes)
			out = append(out, sents...)
			continue
		}
		// 段落可以加入当前 buf 吗?加上会超 → 先 flush 再把这段放新 buf
		// +2 估算段间换行成本
		if buf.Len() > 0 && buf.Len()+2+len(p) > maxBytes {
			flush()
		}
		if buf.Len() > 0 {
			buf.WriteString("\n\n")
		}
		buf.WriteString(p)
	}
	flush()
	return out
}

// splitParagraphs 按双换行切段。输入已假设是 trim 过的非空串。
func splitParagraphs(s string) []string {
	// 归一化换行:\r\n / \r → \n,避免 Windows 换行导致段落识别漏
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	// split 双换行(或更多)
	raw := strings.Split(s, "\n\n")
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		if tp := strings.TrimSpace(p); tp != "" {
			out = append(out, tp)
		}
	}
	return out
}

// splitSentencesOrRune 段本身超限时的降级路径:先按句号切,句仍超就按 rune 硬切。
func splitSentencesOrRune(p string, maxBytes int) []string {
	sents := splitSentences(p)

	var out []string
	var buf strings.Builder
	flush := func() {
		if buf.Len() == 0 {
			return
		}
		out = append(out, strings.TrimSpace(buf.String()))
		buf.Reset()
	}

	for _, sent := range sents {
		sent = strings.TrimSpace(sent)
		if sent == "" {
			continue
		}
		// 单句仍超限 → 直接 rune 硬切
		if len(sent) > maxBytes {
			flush()
			out = append(out, splitByRuneBudget(sent, maxBytes)...)
			continue
		}
		if buf.Len() > 0 && buf.Len()+1+len(sent) > maxBytes {
			flush()
		}
		if buf.Len() > 0 {
			buf.WriteByte(' ')
		}
		buf.WriteString(sent)
	}
	flush()
	return out
}

// sentenceEnders 中英文句末标点,**保留在句子末**(不是 delimiter 吞掉)。
var sentenceEnders = []rune{'。', '!', '?', '.', '!', '?'}

// splitSentences 按句末标点切分,标点保留在前一句尾。
func splitSentences(p string) []string {
	var out []string
	var cur strings.Builder
	for _, r := range p {
		cur.WriteRune(r)
		if isSentenceEnder(r) {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func isSentenceEnder(r rune) bool {
	return slices.Contains(sentenceEnders, r)
}

// splitByRuneBudget 最后兜底:按 rune 边界硬切,保证每段 ≤ maxBytes。
// 单 rune 超 maxBytes 这种病态不会发生(UTF-8 rune 最多 4 字节)。
func splitByRuneBudget(s string, maxBytes int) []string {
	var out []string
	for len(s) > 0 {
		if len(s) <= maxBytes {
			out = append(out, s)
			return out
		}
		// 找 ≤ maxBytes 的 rune 边界
		cut := maxBytes
		// 若 cut 落在 UTF-8 续字节(10xxxxxx)上,往前退到起始字节
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		if cut == 0 {
			// 极端:maxBytes 比单 rune 还小。加一个 rune 即使超一点点也得切,防死循环
			_, size := utf8.DecodeRuneInString(s)
			cut = size
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	return out
}
