// Package codechunker 按 AST 切分源码 —— 函数/类级别的 chunk,保持语义完整。
//
// 设计:
//   - tree-sitter 驱动:一套 Go binding 覆盖 6+ 种主流语言,AST 节点类型映射到 chunk 类型。
//   - 语言按 filename 扩展名识别(简单、确定);不做 content sniffing,代价是 shebang-only 文件会被误判
//     (影响可忽略 —— 没扩展名的代码文件在 git 仓库里是极少数)。
//   - 不支持的语言 / parse 失败 → heuristic fallback,chunk kind 标 "unparsed" 让检索层感知质量。
//   - 和 document/chunker 完全独立:document 切段落,code 切函数,语义完全不一样,没有复用意义。
//
// 单次 Chunk 调用的消耗:tree-sitter parse 通常几 ms 搞定几百行文件。线程安全性由 tree-sitter 保证
// —— 每次 Chunk 内部 new 一个 parser,不跨 goroutine 复用。
package codechunker

import (
	"path/filepath"
	"strings"
)

// Piece 一个切片。chunk_idx 不在这里,由 service 层按产出顺序分配。
//
// 所有字段语义见 internal/code/model.CodeChunk 对应列;这里只是纯"切分结果"的内存形态,
// 不带 org_id / file_id 等上下文。
type Piece struct {
	// Kind 见 codechunker.ChunkKind* 常量。
	Kind string
	// SymbolName 函数 / 方法 / 类的名字。preamble 和 unparsed 留空。
	SymbolName string
	// Signature 完整签名(仅 function/method)。超长 chunker 内部截断到 1024 字节。
	Signature string
	// Content chunk 正文 —— 原文对应片段。
	Content string
	// LineStart / LineEnd 1-based 闭区间行号。
	LineStart int
	LineEnd   int
	// TokenCount 近似 token 数(按 UTF-8 字节 /4 粗估,对代码来说足够)。
	TokenCount int
	// Language 切分时识别的语言 tag。unparsed 路径保留 "unknown"。
	Language string
}

// ChunkKind* Piece.Kind 的取值。和 internal/code 里的常量同步(但不跨包依赖,这里是底层 chunker,
// 让 code 模块 import 我们而不是反过来)。
const (
	ChunkKindFunction = "function"
	ChunkKindMethod   = "method"
	ChunkKindClass    = "class"
	ChunkKindPreamble = "preamble"
	ChunkKindUnparsed = "unparsed"
)

// Chunker 对外接口。Chunk 不持有任何 file 级上下文,纯函数式。
type Chunker interface {
	// Chunk 按 language 选 spec 跑 tree-sitter;language 未注册或 parse 失败 fallback heuristic。
	// 无论哪条路径都必须返至少一个 Piece(空内容除外)—— 保证"每个文件都有 chunk 进检索"。
	Chunk(language, content string) []Piece

	// SupportedLanguages 返已注册的语言 tag 列表。诊断 + 装配日志用。
	SupportedLanguages() []string
}

// Config 构造参数。
type Config struct {
	// MaxChunkBytes 单 chunk 字节上限。所有输出路径(AST definition / preamble / heuristic fallback)
	// 都受此约束 —— 超出就按行切成多段 unparsed。
	//
	// 默认 8192 字节(约 2048 tokens,按 OpenAI BPE 1 token ≈ 4 字符估)。
	// 选值依据:实测 Synapse 自身 1215 个函数 99.9% 在 8KB 以内,再加上嵌入模型的 8192 tokens
	// 上限,8KB 字节 ≈ 2K tokens 的英文代码或 ≈ 6K tokens 的密集中文注释,都还有余量。
	// 再严(如 4KB)会误伤 ~1% 的中大型函数(核心业务方法)让它们被切成 unparsed,得不偿失。
	MaxChunkBytes int
	// FallbackWindowLines heuristic 切分时每 window 的行数上限。默认 60 行
	// —— 和典型函数的行数量级吻合,保证 fallback 产出的 chunk 和 AST 切的粒度近似。
	// 字节数先于行数触发:一行 > MaxChunkBytes 时会出一个超大片段(极端情况,靠上游黑名单拦截 +
	// ingest 层 embedInputMaxBytes 兜底)。
	FallbackWindowLines int
}

// DefaultConfig 合理默认值。
func DefaultConfig() Config {
	return Config{
		MaxChunkBytes:       8 * 1024,
		FallbackWindowLines: 60,
	}
}

// chunker 实现。spec 注册表在 spec.go,生命周期和 chunker 实例一致(可跨 goroutine 共享,tree-sitter Language 是只读常量)。
type chunker struct {
	cfg   Config
	specs map[string]*languageSpec
}

// New 构造 Chunker。内部注册所有已知语言的 spec(spec.go 里的 defaultSpecs)。
// 首次调用会 CGO-load 各语言 grammar;spec.go 采用 package-level 变量 + init 时懒构造,
// 实际 CGO cost 发生在 grammar import 时(编译期就链接好了)。
func New(cfg Config) Chunker {
	if cfg.MaxChunkBytes <= 0 {
		cfg.MaxChunkBytes = 8 * 1024
	}
	if cfg.FallbackWindowLines <= 0 {
		cfg.FallbackWindowLines = 60
	}
	return &chunker{
		cfg:   cfg,
		specs: defaultSpecs(),
	}
}

// Chunk 对外主入口。
//
// 语义:
//   - content 空 / 全空白 → 返空切片(零 chunk 合法,调用方按空处理)
//   - language 有对应 spec → AST 切分,失败降级 heuristic
//   - language 没对应 spec → 直接 heuristic
//
// 返回的 Piece 顺序和源码 top-down 一致,便于保持阅读顺序。
func (c *chunker) Chunk(language, content string) []Piece {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	language = strings.ToLower(strings.TrimSpace(language))

	if spec, ok := c.specs[language]; ok {
		pieces, err := c.chunkWithAST(spec, content)
		if err == nil && len(pieces) > 0 {
			return pieces
		}
		// AST 失败:bubble 到 heuristic,不报错 —— MVP 宁可产低质量 chunk 也别让整个文件失败。
		// 二期把 err 通过 logger 记录,现在没注入 logger 不打点。
	}

	return c.chunkHeuristic(content, language)
}

// SupportedLanguages 见接口。
func (c *chunker) SupportedLanguages() []string {
	out := make([]string, 0, len(c.specs))
	for lang := range c.specs {
		out = append(out, lang)
	}
	return out
}

// ─── 语言识别 ──────────────────────────────────────────────────────────────

// LanguageFromFilename 按扩展名返语言 tag。不认识的返 "unknown"。
//
// 不支持 content sniffing:扩展名足够稳定,极少数"有内容无扩展名"的脚本文件(如 Dockerfile、Makefile)
// 走 heuristic 路径也不是大问题。扩展名大小写不敏感。
func LanguageFromFilename(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".mjs", ".cjs", ".jsx":
		return "javascript"
	case ".java":
		return "java"
	case ".rs":
		return "rust"
	default:
		return "unknown"
	}
}

// estimateTokens 字节数 /4 的粗估 —— OpenAI 的 BPE tokenizer 对英文/代码大致 1 token ≈ 4 字符。
// 精度对调用方(batch 估算 + UI 显示)够用;真要精确得跑 tiktoken,成本不值。
func estimateTokens(content string) int {
	n := len(content) / 4
	if n == 0 && len(content) > 0 {
		return 1
	}
	return n
}

// countLines 返字节 slice 中 '\n' 数 + 1 —— 1-based 行号下"无换行文件"也算 1 行。
// 空字符串返 0。
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	return n + 1
}
