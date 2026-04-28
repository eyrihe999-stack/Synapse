// Package code 是按"完整符号"切分代码文件的 chunker 框架。
//
// 设计要点:
//
//   - 框架与语言后端解耦:chunker.go 持流程(预算 / parent-child 拆分 / Index 回填),
//     语言后端只负责"把源码解析成 ParsedFile"
//   - 一期只接 Go(走标准库 go/parser,零 cgo);其他语言后续 PR 接 tree-sitter
//   - 输出 IngestedChunk 的 SourceType 仍走 SourceTypeDocument(共用 document_chunks 表),
//     代码元字段(SymbolName/Signature/LineStart/LineEnd/Language)写到 Metadata jsonb,
//     避免新建 code_files 表带来的 schema 改动
//
// 调用顺序:
//
//	chunker.Chunk(doc)
//	   → backend.Parse(content) → *ParsedFile{ Preamble, Symbols }
//	   → 框架 emitPreamble + emitSymbols(超 budget 走 parent-child)
//	   → 回填 Index + ChunkerVersion → 返 []IngestedChunk
package code

import (
	"context"
	"errors"
)

// ErrUnsupportedLanguage 没有为目标 Language 注册 backend 时返。
// chunker 上层捕获并降级到 plaintext(在 ChunkerSelector 层处理),不应让用户看到。
var ErrUnsupportedLanguage = errors.New("code chunker: unsupported language")

// LanguageBackend 把单文件源码解析成结构化的 ParsedFile。
//
// 实现要求:
//
//   - 无状态,可并发调用(每次 Parse 持自己的临时数据结构)
//   - 健壮:语法错的源码应当尽可能 Parse 出已认得的部分,而不是整个 fail。
//     标准做法:语法错时返 *ParsedFile + nil error,error 留给 IO / 致命错
//   - LineStart / LineEnd 是 1-based 闭区间(和 Go 标准库 token.Position 对齐)
type LanguageBackend interface {
	// Language 此后端处理的语言 tag,与 NormalizedDoc.Language 字段对得上(如 "go" / "python")。
	Language() string

	// Parse 单文件解析。content 是 UTF-8 源码;实现自己处理换行归一化。
	Parse(ctx context.Context, content []byte) (*ParsedFile, error)
}

// ParsedFile backend 产出的中间表示。
type ParsedFile struct {
	// Preamble 文件级前置内容:package decl / imports / 顶部 file-level doc。
	// 空串表示无 preamble(罕见,如纯函数体片段)。
	Preamble string

	// PreambleLineStart / PreambleLineEnd preamble 在原文中的行号,1-based 闭区间。
	// 0 表示无 preamble(配合 Preamble == "")。
	PreambleLineStart int
	PreambleLineEnd   int

	// Symbols 顶层符号列表,按出现顺序。
	// Go 中即 FuncDecl + GenDecl(TypeSpec)展平后的序列;嵌套不递归(struct 的 method 是顶层)。
	Symbols []Symbol
}

// Symbol 单个"完整符号"的结构化描述。
//
// Body 为完整源码(含紧贴上方的 doc comment)—— framework 层把它直接当 chunk 内容。
// 大符号超 budget 时 framework 会自己 parent-child 拆,backend 不需要预先拆。
type Symbol struct {
	// Name 符号名(函数 / 类型名)。
	Name string

	// Signature 完整签名(仅 function/method);type / class 留空。
	Signature string

	// Kind 内容分类。和 IngestedChunk.ContentType 直接对齐:
	// "function" / "method" / "class"(struct/interface/enum 统一用 "class")。
	Kind string

	// Body 完整源码,含上方紧贴的 doc comment。Trim 末尾空白即可,首尾换行不强求。
	Body string

	// LineStart / LineEnd 1-based 闭区间(含 doc comment 起始)。
	LineStart int
	LineEnd   int
}

// SymbolKind 常量,backend 实现填 Symbol.Kind 用。
const (
	KindFunction = "function"
	KindMethod   = "method"
	KindClass    = "class"
)
