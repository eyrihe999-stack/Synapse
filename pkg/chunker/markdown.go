// markdown.go markdown_structured profile:按 heading 切段,保留结构信息到 Piece。
//
// 动机:旧的递归分隔符对 markdown 一视同仁,heading 只是一行带 `#` 的文本,常常被切在
// 段落中间 —— 「TradePreCreate API」的段落可能被切到下一 chunk,kw_hit 就会失败。
// 按 heading 切段后,每个 section 是一个语义单元,heading 作为 metadata.heading_path 留下,
// 上层可以做"只搜某个章节"这种过滤(T1.4),LLM 也能看到更完整的上下文。
//
// Phase 1 不做 parent-child 分开入库:所有 section 切出的 piece 都是"叶子",parent_chunk_id
// 保持 NULL,但 heading_path 已经能把上下文携带到 LLM prompt 和 BM25 过滤里。将来想做
// 「小粒度召回,大粒度返回」只需在本文件里再 emit 一个 content_type="section" 的父 Piece,
// 其他层已经准备好了。
//
// 实现选择:用 goldmark 解析 AST 定位 heading 的字节位置,section 内容直接用字节切片取(保留原文
// 格式,包括代码块/表格/列表)。不重构 heading 内部的 inline(强调/链接/代码)—— 只取纯文本
// 作为 heading_path 元素,避免复杂度爆炸。
package chunker

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// MarkdownStructuredProfile Markdown 结构化切分。
type MarkdownStructuredProfile struct {
	cfg Config
	rc  *recursiveChunker // 单个 section 超 MaxChars 时再走递归切分
}

// NewMarkdownStructured 构造 markdown_structured profile。
func NewMarkdownStructured(cfg Config) *MarkdownStructuredProfile {
	return &MarkdownStructuredProfile{cfg: cfg, rc: newRecursive(cfg)}
}

// Name / Version —— version 为 "v2",配合 chunker_version 列标记 "T1.3 结构化切分" 这一代。
func (p *MarkdownStructuredProfile) Name() string    { return "markdown_structured" }
func (p *MarkdownStructuredProfile) Version() string { return "v2" }

// Chunk 切分流程:解析 → section 列表 → 每个 section 内按大小再细分。
// 完全没有 heading 的文档(如纯文本 md)会 fallback 到 plain_text,避免输出一个巨大的 "root" chunk。
func (p *MarkdownStructuredProfile) Chunk(content string) []Piece {
	normalized := normalize(content)
	if normalized == "" {
		return nil
	}
	sections := splitByHeadings([]byte(normalized))
	if len(sections) == 0 || (len(sections) == 1 && len(sections[0].path) == 0) {
		// 无 heading 或全是 preamble —— 结构化 profile 没有意义,退到 plain_text。
		return NewPlainText(p.cfg).Chunk(normalized)
	}

	var out []Piece
	for _, s := range sections {
		contentRunes := utf8.RuneCountInString(s.content)
		if contentRunes == 0 {
			continue
		}
		// 每个 chunk 都带上自己的 heading breadcrumb 作为 content 前缀 —— 这是"contextual chunking"。
		// 动机:T1.3 把文档切得更细后,每个 chunk 的语义信号变弱(原来整篇文档的"主题氛围"被拆散到
		// 多个小 chunk)。给 chunk 加上 heading 层级,让 embedding 既能匹到细节关键词,又能认出
		// 所属的语义域("订单/支付/权益/..."),挽回粗粒度 chunk 才有的"整体相关性"。
		prefix := formatHeadingPrefix(s.path)

		// ≤ MaxChars:一整 section 作为一个 chunk。这是"理想形态"—— 边界由 heading 而非字数决定。
		if utf8.RuneCountInString(prefix)+contentRunes <= p.cfg.MaxChars {
			full := prefix + s.content
			out = append(out, Piece{
				Content:     full,
				TokenCount:  utf8.RuneCountInString(full),
				ContentType: "text",
				HeadingPath: s.path,
				ChunkLevel:  s.level,
			})
			continue
		}
		// 超长 section:用递归分隔符切成多个 sub-chunk,每个都继承同一 heading_path/level。
		// 这里是必要的 trade-off —— 超长 section 要么切成多 chunk(丢整体性),要么做一个超大 chunk
		// (对 embedding 不友好 + BM25 打分稀释)。我们选前者,靠 heading_path 把它们关联回来。
		// 每个 sub-chunk 也加 prefix —— sub-chunk 各自独立进索引,每个都需要自己的语义域锚点。
		raws := p.rc.chunkText(s.content)
		for _, r := range raws {
			full := prefix + r.Content
			out = append(out, Piece{
				Content:     full,
				TokenCount:  utf8.RuneCountInString(full),
				ContentType: "text",
				HeadingPath: s.path,
				ChunkLevel:  s.level,
			})
		}
	}
	return out
}

// formatHeadingPrefix 把 heading_path 渲染成 markdown 标题层级,作为 chunk 内容的前缀。
//
// ["Stripe 模块架构", "预下单"] → "# Stripe 模块架构\n\n## 预下单\n\n"
//
// 为什么用 markdown 头:embedding 模型见过大量 md 训练语料,对 `# Title` 这种结构敏感,
// 比 "path: A > B" 这种自造格式更容易激活正确语义。用空 path 返空串(preamble 走这条)。
func formatHeadingPrefix(path []string) string {
	if len(path) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, h := range path {
		// heading 深度用 path 里的位置决定(i+1),不是原文里的 level —— 这样重组出来的
		// "mini-doc" 是连续的 h1→h2→h3,即使原文里中间跳级(h1→h3)也补齐成 h1→h2。
		sb.WriteString(strings.Repeat("#", i+1))
		sb.WriteByte(' ')
		sb.WriteString(h)
		sb.WriteString("\n\n")
	}
	return sb.String()
}

