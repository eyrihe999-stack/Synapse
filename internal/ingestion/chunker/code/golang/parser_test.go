package golang

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/eyrihe999-stack/Synapse/internal/ingestion"
	codechunker "github.com/eyrihe999-stack/Synapse/internal/ingestion/chunker/code"
)

// helper: parse + 找符号
func mustParse(t *testing.T, src string) *codechunker.ParsedFile {
	t.Helper()
	p, err := New().Parse(context.Background(), []byte(src))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if p == nil {
		t.Fatalf("Parse returned nil ParsedFile")
	}
	return p
}

func findSymbol(syms []codechunker.Symbol, name string) *codechunker.Symbol {
	for i := range syms {
		if syms[i].Name == name {
			return &syms[i]
		}
	}
	return nil
}

// ─── 基础 case ──────────────────────────────────────────────────────────────

func TestParse_EmptyContent(t *testing.T) {
	p, err := New().Parse(context.Background(), nil)
	if err != nil {
		t.Fatalf("nil content should not error: %v", err)
	}
	if p != nil {
		t.Fatalf("nil content should return nil ParsedFile, got %+v", p)
	}
}

func TestParse_PackageOnly(t *testing.T) {
	src := `// Package foo 顶部注释。
package foo
`
	p := mustParse(t, src)
	if p.Preamble == "" {
		t.Fatal("expected non-empty preamble")
	}
	if !strings.Contains(p.Preamble, "package foo") {
		t.Errorf("preamble should contain 'package foo', got %q", p.Preamble)
	}
	if !strings.Contains(p.Preamble, "Package foo 顶部注释") {
		t.Errorf("preamble should contain file-level doc, got %q", p.Preamble)
	}
	if len(p.Symbols) != 0 {
		t.Errorf("no symbols expected, got %d", len(p.Symbols))
	}
}

func TestParse_PackageWithImports(t *testing.T) {
	src := `package foo

import (
	"context"
	"fmt"
)
`
	p := mustParse(t, src)
	if !strings.Contains(p.Preamble, "import") {
		t.Errorf("preamble should include imports, got %q", p.Preamble)
	}
}

// ─── 函数 ────────────────────────────────────────────────────────────────────

func TestParse_SimpleFunction(t *testing.T) {
	src := `package foo

// Hello says hi.
func Hello() string { return "hi" }
`
	p := mustParse(t, src)
	if len(p.Symbols) != 1 {
		t.Fatalf("expected 1 symbol, got %d", len(p.Symbols))
	}
	sym := p.Symbols[0]
	if sym.Name != "Hello" {
		t.Errorf("expected name=Hello, got %q", sym.Name)
	}
	if sym.Kind != codechunker.KindFunction {
		t.Errorf("expected kind=function, got %q", sym.Kind)
	}
	if !strings.Contains(sym.Body, "Hello says hi.") {
		t.Errorf("body should include doc comment, got %q", sym.Body)
	}
	if !strings.Contains(sym.Body, "return \"hi\"") {
		t.Errorf("body should include implementation, got %q", sym.Body)
	}
	if !strings.HasPrefix(sym.Signature, "func Hello()") {
		t.Errorf("signature should start with 'func Hello()', got %q", sym.Signature)
	}
	if !strings.Contains(sym.Signature, "string") {
		t.Errorf("signature should mention return type 'string', got %q", sym.Signature)
	}
}

func TestParse_MethodReceiver(t *testing.T) {
	src := `package foo

type Server struct{}

// Start runs the server.
func (s *Server) Start() error { return nil }
`
	p := mustParse(t, src)
	sym := findSymbol(p.Symbols, "Server.Start")
	if sym == nil {
		t.Fatalf("expected Server.Start, got %+v", names(p.Symbols))
	}
	if sym.Kind != codechunker.KindMethod {
		t.Errorf("expected kind=method, got %q", sym.Kind)
	}
	if !strings.Contains(sym.Signature, "(s *Server)") {
		t.Errorf("signature should include receiver, got %q", sym.Signature)
	}
}

func TestParse_MultipleFunctions(t *testing.T) {
	src := `package foo

func A() {}
func B() {}
func C() {}
`
	p := mustParse(t, src)
	if len(p.Symbols) != 3 {
		t.Fatalf("expected 3 symbols, got %d", len(p.Symbols))
	}
	for i, want := range []string{"A", "B", "C"} {
		if p.Symbols[i].Name != want {
			t.Errorf("symbol[%d] = %q, want %q", i, p.Symbols[i].Name, want)
		}
	}
}

