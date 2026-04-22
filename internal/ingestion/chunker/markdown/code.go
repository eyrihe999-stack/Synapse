package markdown

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/eyrihe999-stack/Synapse/internal/ingestion"
	"github.com/eyrihe999-stack/Synapse/internal/ingestion/chunker/internal/tokens"
)

// fenceRE 匹配代码围栏的起始/结束行。支持 ``` 和 ~~~ 两种,可带 language tag。
// 匹配组 1 = 围栏字符串 (``` 或 ~~~),组 2 = language。
var fenceRE = regexp.MustCompile("^(```+|~~~+)\\s*([A-Za-z0-9_+\\-.#]*)\\s*$")

// emitCode 按 Hybrid 策略 emit code chunk(s)。
func (s *state) emitCode(language, body string) {
	// 每段都重新 wrap 回 fence + language
	wrap := func(seg string) string {
		if language != "" {
			return "```" + language + "\n" + seg + "\n```"
		}
		return "```\n" + seg + "\n```"
	}

	if body == "" {
		// 空 fence 也产出一个 chunk(小概率但存在)
		s.appendCodeChunk(language, wrap(""), "", nil)
		return
	}

	// 先试整块
	whole := wrap(body)
	if len(whole) <= s.c.maxBytes {
		s.appendCodeChunk(language, whole, "", nil)
		return
	}

	// 超限 → 按空行优先切 body,各段再 wrap
	segments := splitCodeBody(body, s.c.maxBytes-len("```"+language+"\n\n```")) // 给 wrap 预留空间
	total := len(segments)
	for i, seg := range segments {
		wrapped := wrap(seg)
		part := fmt.Sprintf("%d/%d", i+1, total)
		meta := map[string]any{"chunk_part": part}
		s.appendCodeChunk(language, wrapped, part, meta)
	}
}

// appendCodeChunk 产出单条 code chunk。
func (s *state) appendCodeChunk(language, content, chunkPart string, meta map[string]any) {
	_ = chunkPart // 已写入 meta
	chunk := ingestion.IngestedChunk{
		Content:     content,
		TokenCount:  tokens.Approx(content),
		ContentType: "code",
		HeadingPath: append([]string(nil), s.currentPath()...),
		Metadata:    meta,
	}
	if language != "" {
		chunk.Language = language
	}
	if len(s.headingStack) > 0 {
		pi := s.headingStack[len(s.headingStack)-1].Index
		chunk.ParentIndex = &pi
	}
	s.out = append(s.out, chunk)
}

// splitCodeBody 按空行优先 → 任意行切大代码块。每段确保 ≤ budget。
// budget 传入的是 **body** 的预算(wrap 开销已在上游扣过)。
func splitCodeBody(body string, budget int) []string {
	if budget <= 0 {
		budget = 256 // 极端兜底,保证不死循环
	}
	if len(body) <= budget {
		return []string{body}
	}

	// 优先按空行(\n\n)切出若干 block,再按行拼,单 block 超限就按行硬切
	blocks := strings.Split(body, "\n\n")

	var (
		out []string
		buf strings.Builder
	)
	flush := func() {
		if buf.Len() == 0 {
			return
		}
		out = append(out, strings.TrimRight(buf.String(), "\n"))
		buf.Reset()
	}

	for _, blk := range blocks {
		if blk == "" {
			continue
		}
		if len(blk) > budget {
			flush()
			// 单 block 超限 → 按行逐行填
			for _, line := range splitByLines(blk, budget) {
				if buf.Len() > 0 && buf.Len()+1+len(line) > budget {
					flush()
				}
				if buf.Len() > 0 {
					buf.WriteByte('\n')
				}
				buf.WriteString(line)
			}
			continue
		}
		// block 可入 buf?
		if buf.Len() > 0 && buf.Len()+2+len(blk) > budget {
			flush()
		}
		if buf.Len() > 0 {
			buf.WriteString("\n\n")
		}
		buf.WriteString(blk)
	}
	flush()
	return out
}

// splitByLines 逐行切,超单行按 rune 硬切。
func splitByLines(block string, budget int) []string {
	lines := strings.Split(block, "\n")
	var out []string
	for _, line := range lines {
		if len(line) <= budget {
			out = append(out, line)
			continue
		}
		// 行本身超限 → rune 硬切
		out = append(out, splitRune(line, budget)...)
	}
	return out
}

func splitRune(s string, budget int) []string {
	// 复用 tokens 包的逻辑不方便(未 export),这里简单实现 rune 边界切
	var out []string
	for len(s) > 0 {
		if len(s) <= budget {
			out = append(out, s)
			return out
		}
		cut := budget
		for cut > 0 && (s[cut]&0xC0) == 0x80 {
			cut--
		}
		if cut == 0 {
			cut = 1
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	return out
}

// findFenceClose 从 start 开始找和 open fence 相同的闭合行。找不到 → 返 len(lines),
// 把到末尾的全当 code body(宽松语义,避免文档末尾漏写 ``` 时整篇爆)。
func findFenceClose(lines []string, start int, open string) int {
	for i := start; i < len(lines); i++ {
		if m := fenceRE.FindStringSubmatch(lines[i]); m != nil && m[1] == open {
			return i
		}
	}
	return len(lines)
}
