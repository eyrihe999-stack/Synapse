package chunker

import (
	"strings"
	"testing"
)

func TestMarkdownStructured_Empty(t *testing.T) {
	p := NewMarkdownStructured(DefaultConfig())
	if got := p.Chunk(""); len(got) != 0 {
		t.Errorf("empty → %d pieces, want 0", len(got))
	}
	if got := p.Chunk("   \n\t  "); len(got) != 0 {
		t.Errorf("whitespace-only → %d pieces, want 0", len(got))
	}
}

// 没有 heading 的文档应该 fallback 到 plain_text,而不是产出一个巨型 chunk 或无 path 的奇怪结构。
func TestMarkdownStructured_NoHeadings_FallsBackToPlainText(t *testing.T) {
	p := NewMarkdownStructured(Config{MaxChars: 100, OverlapChars: 0})
	text := strings.Repeat("hello ", 50) // > MaxChars,plain_text 会切多块
	got := p.Chunk(text)
	if len(got) < 2 {
		t.Fatalf("expected plain_text fallback to produce multiple chunks, got %d", len(got))
	}
	for i, piece := range got {
		if piece.ContentType != "text" {
			t.Errorf("piece %d: ContentType=%q, want text (fallback)", i, piece.ContentType)
		}
		if len(piece.HeadingPath) != 0 {
			t.Errorf("piece %d: unexpected heading_path=%v (no headings in input)", i, piece.HeadingPath)
		}
	}
}

// 核心路径:两个 h1 section 各自产出一个 piece,heading_path 正确。
func TestMarkdownStructured_SimpleH1Sections(t *testing.T) {
	source := "# Architecture\n\nDescribes the system.\n\n# Data Model\n\nCore entities listed here."
	p := NewMarkdownStructured(DefaultConfig())
	got := p.Chunk(source)
	if len(got) != 2 {
		t.Fatalf("expected 2 pieces (one per h1 section), got %d", len(got))
	}
	if got[0].HeadingPath[0] != "Architecture" || got[1].HeadingPath[0] != "Data Model" {
		t.Errorf("heading paths wrong: %v / %v", got[0].HeadingPath, got[1].HeadingPath)
	}
	if got[0].ChunkLevel != 1 || got[1].ChunkLevel != 1 {
		t.Errorf("chunk_level = %d / %d, want 1 / 1", got[0].ChunkLevel, got[1].ChunkLevel)
	}
	// Contextual chunking:chunk content 应以 markdown heading 前缀开头(h1 → "# Architecture"),
	// 然后是 section 的正文。这是 T1.3 让 embedding 同时吃到关键词和语义域的机制。
	if !strings.HasPrefix(got[0].Content, "# Architecture\n\n") {
		t.Errorf("chunk missing heading breadcrumb prefix: %q", got[0].Content)
	}
	if !strings.Contains(got[0].Content, "Describes the system.") {
		t.Errorf("section body lost in chunk: %q", got[0].Content)
	}
}

// 嵌套 heading:h1 下有 h2,h2 的 heading_path 要带上 h1。
func TestMarkdownStructured_NestedHeadingPath(t *testing.T) {
	source := "# Payment\n\nIntro paragraph.\n\n## Stripe\n\nStripe details.\n\n## Alipay\n\nAlipay details.\n\n# Other\n\nTail."
	p := NewMarkdownStructured(DefaultConfig())
	got := p.Chunk(source)
	// 预期 4 个非空 section:Payment intro、Payment>Stripe、Payment>Alipay、Other
	if len(got) != 4 {
		t.Fatalf("expected 4 pieces, got %d: %+v", len(got), got)
	}

	wantPaths := [][]string{
		{"Payment"},
		{"Payment", "Stripe"},
		{"Payment", "Alipay"},
		{"Other"},
	}
	for i, want := range wantPaths {
		if !equalStringSlice(got[i].HeadingPath, want) {
			t.Errorf("piece %d heading_path = %v, want %v", i, got[i].HeadingPath, want)
		}
	}
	if got[1].ChunkLevel != 2 || got[2].ChunkLevel != 2 {
		t.Errorf("h2 chunk_level should be 2, got %d/%d", got[1].ChunkLevel, got[2].ChunkLevel)
	}
}

