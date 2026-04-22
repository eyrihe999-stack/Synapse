package markdown

import (
	"context"
	"strings"
	"testing"

	"github.com/eyrihe999-stack/Synapse/internal/ingestion"
)

func makeDoc(s string) *ingestion.NormalizedDoc {
	return &ingestion.NormalizedDoc{
		OrgID:      1,
		SourceType: ingestion.SourceTypeDocument,
		SourceID:   "test-md",
		MIMEType:   "text/markdown",
		Content:    []byte(s),
	}
}

func chunk(t *testing.T, c *Chunker, s string) []ingestion.IngestedChunk {
	t.Helper()
	out, err := c.Chunk(context.Background(), makeDoc(s))
	if err != nil {
		t.Fatalf("chunk err: %v", err)
	}
	return out
}

// TestEmpty 空 / 全空白返 nil。
func TestEmpty(t *testing.T) {
	c := New(500)
	for _, s := range []string{"", "\n\n", "   "} {
		if got := chunk(t, c, s); got != nil {
			t.Fatalf("%q: want nil, got %+v", s, got)
		}
	}
}

// TestPreambleThenHeadingThenBody 验证整套骨架:preamble / heading / text 的 HeadingPath + ParentIndex。
func TestPreambleThenHeadingThenBody(t *testing.T) {
	c := New(500)
	md := `这是前言,在第一个 heading 之前。

# 架构

本模块概述。

## 数据模型

我们用 PostgreSQL。
`
	out := chunk(t, c, md)
	if len(out) < 4 {
		t.Fatalf("want ≥4 chunks, got %d: %+v", len(out), out)
	}

	// 第 0 个:preamble
	if out[0].ContentType != "preamble" {
		t.Errorf("out[0].ContentType = %q, want preamble", out[0].ContentType)
	}
	if len(out[0].HeadingPath) != 0 {
		t.Errorf("out[0].HeadingPath should be empty, got %v", out[0].HeadingPath)
	}
	if out[0].ParentIndex != nil {
		t.Errorf("preamble should have no parent")
	}

	// 找 h1 "架构"
	var h1Idx, bodyUnderH1Idx, h2Idx, bodyUnderH2Idx int = -1, -1, -1, -1
	for i, ch := range out {
		switch ch.ContentType {
		case "heading":
			if ch.Level == 1 && strings.Contains(ch.Content, "架构") {
				h1Idx = i
			} else if ch.Level == 2 && strings.Contains(ch.Content, "数据模型") {
				h2Idx = i
			}
		case "text":
			if strings.Contains(ch.Content, "本模块概述") {
				bodyUnderH1Idx = i
			}
			if strings.Contains(ch.Content, "PostgreSQL") {
				bodyUnderH2Idx = i
			}
		}
	}
	if h1Idx < 0 || h2Idx < 0 || bodyUnderH1Idx < 0 || bodyUnderH2Idx < 0 {
		t.Fatalf("missing expected chunks. h1=%d h2=%d b1=%d b2=%d out=%+v",
			h1Idx, h2Idx, bodyUnderH1Idx, bodyUnderH2Idx, out)
	}

	// heading 路径
	if !equalPath(out[h1Idx].HeadingPath, []string{"架构"}) {
		t.Errorf("h1 HeadingPath = %v, want [架构]", out[h1Idx].HeadingPath)
	}
	if !equalPath(out[h2Idx].HeadingPath, []string{"架构", "数据模型"}) {
		t.Errorf("h2 HeadingPath = %v, want [架构 数据模型]", out[h2Idx].HeadingPath)
	}

	// body "本模块概述" 的 ParentIndex 指 h1
	if out[bodyUnderH1Idx].ParentIndex == nil || *out[bodyUnderH1Idx].ParentIndex != h1Idx {
		t.Errorf("body under h1 ParentIndex = %v, want %d", out[bodyUnderH1Idx].ParentIndex, h1Idx)
	}
	if !equalPath(out[bodyUnderH1Idx].HeadingPath, []string{"架构"}) {
		t.Errorf("body under h1 HeadingPath = %v", out[bodyUnderH1Idx].HeadingPath)
	}

	// body "PostgreSQL" 的 ParentIndex 指 h2
	if out[bodyUnderH2Idx].ParentIndex == nil || *out[bodyUnderH2Idx].ParentIndex != h2Idx {
		t.Errorf("body under h2 ParentIndex = %v, want %d", out[bodyUnderH2Idx].ParentIndex, h2Idx)
	}
	if !equalPath(out[bodyUnderH2Idx].HeadingPath, []string{"架构", "数据模型"}) {
		t.Errorf("body under h2 HeadingPath = %v", out[bodyUnderH2Idx].HeadingPath)
	}
}

// TestHeadingStackPop 验证栈语义:## → ### → ## 时,第三个 ## 出现应弹到 h1 后再 push。
func TestHeadingStackPop(t *testing.T) {
	c := New(500)
	md := `# A
## B
### C
## D
body under D
`
	out := chunk(t, c, md)
	var dIdx, bodyDIdx int = -1, -1
	for i, ch := range out {
		if ch.ContentType == "heading" && ch.Level == 2 && strings.Contains(ch.Content, "D") {
			dIdx = i
		}
		if ch.ContentType == "text" && strings.Contains(ch.Content, "body under D") {
			bodyDIdx = i
		}
	}
	if dIdx < 0 || bodyDIdx < 0 {
		t.Fatalf("chunks not found: %+v", out)
	}
	if !equalPath(out[dIdx].HeadingPath, []string{"A", "D"}) {
		t.Errorf("D HeadingPath = %v, want [A D]", out[dIdx].HeadingPath)
	}
	if !equalPath(out[bodyDIdx].HeadingPath, []string{"A", "D"}) {
		t.Errorf("body D HeadingPath = %v, want [A D]", out[bodyDIdx].HeadingPath)
	}
}

