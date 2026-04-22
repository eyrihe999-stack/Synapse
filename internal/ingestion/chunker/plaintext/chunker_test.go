package plaintext

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
		SourceID:   "test",
		Content:    []byte(s),
	}
}

// TestEmpty 空 doc / 全空白 → nil, nil。
func TestEmpty(t *testing.T) {
	c := New(500)
	for _, s := range []string{"", "   ", "\n\n\n"} {
		out, err := c.Chunk(context.Background(), makeDoc(s))
		if err != nil {
			t.Fatalf("%q: err = %v", s, err)
		}
		if out != nil {
			t.Fatalf("%q: want nil, got %d chunks", s, len(out))
		}
	}
}

// TestShortStaysOne 短文本一 chunk。
func TestShortStaysOne(t *testing.T) {
	c := New(500)
	out, err := c.Chunk(context.Background(), makeDoc("hello world"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 chunk, got %d", len(out))
	}
	if out[0].ContentType != "text" {
		t.Fatalf("ContentType = %q, want text", out[0].ContentType)
	}
	if len(out[0].HeadingPath) != 0 {
		t.Fatalf("HeadingPath should be empty, got %v", out[0].HeadingPath)
	}
	if out[0].Level != 0 {
		t.Fatalf("Level = %d, want 0", out[0].Level)
	}
	if out[0].ChunkerVersion != chunkerVersion {
		t.Fatalf("ChunkerVersion = %q, want %q", out[0].ChunkerVersion, chunkerVersion)
	}
}

// TestParagraphsMergeAndSplit 多段:短段落合并,超 budget 时切分。
func TestParagraphsMergeAndSplit(t *testing.T) {
	// maxTokens=25 → maxBytes=100
	c := New(25)

	p1 := strings.Repeat("a", 60)
	p2 := strings.Repeat("b", 60)
	p3 := strings.Repeat("c", 60)
	// 3 个 60-byte 段落,合并两段就会超 100 bytes → 期望至少 3 个 chunk(每段独占)
	in := p1 + "\n\n" + p2 + "\n\n" + p3
	out, err := c.Chunk(context.Background(), makeDoc(in))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("want 3 chunks, got %d: %+v", len(out), out)
	}
	for i, ch := range out {
		if !strings.HasPrefix(ch.Content, string(rune('a'+i))) {
			t.Fatalf("chunk %d content head = %.5s..., want prefix %q", i, ch.Content, string(rune('a'+i)))
		}
		if ch.Index != i {
			t.Fatalf("Index[%d] = %d, want %d", i, ch.Index, i)
		}
	}
}

// TestLongParagraphFallbackToSentences 单段超长 → 按句号切。
func TestLongParagraphFallbackToSentences(t *testing.T) {
	c := New(20) // ~80 bytes
	s1 := strings.Repeat("x", 50) + "."
	s2 := strings.Repeat("y", 50) + "."
	in := s1 + s2 // 100+ bytes,单段
	out, err := c.Chunk(context.Background(), makeDoc(in))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) < 2 {
		t.Fatalf("want ≥2 chunks after sentence split, got %d: %+v", len(out), out)
	}
}
