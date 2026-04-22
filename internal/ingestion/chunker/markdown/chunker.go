// Package markdown 是 SourceType=document 下 MIMEType=text/markdown 的 chunker。
//
// 策略(决策 1-5):
//
//   - preamble(第一个 heading 之前的内容)→ ContentType="preamble", HeadingPath=[], 无 parent
//   - heading 行 → 单独 chunk,ContentType="heading",HeadingPath=完整祖先链含自身
//   - body 正文 → 按段落/句子/rune 切成多个 ContentType="text",
//     HeadingPath 同当前 heading 栈快照,ParentIndex 指向所属 heading chunk 的 Index
//   - 代码块 ≤ 上限原子保留;> 上限按空行/任意行切,每段仍是 "```lang\n...\n```",加 metadata.chunk_part
//   - 表格 ≤ 上限原子保留;> 上限按数据行切,每段必须重复 header 行,加 metadata.chunk_part
//   - 单行超上限按 rune 硬切
//
// 实现上走 line-by-line 扫描 + 状态机,不依赖真 AST parser(AST 成本高,收益低)。
//
// 文件组织(同包 state 方法拆散在多文件,按类目分):
//
//   - chunker.go :类型 / 构造 / Chunk 驱动 / run 主循环 / body / 换行归一化
//   - heading.go :heading 识别 + 栈维护 + 产出
//   - code.go    :代码围栏识别 + split + 产出
//   - table.go   :表格识别 + split + 产出
package markdown

import (
	"context"
	"strings"

	"github.com/eyrihe999-stack/Synapse/internal/ingestion"
	"github.com/eyrihe999-stack/Synapse/internal/ingestion/chunker/internal/tokens"
)

const (
	chunkerName    = "markdown"
	chunkerVersion = "v1"
)

// Chunker markdown 切分器。无状态,可并发调用(每次 Chunk 创建独立 state)。
type Chunker struct {
	maxTokens int
	maxBytes  int
}

// New 构造。maxTokens <= 0 走默认 500。
func New(maxTokens int) *Chunker {
	if maxTokens <= 0 {
		maxTokens = 500
	}
	return &Chunker{
		maxTokens: maxTokens,
		maxBytes:  tokens.MaxChunkBytes(maxTokens),
	}
}

// Name 见 ingestion.Chunker。
func (c *Chunker) Name() string { return chunkerName }

// Version 见 ingestion.Chunker。
func (c *Chunker) Version() string { return chunkerVersion }

// state 一次 Chunk 调用的运行时状态。
type state struct {
	c            *Chunker
	out          []ingestion.IngestedChunk
	headingStack []headingFrame // 当前 heading 祖先链(包含最深一级)
}

// headingFrame 栈元素:记下 heading 的 level + 文本 + 它在 out 里的 index。
// Index 让下游 text chunk 的 ParentIndex 能指对。
type headingFrame struct {
	Level int16
	Text  string
	Index int
}

// Chunk 按 heading / fence / table / 正文做状态机扫描,切成若干 IngestedChunk。
//
// 空或全空白 Content 返 (nil, nil)。
//
// 错误场景:当前实现不会返 error(纯字符串状态机,无外部依赖);签名保留 error 是为和接口对齐、
// 给未来可能引入的 tokenizer 或严格 markdown 校验留位。
func (c *Chunker) Chunk(_ context.Context, doc *ingestion.NormalizedDoc) ([]ingestion.IngestedChunk, error) {
	if doc == nil || len(doc.Content) == 0 {
		return nil, nil
	}
	src := normalizeNewlines(string(doc.Content))
	if strings.TrimSpace(src) == "" {
		return nil, nil
	}

	s := &state{c: c}
	s.run(src)
	if len(s.out) == 0 {
		return nil, nil
	}
	// 所有 chunk 的 Index 按 out 里顺序,0-based,填回。
	for i := range s.out {
		s.out[i].Index = i
		s.out[i].ChunkerVersion = chunkerVersion
	}
	return s.out, nil
}

// run 扫一遍文本,分层 emit。
//
// 行分类(互斥):
//  1. heading  → flush 当前 body buffer,emit heading chunk,更新栈
//  2. 代码围栏 → 进入 code 模式,收集到下一个围栏结束,emit code chunk(s)
//  3. 表格行   → 进入 table 模式,收集连续的 pipe 行,到非 pipe 行结束,emit table chunk(s)
//  4. 其他     → 累积进 body buffer
func (s *state) run(src string) {
	lines := strings.Split(src, "\n")

	var bodyBuf []string // 当前累积的正文行(不含 heading / code / table)

	flushBody := func() {
		body := strings.TrimSpace(strings.Join(bodyBuf, "\n"))
		bodyBuf = bodyBuf[:0]
		if body == "" {
			return
		}
		s.emitBody(body)
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]

		// 代码围栏?吞到闭合围栏为止(闭合围栏可以和开启对不上大小,但大多数 md 里一样)。
		if m := fenceRE.FindStringSubmatch(line); m != nil {
			flushBody()
			end := findFenceClose(lines, i+1, m[1])
			code := ""
			if end > i+1 {
				code = strings.Join(lines[i+1:end], "\n")
			}
			s.emitCode(m[2], code)
			i = end // 跳到闭合行(for 循环再 ++)
			continue
		}

		// heading?
		if m := headingRE.FindStringSubmatch(line); m != nil {
			flushBody()
			level := int16(len(m[1]))
			text := strings.TrimSpace(m[2])
			s.emitHeading(level, text)
			continue
		}

		// 表格起点:当前行以 | 开头且下一行是 pipe 分隔行(--- 或 :---:)?
		if isTableHeaderLine(line) && i+1 < len(lines) && isTableDividerLine(lines[i+1]) {
			flushBody()
			end := findTableEnd(lines, i)
			s.emitTable(lines[i:end])
			i = end - 1
			continue
		}

		bodyBuf = append(bodyBuf, line)
	}
	flushBody()
}

// ─── body (text + preamble) ────────────────────────────────────────────────

// emitBody 把累积的正文按 budget 切成若干 chunk。
//
// 如果当前还没见过 heading → ContentType="preamble", 无 parent。
// 否则 → ContentType="text",ParentIndex 指栈顶 heading chunk。
func (s *state) emitBody(body string) {
	pieces := tokens.SplitByBudget(body, s.c.maxBytes)
	var (
		contentType = "text"
		parentIdx   *int
		path        []string
	)
	if len(s.headingStack) == 0 {
		contentType = "preamble"
		// preamble 无 parent,HeadingPath 空
	} else {
		pi := s.headingStack[len(s.headingStack)-1].Index
		parentIdx = &pi
		path = s.currentPath()
	}

	for _, p := range pieces {
		s.out = append(s.out, ingestion.IngestedChunk{
			Content:     p,
			TokenCount:  tokens.Approx(p),
			ContentType: contentType,
			HeadingPath: append([]string(nil), path...),
			ParentIndex: parentIdx,
		})
	}
}

// ─── utils ──────────────────────────────────────────────────────────────────

// normalizeNewlines \r\n / \r → \n。Windows 换行 + 旧 Mac 换行统一归一化。
func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s
}