// TestCodeFenceAtomic 整块代码 fence 保留,ContentType=code,Language 填对。
func TestCodeFenceAtomic(t *testing.T) {
	c := New(500)
	md := "```go\nfunc f() { return 1 }\n```\n"
	out := chunk(t, c, md)
	if len(out) != 1 {
		t.Fatalf("want 1 chunk, got %d: %+v", len(out), out)
	}
	if out[0].ContentType != "code" {
		t.Errorf("ContentType = %q, want code", out[0].ContentType)
	}
	if out[0].Language != "go" {
		t.Errorf("Language = %q, want go", out[0].Language)
	}
	if !strings.Contains(out[0].Content, "func f()") {
		t.Errorf("Content missing code body: %q", out[0].Content)
	}
}

// TestCodeFenceSplitLongByBlankLines 超长代码按空行切,每段 chunk_part 标记。
func TestCodeFenceSplitLongByBlankLines(t *testing.T) {
	c := New(25) // maxBytes=100

	// 三段,每段约 50 bytes,两两都能塞下(含 wrap 开销超限)
	block1 := strings.Repeat("a", 40)
	block2 := strings.Repeat("b", 40)
	block3 := strings.Repeat("c", 40)
	body := block1 + "\n\n" + block2 + "\n\n" + block3
	md := "```py\n" + body + "\n```"

	out := chunk(t, c, md)
	if len(out) < 2 {
		t.Fatalf("want ≥2 code chunks after split, got %d", len(out))
	}

	for i, ch := range out {
		if ch.ContentType != "code" {
			t.Errorf("chunk %d type = %q, want code", i, ch.ContentType)
		}
		if ch.Language != "py" {
			t.Errorf("chunk %d language = %q, want py", i, ch.Language)
		}
		if ch.Metadata == nil || ch.Metadata["chunk_part"] == nil {
			t.Errorf("chunk %d missing chunk_part metadata: %v", i, ch.Metadata)
		}
		if !strings.HasPrefix(ch.Content, "```py") {
			t.Errorf("chunk %d not fence-wrapped: %q", i, ch.Content)
		}
	}
}

// TestTableAtomic 小表格整块保留。
func TestTableAtomic(t *testing.T) {
	c := New(500)
	md := "| A | B |\n|---|---|\n| 1 | 2 |\n| 3 | 4 |\n"
	out := chunk(t, c, md)
	if len(out) != 1 {
		t.Fatalf("want 1 chunk, got %d: %+v", len(out), out)
	}
	if out[0].ContentType != "table" {
		t.Errorf("ContentType = %q, want table", out[0].ContentType)
	}
}

// TestTableSplitRepeatsHeader 超长表格按数据行切,每片必须重复 header + divider。
func TestTableSplitRepeatsHeader(t *testing.T) {
	c := New(25) // maxBytes=100

	// header ~ 10 bytes, divider ~ 10 bytes, 每行 ~ 30 bytes → 3+ 行就超
	header := "| col1 | col2 |"
	divider := "|------|------|"
	var rows []string
	for range 8 {
		rows = append(rows, "| aaaaa | bbbbb |")
	}
	md := header + "\n" + divider + "\n" + strings.Join(rows, "\n")

	out := chunk(t, c, md)
	if len(out) < 2 {
		t.Fatalf("want ≥2 table chunks, got %d", len(out))
	}
	for i, ch := range out {
		if ch.ContentType != "table" {
			t.Errorf("chunk %d type = %q, want table", i, ch.ContentType)
		}
		if !strings.Contains(ch.Content, "col1") || !strings.Contains(ch.Content, "------") {
			t.Errorf("chunk %d missing repeated header: %q", i, ch.Content)
		}
		if ch.Metadata == nil || ch.Metadata["chunk_part"] == nil {
			t.Errorf("chunk %d missing chunk_part metadata", i)
		}
	}
}

// TestHeadingTrimming `## 数据模型  ` 的 trailing 空白应被 trim。
func TestHeadingTrimming(t *testing.T) {
	c := New(500)
	md := "## 数据模型   \n"
	out := chunk(t, c, md)
	if len(out) != 1 {
		t.Fatalf("want 1 chunk, got %d", len(out))
	}
	path := out[0].HeadingPath
	if !equalPath(path, []string{"数据模型"}) {
		t.Errorf("HeadingPath = %v, want [数据模型] (trimmed)", path)
	}
}

// TestLongBodyFallbackToSentences 超长 body 自动按段落/句子切。
func TestLongBodyFallbackToSentences(t *testing.T) {
	c := New(20) // maxBytes=80
	md := "# H\n" + strings.Repeat("x", 50) + "." + strings.Repeat("y", 50) + "."
	out := chunk(t, c, md)
	textChunks := 0
	for _, ch := range out {
		if ch.ContentType == "text" {
			textChunks++
		}
	}
	if textChunks < 2 {
		t.Fatalf("want ≥2 text chunks after split, got %d: %+v", textChunks, out)
	}
}

// equalPath string slice 比较。
func equalPath(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