func TestParse_FunctionWithGenerics(t *testing.T) {
	src := `package foo

// Map maps a slice.
func Map[T any, U any](in []T, f func(T) U) []U { return nil }
`
	p := mustParse(t, src)
	sym := findSymbol(p.Symbols, "Map")
	if sym == nil {
		t.Fatalf("expected Map, got %+v", names(p.Symbols))
	}
	if !strings.Contains(sym.Signature, "[T any, U any]") {
		t.Errorf("signature should include type params, got %q", sym.Signature)
	}
}

// ─── 类型 ────────────────────────────────────────────────────────────────────

func TestParse_StructType(t *testing.T) {
	src := `package foo

// User holds user info.
type User struct {
	ID   uint64
	Name string
}
`
	p := mustParse(t, src)
	sym := findSymbol(p.Symbols, "User")
	if sym == nil {
		t.Fatalf("expected User, got %+v", names(p.Symbols))
	}
	if sym.Kind != codechunker.KindClass {
		t.Errorf("expected kind=class, got %q", sym.Kind)
	}
	if !strings.Contains(sym.Body, "User holds user info") {
		t.Errorf("body should include doc, got %q", sym.Body)
	}
	if !strings.Contains(sym.Body, "type User struct") {
		t.Errorf("body should include struct decl, got %q", sym.Body)
	}
}

func TestParse_InterfaceType(t *testing.T) {
	src := `package foo

type Reader interface {
	Read() ([]byte, error)
}
`
	p := mustParse(t, src)
	sym := findSymbol(p.Symbols, "Reader")
	if sym == nil {
		t.Fatalf("expected Reader, got %+v", names(p.Symbols))
	}
	if !strings.Contains(sym.Body, "type Reader interface") {
		t.Errorf("body should include interface decl, got %q", sym.Body)
	}
}

func TestParse_GroupedTypeDecl(t *testing.T) {
	src := `package foo

type (
	A int
	// BDoc 说明 B。
	B string
)
`
	p := mustParse(t, src)
	if len(p.Symbols) != 2 {
		t.Fatalf("expected 2 symbols, got %d (%+v)", len(p.Symbols), names(p.Symbols))
	}
	a := findSymbol(p.Symbols, "A")
	b := findSymbol(p.Symbols, "B")
	if a == nil || b == nil {
		t.Fatalf("missing A or B: %+v", names(p.Symbols))
	}
	if !strings.HasPrefix(a.Body, "type ") {
		t.Errorf("A.Body should be prefixed by 'type ', got %q", a.Body)
	}
	if !strings.Contains(b.Body, "BDoc 说明 B") {
		t.Errorf("B.Body should include its own doc, got %q", b.Body)
	}
}

// ─── 健壮性 ──────────────────────────────────────────────────────────────────

func TestParse_SyntaxError_StillReturnsRecognized(t *testing.T) {
	// 故意写一个未闭合的 func — go/parser 应当返 ast.File != nil + err
	src := `package foo

// A is a function.
func A() {
	return
}

func Broken( {  // ← 语法错
`
	p, err := New().Parse(context.Background(), []byte(src))
	if err != nil {
		t.Fatalf("Parse should not return error on syntax error: %v", err)
	}
	if p == nil {
		t.Fatal("ParsedFile should be non-nil even with syntax errors")
	}
	// A 应当被认出来
	if findSymbol(p.Symbols, "A") == nil {
		t.Errorf("A should be recognized despite syntax error in Broken; got %+v", names(p.Symbols))
	}
}

func TestParse_VarConstNotEmittedAsSymbol(t *testing.T) {
	// var / const 一期不切独立 chunk;只 type 进 Symbols
	src := `package foo

var Counter int = 0

const Pi = 3.14

type T int
`
	p := mustParse(t, src)
	if len(p.Symbols) != 1 || p.Symbols[0].Name != "T" {
		t.Errorf("expected only T as symbol, got %+v", names(p.Symbols))
	}
}

// ─── 行号 ────────────────────────────────────────────────────────────────────

func TestParse_LineNumbers(t *testing.T) {
	src := `package foo

// Foo at line 4
func Foo() {
	// body
}
`
	p := mustParse(t, src)
	if sym := findSymbol(p.Symbols, "Foo"); sym != nil {
		// doc comment 在第 3 行,func 第 4 行;LineStart 应取 doc 起点 = 3
		if sym.LineStart != 3 {
			t.Errorf("Foo.LineStart = %d, want 3", sym.LineStart)
		}
		if sym.LineEnd < sym.LineStart {
			t.Errorf("Foo.LineEnd %d < LineStart %d", sym.LineEnd, sym.LineStart)
		}
	}
}

// ─── 上层 chunker 集成 ──────────────────────────────────────────────────────

