package markdown

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/eyrihe999-stack/Synapse/internal/ingestion"
	"github.com/eyrihe999-stack/Synapse/internal/ingestion/chunker/internal/tokens"
)

// emitTable 按 Hybrid 策略 emit table chunk(s)。
// 入参 lines 是表格的连续行,第一行是 header,第二行是 divider,其余是数据行。
func (s *state) emitTable(lines []string) {
	whole := strings.Join(lines, "\n")
	if len(whole) <= s.c.maxBytes {
		s.appendTableChunk(whole, "", nil)
		return
	}
	// 超限 → 按数据行切,每段重复 header + divider
	if len(lines) < 3 {
		// 没数据行就是两行或更少,整块拿去 emit
		s.appendTableChunk(whole, "", nil)
		return
	}
	header := lines[0]
	divider := lines[1]
	data := lines[2:]

	budget := s.c.maxBytes - len(header) - len(divider) - 2 // 两个换行
	if budget <= 0 {
		// header 本身就超限,退化成单 chunk 不重复(罕见)
		s.appendTableChunk(whole, "", nil)
		return
	}

	var (
		segments [][]string
		curRows  []string
		curBytes int
	)
	flushSeg := func() {
		if len(curRows) == 0 {
			return
		}
		segments = append(segments, append([]string(nil), curRows...))
		curRows = curRows[:0]
		curBytes = 0
	}
	for _, row := range data {
		need := len(row) + 1 // row + 换行
		if curBytes+need > budget && len(curRows) > 0 {
			flushSeg()
		}
		curRows = append(curRows, row)
		curBytes += need
	}
	flushSeg()

	total := len(segments)
	for i, rows := range segments {
		seg := header + "\n" + divider + "\n" + strings.Join(rows, "\n")
		part := fmt.Sprintf("%d/%d", i+1, total)
		meta := map[string]any{"chunk_part": part}
		s.appendTableChunk(seg, part, meta)
	}
}

func (s *state) appendTableChunk(content, chunkPart string, meta map[string]any) {
	_ = chunkPart
	chunk := ingestion.IngestedChunk{
		Content:     content,
		TokenCount:  tokens.Approx(content),
		ContentType: "table",
		HeadingPath: append([]string(nil), s.currentPath()...),
		Metadata:    meta,
	}
	if len(s.headingStack) > 0 {
		pi := s.headingStack[len(s.headingStack)-1].Index
		chunk.ParentIndex = &pi
	}
	s.out = append(s.out, chunk)
}

// isTableHeaderLine 形如 "| a | b |" 的行视为表格行。
func isTableHeaderLine(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "|") && strings.Count(t, "|") >= 2
}

// dividerRE 形如 "|---|:---:|---:|" 的分隔行。
var dividerRE = regexp.MustCompile(`^\s*\|?\s*(:?-{3,}:?\s*\|\s*)+:?-{3,}:?\s*\|?\s*$`)

func isTableDividerLine(line string) bool {
	return dividerRE.MatchString(line)
}

// findTableEnd 从 start 行开始,吞所有连续 pipe 行(header 已包含)。返回第一个非 pipe 行的 index。
func findTableEnd(lines []string, start int) int {
	i := start
	for i < len(lines) && isTableHeaderLine(lines[i]) {
		i++
	}
	return i
}
