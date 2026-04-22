package markdown

import (
	"regexp"
	"strings"

	"github.com/eyrihe999-stack/Synapse/internal/ingestion"
	"github.com/eyrihe999-stack/Synapse/internal/ingestion/chunker/internal/tokens"
)

// headingRE 匹配 ATX heading(# 开头)。不支持 Setext(下划线 === / ---)—— 罕见,遇到走普通 text。
// 匹配组 1 = heading level 的 # 串,组 2 = heading 文本(已去掉 #)。
var headingRE = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*#*\s*$`)

// emitHeading 更新栈 + 产出 heading chunk。
//
// 栈维护:弹掉栈里所有 level >= 当前的 frame,再 push 当前。
// 这保证 "## A → ### B → ## C" 时 C 来的时候先弹掉 A 和 B,栈变成 [C]。
func (s *state) emitHeading(level int16, text string) {
	// 弹
	for len(s.headingStack) > 0 && s.headingStack[len(s.headingStack)-1].Level >= level {
		s.headingStack = s.headingStack[:len(s.headingStack)-1]
	}

	// 先占一个 index,再 push(这样 path 已经反映了本 heading 自身)
	idx := len(s.out)
	s.headingStack = append(s.headingStack, headingFrame{Level: level, Text: text, Index: idx})
	path := s.currentPath()

	// heading chunk 的 Content 用原始 "# heading" 形式,和 text chunk 看着有区分度
	marker := strings.Repeat("#", int(level))
	content := marker + " " + text
	s.out = append(s.out, ingestion.IngestedChunk{
		Content:     content,
		TokenCount:  tokens.Approx(content),
		ContentType: "heading",
		Level:       level,
		HeadingPath: append([]string(nil), path...),
		// heading 自己没有 parent(祖先链只用于 path,不用 ParentIndex)
	})
}

// currentPath 当前 heading 栈的 text 列表(完整祖先链,含最深一级)。
func (s *state) currentPath() []string {
	if len(s.headingStack) == 0 {
		return nil
	}
	out := make([]string, len(s.headingStack))
	for i, f := range s.headingStack {
		out[i] = f.Text
	}
	return out
}
