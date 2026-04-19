// blocks_to_md.go 飞书 docx block 列表 → markdown 字符串。
//
// 为什么要做这层:飞书 block 是树状,但 GetDocxBlocks 返回扁平顺序(按 DFS 前序),
// parent_id 串起结构。我们把它还原成 markdown,下游的 chunker(markdown_structured)就能
// 按 heading 切、parent_chunk 等特性自然生效 —— feishu 源天然对齐 T1.3 的结构化切分。
//
// 实现策略:只处理 MVP 最常见的 9 种 block_type:
//   - Page (1) 根节点,不输出,只递归子
//   - Text (2) 段落,inline 拼回 markdown
//   - Heading1-9 (3-11) → #, ##, ..., ######(markdown 最多 6 级,7-9 降级成 6)
//   - Bullet (12) → "- " 无序列表
//   - Ordered (13) → "1. " 有序列表
//   - Code (14) → ```lang\n...\n```
//   - Quote (15) → "> "
//   - Todo (17) → "- [ ] " / "- [x] "
//   - Divider (22) → "---"
//
// 其他 block type(table / image / callout / embed)先忽略,留 TODO 按需扩。
// Inline 样式(bold / italic / link / code span)暂时只识别 bold 和 link,其他落成纯文本。
package feishu

import (
	"encoding/json"
	"fmt"
	"strings"
)

// 飞书 block_type 枚举。值来自飞书 OpenAPI 文档,和 DocxBlock 里的 JSON 字段名对应。
const (
	blockTypePage      = 1
	blockTypeText      = 2
	blockTypeHeading1  = 3
	blockTypeHeading2  = 4
	blockTypeHeading3  = 5
	blockTypeHeading4  = 6
	blockTypeHeading5  = 7
	blockTypeHeading6  = 8
	blockTypeHeading7  = 9
	blockTypeHeading8  = 10
	blockTypeHeading9  = 11
	blockTypeBullet    = 12
	blockTypeOrdered   = 13
	blockTypeCode      = 14
	blockTypeQuote     = 15
	blockTypeTodo      = 17
	blockTypeDivider   = 22
	blockTypeCallout   = 19
	blockTypeTable     = 31
	blockTypeImage     = 27
)

// blockText 一个飞书 block 里 text/heading/list 节点的 inline 层结构。所有文本 block
// 都是 `{elements: [...], style: {...}}` 的形状,这里用统一类型接住。
type blockText struct {
	Elements []inlineElement `json:"elements"`
	Style    blockStyle      `json:"style"`
}

// inlineElement 文本块里的单个 inline 元素。飞书支持多种类型,MVP 只识别 text_run(带样式)
// 和 link(url);其他 (mention_user / mention_doc / equation / file 等)先降级为纯文本或标记。
type inlineElement struct {
	TextRun *struct {
		Content      string `json:"content"`
		TextElementStyle struct {
			Bold      bool   `json:"bold,omitempty"`
			Italic    bool   `json:"italic,omitempty"`
			InlineCode bool  `json:"inline_code,omitempty"`
			Strikethrough bool `json:"strikethrough,omitempty"`
			Link      *struct {
				URL string `json:"url"`
			} `json:"link,omitempty"`
		} `json:"text_element_style,omitempty"`
	} `json:"text_run,omitempty"`

	// MentionUser / MentionDoc 等其他类型忽略或按需转成 `@user` / `[文档](url)`。
}

// blockStyle 列表项 / 段落的样式层。MVP 只用 language(code block 用),其他忽略。
type blockStyle struct {
	Language string `json:"language,omitempty"`
	Done     bool   `json:"done,omitempty"` // todo 的勾选状态
}