func TestChunker_EndToEnd(t *testing.T) {
	src := `// Package foo demo.
package foo

import "fmt"

// Hello prints hi.
func Hello(name string) {
	fmt.Println("hi", name)
}

// Counter is a int.
type Counter int

func (c *Counter) Inc() { *c++ }
`
	chk := codechunker.New(0, New())
	if !chk.Supports("go") {
		t.Fatal("chunker should support 'go'")
	}
	doc := makeDoc(src, "go")
	chunks, err := chk.Chunk(context.Background(), doc)
	if err != nil {
		t.Fatalf("Chunk error: %v", err)
	}
	if len(chunks) < 4 {
		t.Fatalf("expected ≥4 chunks (preamble + 3 symbols), got %d: %s", len(chunks), summarize(chunks))
	}
	// 第 0 个应是 preamble
	if chunks[0].ContentType != "preamble" {
		t.Errorf("chunks[0].ContentType = %q, want 'preamble'", chunks[0].ContentType)
	}
	// 至少一个 function / method
	gotFunc, gotMethod, gotClass := false, false, false
	for _, ch := range chunks {
		switch ch.ContentType {
		case "function":
			gotFunc = true
		case "method":
			gotMethod = true
		case "class":
			gotClass = true
		}
		if ch.Language != "go" && ch.ContentType != "preamble" {
			// preamble 也可以无 SymbolName,但 Language 应填上
			t.Errorf("chunk language should be 'go', got %q", ch.Language)
		}
		// metadata 应包含 language
		if lang, _ := ch.Metadata["language"].(string); lang != "go" {
			t.Errorf("metadata.language = %v, want 'go' (chunk: %s)", ch.Metadata["language"], ch.ContentType)
		}
	}
	if !gotFunc || !gotMethod || !gotClass {
		t.Errorf("missing chunk type: function=%v method=%v class=%v", gotFunc, gotMethod, gotClass)
	}
	// Index 必须连续 0..N-1
	for i, ch := range chunks {
		if ch.Index != i {
			t.Errorf("chunks[%d].Index = %d, want %d", i, ch.Index, i)
		}
		if ch.ChunkerVersion == "" {
			t.Errorf("chunks[%d].ChunkerVersion empty", i)
		}
	}
}

func TestChunker_BigSymbolParentChild(t *testing.T) {
	// 造一个超 budget 的函数(>2000 字节,默认 budget = 500 token = 2000 字节)
	body := strings.Repeat("\tx := 1\n", 400) // ~3200 字节
	src := "package foo\n\n// Big does many things.\nfunc Big() {\n" + body + "}\n"
	chk := codechunker.New(0, New())
	chunks, err := chk.Chunk(context.Background(), makeDoc(src, "go"))
	if err != nil {
		t.Fatalf("Chunk error: %v", err)
	}
	// 至少:preamble + parent + ≥1 child
	if len(chunks) < 3 {
		t.Fatalf("expected ≥3 chunks, got %d: %s", len(chunks), summarize(chunks))
	}
	// 找到 parent 和 child
	parentIdx := -1
	childCount := 0
	for i, ch := range chunks {
		if ch.SymbolName != "Big" {
			continue
		}
		if ch.ParentIndex == nil {
			if parentIdx >= 0 {
				t.Errorf("multiple parent chunks for Big: %d, %d", parentIdx, i)
			}
			parentIdx = i
		} else {
			childCount++
			// child 的 ParentIndex 必须指 parent
			if *ch.ParentIndex != parentIdx {
				t.Errorf("child[%d].ParentIndex=%d, want %d", i, *ch.ParentIndex, parentIdx)
			}
		}
	}
	if parentIdx < 0 {
		t.Fatal("expected one parent chunk for Big")
	}
	if childCount < 1 {
		t.Fatal("expected ≥1 child chunk for Big")
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func names(syms []codechunker.Symbol) []string {
	out := make([]string, len(syms))
	for i, s := range syms {
		out[i] = s.Name
	}
	return out
}

// makeDoc 构造一个最小 NormalizedDoc 给 chunker.Chunk 用。
func makeDoc(src, language string) *ingestion.NormalizedDoc {
	return &ingestion.NormalizedDoc{
		OrgID:      1,
		SourceType: ingestion.SourceTypeDocument,
		SourceID:   "test:" + language,
		Content:    []byte(src),
		Language:   language,
		FileName:   "test." + language,
	}
}

// summarize 给失败信息打印 chunks 概览。
func summarize(chunks []ingestion.IngestedChunk) string {
	parts := make([]string, len(chunks))
	for i, ch := range chunks {
		parts[i] = fmt.Sprintf("[%d %s/%s]", i, ch.ContentType, ch.SymbolName)
	}
	return strings.Join(parts, " ")
}
