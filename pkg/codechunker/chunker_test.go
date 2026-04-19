package codechunker

import (
	"strings"
	"testing"
)

func TestChunk_Go_FunctionsAndMethods(t *testing.T) {
	src := `package foo

import "fmt"

// helper greets.
func helper(name string) string {
	return "hello, " + name
}

type Greeter struct {
	prefix string
}

func (g *Greeter) Greet(name string) string {
	return g.prefix + helper(name)
}
`
	c := New(DefaultConfig())
	pieces := c.Chunk("go", src)
	if len(pieces) == 0 {
		t.Fatal("expected at least 1 piece")
	}

	// 按种类统计,期望:1 preamble + 1 function + 1 type + 1 method
	var kinds = map[string]int{}
	for _, p := range pieces {
		kinds[p.Kind]++
	}
	if kinds[ChunkKindPreamble] != 1 {
		t.Errorf("expected 1 preamble, got %d (pieces=%+v)", kinds[ChunkKindPreamble], kindList(pieces))
	}
	if kinds[ChunkKindFunction] < 1 {
		t.Errorf("expected at least 1 function, got %d", kinds[ChunkKindFunction])
	}
	if kinds[ChunkKindMethod] < 1 {
		t.Errorf("expected at least 1 method, got %d", kinds[ChunkKindMethod])
	}

	// 验证能抽出 symbol name
	var names []string
	for _, p := range pieces {
		if p.SymbolName != "" {
			names = append(names, p.SymbolName)
		}
	}
	joined := strings.Join(names, ",")
	for _, want := range []string{"helper", "Greeter", "Greet"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected symbol %q in names %q", want, joined)
		}
	}

	// Language 字段回填正确
	for _, p := range pieces {
		if p.Language != "go" {
			t.Errorf("expected Language=go, got %q for piece kind=%s", p.Language, p.Kind)
		}
	}
}

func TestChunk_Python_FunctionsAndClass(t *testing.T) {
	src := `"""Module docstring."""
import os


def top_level(x):
    return x + 1


class Foo:
    def method(self):
        return 42
`
	c := New(DefaultConfig())
	pieces := c.Chunk("python", src)
	if len(pieces) == 0 {
		t.Fatal("expected at least 1 piece")
	}

	var names []string
	for _, p := range pieces {
		if p.SymbolName != "" {
			names = append(names, p.SymbolName)
		}
	}
	joined := strings.Join(names, ",")
	for _, want := range []string{"top_level", "Foo"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected symbol %q in names %q", want, joined)
		}
	}
}

func TestChunk_UnknownLanguage_Fallback(t *testing.T) {
	// 任意语言的代码,language="unknown" 就走 heuristic
	src := strings.Repeat("some line of code\n", 10)
	c := New(DefaultConfig())
	pieces := c.Chunk("unknown", src)
	if len(pieces) == 0 {
		t.Fatal("expected fallback pieces")
	}
	for _, p := range pieces {
		if p.Kind != ChunkKindUnparsed {
			t.Errorf("fallback should produce unparsed kind, got %s", p.Kind)
		}
		if p.Language != "unknown" {
			t.Errorf("expected Language=unknown, got %q", p.Language)
		}
	}
}

func TestChunk_EmptyContent(t *testing.T) {
	c := New(DefaultConfig())
	pieces := c.Chunk("go", "")
	if len(pieces) != 0 {
		t.Errorf("empty content should yield 0 pieces, got %d", len(pieces))
	}
	pieces = c.Chunk("go", "   \n\t\n")
	if len(pieces) != 0 {
		t.Errorf("whitespace-only should yield 0 pieces, got %d", len(pieces))
	}
}

func TestChunk_NoDefinitionsFallsBack(t *testing.T) {
	// 纯声明 / 配置文件:只有 package,没 function/type → 应该走 heuristic
	src := `package foo
`
	c := New(DefaultConfig())
	pieces := c.Chunk("go", src)
	// 预期:无 definition → fallback heuristic,产出 unparsed chunk
	if len(pieces) == 0 {
		t.Fatal("expected fallback pieces when no definitions present")
	}
	for _, p := range pieces {
		if p.Kind != ChunkKindUnparsed {
			t.Errorf("no-def file should fallback to unparsed, got %s", p.Kind)
		}
	}
}

func TestChunk_LargeFunctionSplit(t *testing.T) {
	// 构造一个超大的函数(超过 MaxChunkBytes)→ 应该被切成多个 unparsed 片段
	body := strings.Repeat("\tfmt.Println(\"line\")\n", 500) // 约 500 行 × 20 字节 = 10000 字节
	src := "package foo\n\nfunc huge() {\n" + body + "}\n"
	c := New(Config{MaxChunkBytes: 1024, FallbackWindowLines: 20})
	pieces := c.Chunk("go", src)
	var unparsed int
	for _, p := range pieces {
		if p.Kind == ChunkKindUnparsed {
			unparsed++
		}
	}
	if unparsed < 2 {
		t.Errorf("large function should be split into multiple unparsed pieces, got unparsed=%d pieces=%+v",
			unparsed, kindList(pieces))
	}
}

func TestLanguageFromFilename(t *testing.T) {
	tests := []struct{ name, want string }{
		{"foo.go", "go"},
		{"pkg/bar.go", "go"},
		{"main.py", "python"},
		{"app.ts", "typescript"},
		{"app.tsx", "typescript"},
		{"lib.js", "javascript"},
		{"lib.mjs", "javascript"},
		{"Main.java", "java"},
		{"mod.rs", "rust"},
		{"Dockerfile", "unknown"},
		{"README.md", "unknown"},
	}
	for _, tc := range tests {
		if got := LanguageFromFilename(tc.name); got != tc.want {
			t.Errorf("LanguageFromFilename(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSupportedLanguages(t *testing.T) {
	c := New(DefaultConfig())
	langs := c.SupportedLanguages()
	want := []string{"go", "python", "typescript", "javascript", "java", "rust"}
	if len(langs) != len(want) {
		t.Errorf("SupportedLanguages len = %d, want %d: %v", len(langs), len(want), langs)
	}
	got := map[string]bool{}
	for _, l := range langs {
		got[l] = true
	}
	for _, l := range want {
		if !got[l] {
			t.Errorf("missing language %q in SupportedLanguages() = %v", l, langs)
		}
	}
}

// kindList helper:把 pieces 压成 "kind:name" 列表方便 error message。
func kindList(pieces []Piece) []string {
	out := make([]string, len(pieces))
	for i, p := range pieces {
		out[i] = p.Kind + ":" + p.SymbolName
	}
	return out
}