// BlocksToMarkdown 主入口:扁平 blocks 列表 → markdown 字符串。
//
// 算法:
//  1. 建 parent_id → []child_blocks 映射(保持原顺序)
//  2. 找到 Page (block_type=1) 根节点,DFS 遍历子
//  3. 每个 block 按 block_type 分派到对应 renderer(heading / paragraph / list / code / ...)
//  4. 列表 block 按缩进表达嵌套(markdown 的 "  - " 两空格一层)
//
// 错误处理:未知 block_type 不是错,记个 log-style 注释 + 跳过子树(保守策略,宁少不错)。
func BlocksToMarkdown(blocks []DocxBlock) string {
	if len(blocks) == 0 {
		return ""
	}

	// 建父子映射。飞书保证顺序一致,这里只是按 parent_id 分组方便 DFS。
	childrenByParent := make(map[string][]DocxBlock, len(blocks))
	var root *DocxBlock
	for i := range blocks {
		b := &blocks[i]
		if b.BlockType == blockTypePage {
			root = b
			continue
		}
		childrenByParent[b.ParentID] = append(childrenByParent[b.ParentID], *b)
	}
	if root == nil {
		// 没 Page 根:文档非法或 API 异常。降级 —— 把所有 block 按顺序渲染,靠 parent 关系还原层级。
		// 找一个"没 parent 对应真 block"的起点当伪根。
		return renderFlat(blocks, childrenByParent)
	}

	var sb strings.Builder
	for _, child := range childrenByParent[root.BlockID] {
		renderBlock(&sb, child, childrenByParent, 0)
	}
	return strings.TrimSpace(sb.String())
}

// renderFlat 兜底路径:无 Page 根时按给定顺序整体渲染。indent=0 固定。
func renderFlat(blocks []DocxBlock, children map[string][]DocxBlock) string {
	var sb strings.Builder
	for _, b := range blocks {
		if b.BlockType == blockTypePage {
			continue
		}
		renderBlock(&sb, b, children, 0)
	}
	return strings.TrimSpace(sb.String())
}

// renderBlock 把单个 block 输出到 sb。depth 用于列表缩进。
func renderBlock(sb *strings.Builder, b DocxBlock, children map[string][]DocxBlock, depth int) {
	switch b.BlockType {
	case blockTypeText:
		txt := extractText(b.Text)
		if txt != "" {
			sb.WriteString(txt)
			sb.WriteString("\n\n")
		}
	case blockTypeHeading1, blockTypeHeading2, blockTypeHeading3,
		blockTypeHeading4, blockTypeHeading5, blockTypeHeading6,
		blockTypeHeading7, blockTypeHeading8, blockTypeHeading9:
		// 飞书 heading 1-9,markdown 最多 6 级。7-9 保底到 6 级。
		level := b.BlockType - blockTypeHeading1 + 1
		if level > 6 {
			level = 6
		}
		var raw json.RawMessage
		switch b.BlockType {
		case blockTypeHeading1:
			raw = b.Heading1
		case blockTypeHeading2:
			raw = b.Heading2
		case blockTypeHeading3:
			raw = b.Heading3
		case blockTypeHeading4:
			raw = b.Heading4
		case blockTypeHeading5:
			raw = b.Heading5
		case blockTypeHeading6, blockTypeHeading7, blockTypeHeading8, blockTypeHeading9:
			raw = b.Heading6 // 7-9 用不到单独字段,飞书多半也放在 Heading6 下;拿不到就空
		}
		txt := extractText(raw)
		if txt != "" {
			sb.WriteString(strings.Repeat("#", level))
			sb.WriteByte(' ')
			sb.WriteString(txt)
			sb.WriteString("\n\n")
		}
	case blockTypeBullet:
		renderListItem(sb, b.Bullet, "- ", depth)
	case blockTypeOrdered:
		renderListItem(sb, b.Ordered, "1. ", depth)
	case blockTypeCode:
		renderCode(sb, b.Code)
	case blockTypeQuote:
		txt := extractText(b.Quote)
		if txt != "" {
			sb.WriteString("> ")
			sb.WriteString(txt)
			sb.WriteString("\n\n")
		}
	case blockTypeTodo:
		renderTodo(sb, b.Todo, depth)
	case blockTypeDivider:
		sb.WriteString("---\n\n")
	case blockTypeCallout:
		// Callout 内容本身是 text block,飞书会把子 block 渲染进来 —— 先原样递归子即可。
		// MVP 不做特殊语法(> [!NOTE])。
	case blockTypeTable, blockTypeImage:
		// TODO: 表格用 | markdown 表达,图片用 ![]() + 飞书图片链接(需要额外 API 拿 URL)。
		// 当前降级:输出占位,chunker 不会误解到段落里。
		sb.WriteString(fmt.Sprintf("[飞书 %s 内容,待支持]\n\n", blockTypeName(b.BlockType)))
	default:
		// 未知 block_type:不报错、不输出,保守跳过。
	}

	// 递归子 block。列表 / quote / callout 的嵌套子 block 要加缩进。
	nextDepth := depth
	if b.BlockType == blockTypeBullet || b.BlockType == blockTypeOrdered || b.BlockType == blockTypeTodo {
		nextDepth = depth + 1
	}
	for _, child := range children[b.BlockID] {
		renderBlock(sb, child, children, nextDepth)
	}
}