// ─── heading 扫描 ──────────────────────────────────────────────────────────────

// section 一个 heading 管辖的内容块。
//
// path 是从文档根到本 section 的 heading 文本路径;level = len(path)(1=h1,2=h2…)。
// content 是本 heading 之后到下一同级或更高级 heading 之前的原文字节(已 TrimSpace)。
type section struct {
	path    []string
	level   int16
	content string
}

// headingPos 一个 heading 在源码里的位置和层级。
type headingPos struct {
	level int   // 1~6
	text  string // 纯文本标题
	start int   // heading 这一行在 source 中的起始字节(含 `# `)
	end   int   // heading 这一行结束后的字节(含 trailing \n 的下一位)
}

// splitByHeadings 用 goldmark 定位所有 heading 节点,然后按"当前 heading 结束 → 下一 heading 开始"
// 的字节范围切出 section 内容。这避免了手写 markdown 词法(代码块里的 `#`、setext heading、
// front matter 等都走 goldmark 的标准实现)。
//
// heading 的 inline(如 `## **bold** title`)只提取纯文本作为 path 元素;格式符号丢弃。
func splitByHeadings(source []byte) []section {
	md := goldmark.New()
	doc := md.Parser().Parse(text.NewReader(source))

	var headings []headingPos
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		h, ok := n.(*ast.Heading)
		if !ok {
			return ast.WalkContinue, nil
		}
		lines := h.Lines()
		if lines == nil || lines.Len() == 0 {
			// 罕见:空 heading —— 跳过,避免边界异常。
			return ast.WalkSkipChildren, nil
		}
		first := lines.At(0)
		last := lines.At(lines.Len() - 1)
		// goldmark 的 heading.Lines() 返回的是 heading 内容段(`#` 标记之后),
		// 要拿 heading 整行的字节范围,得从 first.Start 向前回溯到上一个 \n(或文档起点)
		// 把 `#` 前缀包含进来。否则 `#` 会泄露到上一 section 的内容尾部或 preamble。
		lineStart := first.Start
		for lineStart > 0 && source[lineStart-1] != '\n' {
			lineStart--
		}
		// 类似地把 last.Stop 扩展到包含本行结尾的 \n(如果有),避免下一 section content 带上
		// 前导换行(TrimSpace 能兜住但显式处理更干净)。
		lineEnd := last.Stop
		if lineEnd < len(source) && source[lineEnd] == '\n' {
			lineEnd++
		}
		headings = append(headings, headingPos{
			level: h.Level,
			text:  extractInlineText(h, source),
			start: lineStart,
			end:   lineEnd,
		})
		return ast.WalkSkipChildren, nil
	})

	if len(headings) == 0 {
		// 无 heading:整篇作为一个无 path 的"section",调用方会判断后 fallback。
		content := strings.TrimSpace(string(source))
		if content == "" {
			return nil
		}
		return []section{{path: nil, level: 0, content: content}}
	}

	// Heading 出现前的 preamble(如文档开头的简介):作为一个 level=0 的无 path section。
	var out []section
	if headings[0].start > 0 {
		preamble := strings.TrimSpace(string(source[:headings[0].start]))
		if preamble != "" {
			out = append(out, section{path: nil, level: 0, content: preamble})
		}
	}

	// 维护 heading 栈,每个 heading 都产生一个 section(内容从 heading 结束到下一 heading 开始)。
	// 入栈规则:新 heading level L,弹出栈顶所有 level >= L 的,再压入 —— 经典的目录树构造。
	var stack []headingPos
	for i, h := range headings {
		for len(stack) > 0 && stack[len(stack)-1].level >= h.level {
			stack = stack[:len(stack)-1]
		}
		stack = append(stack, h)

		contentStart := h.end
		contentEnd := len(source)
		if i+1 < len(headings) {
			contentEnd = headings[i+1].start
		}
		sectionContent := strings.TrimSpace(string(source[contentStart:contentEnd]))
		if sectionContent == "" {
			// 空 section(heading 下直接是下一 heading,没内容)—— 跳过,避免产生"只有标题"的空 chunk。
			continue
		}

		path := make([]string, len(stack))
		for j, s := range stack {
			path[j] = s.text
		}
		out = append(out, section{
			path:    path,
			level:   int16(len(stack)),
			content: sectionContent,
		})
	}
	return out
}

// extractInlineText 从 heading 节点取纯文本(跳过强调/代码/链接等 inline 节点的装饰)。
// 递归下到所有子节点的 Text 段,用 bytes.Buffer 拼接。
// 不用 goldmark 的 Text() 方法是因为那个方法在多级 inline 嵌套时可能丢字符。
func extractInlineText(parent ast.Node, source []byte) string {
	var buf bytes.Buffer
	for c := parent.FirstChild(); c != nil; c = c.NextSibling() {
		switch n := c.(type) {
		case *ast.Text:
			buf.Write(n.Segment.Value(source))
		default:
			buf.WriteString(extractInlineText(n, source))
		}
	}
	return strings.TrimSpace(buf.String())
}
