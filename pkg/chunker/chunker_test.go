package chunker

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunker_Empty(t *testing.T) {
	c := NewPlainText(DefaultConfig())
	if got := c.Chunk(""); len(got) != 0 {
		t.Errorf("empty string should produce 0 chunks, got %d", len(got))
	}
	if got := c.Chunk("   \n\t  "); len(got) != 0 {
		t.Errorf("whitespace-only should produce 0 chunks, got %d", len(got))
	}
}

func TestChunker_ShortText_SingleChunk(t *testing.T) {
	c := NewPlainText(Config{MaxChars: 100, OverlapChars: 10})
	got := c.Chunk("hello world")
	if len(got) != 1 {
		t.Fatalf("short text → %d chunks, want 1", len(got))
	}
	if got[0].Content != "hello world" {
		t.Errorf("content = %q", got[0].Content)
	}
	if got[0].TokenCount != 11 {
		t.Errorf("tokencount = %d, want 11", got[0].TokenCount)
	}
}

func TestChunker_LongText_SplitsAtParagraphs(t *testing.T) {
	// 三段,每段远小于 MaxChars 但合起来超过 MaxChars → 预期按段落被拆成多块。
	p1 := strings.Repeat("alpha ", 20)
	p2 := strings.Repeat("beta ", 20)
	p3 := strings.Repeat("gamma ", 20)
	text := p1 + "\n\n" + p2 + "\n\n" + p3

	c := NewPlainText(Config{MaxChars: 200, OverlapChars: 0})
	got := c.Chunk(text)
	if len(got) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(got))
	}
	for i, ch := range got {
		runes := utf8.RuneCountInString(ch.Content)
		if runes > 200 {
			t.Errorf("chunk %d has %d runes, exceeds MaxChars 200", i, runes)
		}
	}
	// 每段的关键词应出现在至少一个 chunk 里。
	all := strings.Join(chunkContents(got), "")
	for _, kw := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(all, kw) {
			t.Errorf("output missing keyword %q", kw)
		}
	}
}

func TestChunker_Overlap(t *testing.T) {
	text := strings.Repeat("a", 50) + "\n" + strings.Repeat("b", 50) + "\n" + strings.Repeat("c", 50)
	c := NewPlainText(Config{MaxChars: 70, OverlapChars: 20})
	got := c.Chunk(text)
	if len(got) < 2 {
		t.Fatalf("expected ≥2 chunks, got %d", len(got))
	}
	// 除第一个外,每个 chunk 起始应该带上一个 chunk 的尾巴(overlap)。
	for i := 1; i < len(got); i++ {
		// overlap 来自前一块的最后 OverlapChars 字符 —— 不要求精确,
		// 但当前块前 20 字符应当完全来自前块的尾部。
		prev := got[i-1].Content
		prevTail := prev
		if r := []rune(prev); len(r) > 20 {
			prevTail = string(r[len(r)-20:])
		}
		curHead := got[i].Content
		if r := []rune(curHead); len(r) >= 20 {
			curHead = string(r[:20])
		}
		// 只做包含关系检查 —— trim 会吃掉纯空白的前缀/后缀,导致精确相等断言脆弱。
		if !strings.Contains(got[i].Content, prevTail) && !strings.Contains(prevTail, curHead) {
			t.Errorf("chunk %d head %q does not overlap with prev tail %q", i, curHead, prevTail)
		}
	}
}

func TestChunker_LongSingleParagraph_HardSplit(t *testing.T) {
	// 无空行 / 无句号 / 无空格 → 只能硬切。
	text := strings.Repeat("x", 500)
	c := NewPlainText(Config{MaxChars: 100, OverlapChars: 0})
	got := c.Chunk(text)
	if len(got) != 5 {
		t.Errorf("500 chars / 100 each → %d chunks, want 5", len(got))
	}
	for i, ch := range got {
		if utf8.RuneCountInString(ch.Content) > 100 {
			t.Errorf("chunk %d exceeds MaxChars", i)
		}
	}
}

func TestChunker_CJKPreservesRunes(t *testing.T) {
	// 纯 CJK,硬切路径;按 rune 计数不能切断多字节字符。
	text := strings.Repeat("测试", 100) // 200 runes,600 bytes
	c := NewPlainText(Config{MaxChars: 50, OverlapChars: 0})
	got := c.Chunk(text)
	for i, ch := range got {
		if !utf8.ValidString(ch.Content) {
			t.Errorf("chunk %d contains invalid utf-8", i)
		}
		if utf8.RuneCountInString(ch.Content) > 50 {
			t.Errorf("chunk %d exceeds MaxChars", i)
		}
	}
}

func TestChunker_ConfigNormalization(t *testing.T) {
	// MaxChars <= 0 应回退;Overlap >= Max 应被截半。
	c := NewPlainText(Config{MaxChars: 0, OverlapChars: -5})
	if got := c.Chunk("hello"); len(got) != 1 {
		t.Errorf("normalization failed: %d chunks", len(got))
	}

	// Overlap >= Max:应不爆,并产出 chunks。
	c2 := NewPlainText(Config{MaxChars: 10, OverlapChars: 20})
	got := c2.Chunk(strings.Repeat("ab", 100))
	if len(got) == 0 {
		t.Errorf("expected chunks with normalized overlap")
	}
}

func TestChunker_MarkdownStructure(t *testing.T) {
	// Markdown 文档:标题 + 段落。分隔符策略应优先切在空行上,标题完整保留在某个 chunk 里。
	text := "# Heading A\n\nFirst paragraph here.\n\n# Heading B\n\nSecond paragraph here, which is a bit longer."
	c := NewPlainText(Config{MaxChars: 60, OverlapChars: 0})
	got := c.Chunk(text)
	if len(got) < 2 {
		t.Fatalf("expected ≥2 chunks, got %d", len(got))
	}
	all := strings.Join(chunkContents(got), "|")
	if !strings.Contains(all, "# Heading A") || !strings.Contains(all, "# Heading B") {
		t.Errorf("headings lost in output: %q", all)
	}
}

func TestChunker_CRLFNormalized(t *testing.T) {
	c := NewPlainText(Config{MaxChars: 100, OverlapChars: 0})
	got := c.Chunk("line1\r\nline2\r\nline3")
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	if strings.Contains(got[0].Content, "\r") {
		t.Error("CRLF not normalized to LF")
	}
}

func chunkContents(chunks []Piece) []string {
	out := make([]string, len(chunks))
	for i, c := range chunks {
		out[i] = c.Content
	}
	return out
}