// renderListItem 输出一行列表项(带缩进前缀)。inline 内容按 text_run 拼。
func renderListItem(sb *strings.Builder, raw json.RawMessage, marker string, depth int) {
	txt := extractText(raw)
	if txt == "" {
		return
	}
	sb.WriteString(strings.Repeat("  ", depth))
	sb.WriteString(marker)
	sb.WriteString(txt)
	sb.WriteByte('\n')
}

// renderTodo "- [ ] foo" / "- [x] foo"。Style.Done 控制勾选。
func renderTodo(sb *strings.Builder, raw json.RawMessage, depth int) {
	if len(raw) == 0 {
		return
	}
	var bt blockText
	if err := json.Unmarshal(raw, &bt); err != nil {
		return
	}
	marker := "- [ ] "
	if bt.Style.Done {
		marker = "- [x] "
	}
	sb.WriteString(strings.Repeat("  ", depth))
	sb.WriteString(marker)
	sb.WriteString(renderInline(bt.Elements))
	sb.WriteByte('\n')
}

// renderCode 代码块。语言从 style.language 读,空则 fenced 不带 lang。
func renderCode(sb *strings.Builder, raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var bt blockText
	if err := json.Unmarshal(raw, &bt); err != nil {
		return
	}
	content := renderInline(bt.Elements)
	if content == "" {
		return
	}
	sb.WriteString("```")
	sb.WriteString(bt.Style.Language)
	sb.WriteByte('\n')
	sb.WriteString(content)
	sb.WriteString("\n```\n\n")
}

// extractText 通用工具:任何带 blockText 形状的 raw JSON 都能抽文本。
// heading / text / quote / bullet / ordered 都符合这个 shape。
func extractText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var bt blockText
	if err := json.Unmarshal(raw, &bt); err != nil {
		return ""
	}
	return renderInline(bt.Elements)
}

// renderInline 把 inline 元素数组合成带 markdown 样式的字符串。
//
// 当前覆盖:纯文本、bold (**)、italic (*)、inline_code (`)、strikethrough (~~)、link ([]())
// 其他样式(下划线、颜色、背景)在 markdown 无标准表达,落纯文本。
func renderInline(elems []inlineElement) string {
	var sb strings.Builder
	for _, e := range elems {
		if e.TextRun == nil {
			continue
		}
		content := e.TextRun.Content
		if content == "" {
			continue
		}
		style := e.TextRun.TextElementStyle

		// 样式嵌套顺序:先 link 包外面,再 bold/italic/inline_code,最后原文。
		if style.Link != nil && style.Link.URL != "" {
			content = fmt.Sprintf("[%s](%s)", content, style.Link.URL)
		}
		if style.InlineCode {
			content = "`" + content + "`"
		}
		if style.Bold {
			content = "**" + content + "**"
		}
		if style.Italic {
			content = "*" + content + "*"
		}
		if style.Strikethrough {
			content = "~~" + content + "~~"
		}
		sb.WriteString(content)
	}
	return sb.String()
}

// blockTypeName debug 用,未知 block_type 的降级提示里用得上。
func blockTypeName(t int) string {
	switch t {
	case blockTypeTable:
		return "table"
	case blockTypeImage:
		return "image"
	case blockTypeCallout:
		return "callout"
	default:
		return fmt.Sprintf("block_type_%d", t)
	}
}