// Preamble(第一个 heading 之前的内容)要作为一个独立的 level=0 的 piece,不丢失。
func TestMarkdownStructured_PreambleBeforeFirstHeading(t *testing.T) {
	source := "Doc overview without a title.\n\nSome context paragraphs.\n\n# Section A\n\nSection body."
	p := NewMarkdownStructured(DefaultConfig())
	got := p.Chunk(source)
	if len(got) != 2 {
		t.Fatalf("expected 2 pieces (preamble + Section A), got %d", len(got))
	}
	if len(got[0].HeadingPath) != 0 || got[0].ChunkLevel != 0 {
		t.Errorf("preamble should have empty heading_path and level=0, got path=%v level=%d",
			got[0].HeadingPath, got[0].ChunkLevel)
	}
	if !strings.Contains(got[0].Content, "overview") {
		t.Errorf("preamble content lost: %q", got[0].Content)
	}
}

// 超长 section 需要被递归分隔符切成多段,但每段仍然保留同样的 heading_path(不丢上下文)。
func TestMarkdownStructured_LongSectionSplitsButKeepsHeading(t *testing.T) {
	long := strings.Repeat("alpha beta gamma ", 100) // >> MaxChars=100
	source := "# Big Section\n\n" + long
	p := NewMarkdownStructured(Config{MaxChars: 100, OverlapChars: 0})
	got := p.Chunk(source)
	if len(got) < 2 {
		t.Fatalf("long section should split into multiple pieces, got %d", len(got))
	}
	for i, piece := range got {
		if len(piece.HeadingPath) == 0 || piece.HeadingPath[0] != "Big Section" {
			t.Errorf("piece %d lost heading context: path=%v", i, piece.HeadingPath)
		}
		if piece.ChunkLevel != 1 {
			t.Errorf("piece %d level=%d, want 1", i, piece.ChunkLevel)
		}
	}
}

// 代码块里的 `#` 不应该被误识别为 heading。
func TestMarkdownStructured_HashInCodeBlockIsNotHeading(t *testing.T) {
	source := "# Real Heading\n\nSome text.\n\n```\n# This is a comment, not a heading\nprint('x')\n```\n\nAfter code."
	p := NewMarkdownStructured(DefaultConfig())
	got := p.Chunk(source)
	// 应该只有一个 section(Real Heading),代码块里的 `# This is a comment` 是正文。
	if len(got) != 1 {
		t.Fatalf("expected 1 section (code block # ignored), got %d: %+v", len(got), got)
	}
	if got[0].HeadingPath[0] != "Real Heading" {
		t.Errorf("wrong heading: %v", got[0].HeadingPath)
	}
	if !strings.Contains(got[0].Content, "# This is a comment") {
		t.Errorf("code block content lost: %q", got[0].Content)
	}
}

// Heading 里有 inline 格式(粗体/代码)的文本提取应当是纯文本。
func TestMarkdownStructured_InlineFormattingInHeading(t *testing.T) {
	source := "## The `V2Feedback` Table\n\nDescribes the feedback table schema."
	p := NewMarkdownStructured(DefaultConfig())
	got := p.Chunk(source)
	if len(got) != 1 {
		t.Fatalf("expected 1 piece, got %d", len(got))
	}
	// 反引号应该被剥掉,留下 "The V2Feedback Table"。
	if got[0].HeadingPath[0] != "The V2Feedback Table" {
		t.Errorf("inline code not stripped from heading: %q", got[0].HeadingPath[0])
	}
}

func equalStringSlice(a, b []string) bool {
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
