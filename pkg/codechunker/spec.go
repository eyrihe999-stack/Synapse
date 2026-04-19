// spec.go 每种语言的 tree-sitter 配置:grammar + 要切出来的 AST 节点类型 + 怎么抽 symbol name。
//
// 新加语言只要在 defaultSpecs 里加一条,不用改 treesitter.go 的逻辑。
package codechunker

import (
	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/java"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/rust"
	"github.com/smacker/go-tree-sitter/typescript/typescript"
)

// languageSpec 一种语言的 AST 切分配方。
type languageSpec struct {
	// Name 语言 tag,和 LanguageFromFilename 返回值一致,用作 Piece.Language。
	Name string

	// Language tree-sitter 的 grammar。CGO 常量,构造期引用即可。
	Language *sitter.Language

	// Definition tree-sitter AST 节点类型集合:命中这些类型的 top-level 节点会被切成独立 chunk。
	// 当前策略只遍历 root 的直接 named children,不递归 —— 嵌套定义(如 Python 里 class 内的 method)
	// 整个和外层 class 打包成一个 chunk,避免召回时"类声明 + 方法"被拆得看不懂上下文。
	// 同一语言中 function vs method 的区分在 Kind 判定时做(见 classifyKind)。
	Definition map[string]string // node type → chunk kind

	// NameField 从节点里找 symbol name 的 field 名字。tree-sitter 不同 grammar 约定不同
	// (Go / Python / Java 大多叫 "name",JS 的 function_declaration 也叫 "name")。
	// 多个候选名依次尝试,第一个命中返回。没一个命中 → symbol name 留空,不视为错。
	NameField []string
}

// defaultSpecs 返回内置的 6 种语言 spec。在 chunker.New 里注册。
func defaultSpecs() map[string]*languageSpec {
	return map[string]*languageSpec{
		"go": {
			Name:     "go",
			Language: golang.GetLanguage(),
			Definition: map[string]string{
				"function_declaration": ChunkKindFunction,
				"method_declaration":   ChunkKindMethod,
				"type_declaration":     ChunkKindClass, // 包括 struct/interface
			},
			NameField: []string{"name"},
		},
		"python": {
			Name:     "python",
			Language: python.GetLanguage(),
			Definition: map[string]string{
				"function_definition": ChunkKindFunction,
				"class_definition":    ChunkKindClass,
				// 装饰器装饰的函数在 Python AST 里根是 decorated_definition,包装了内部的 function_definition。
				// 作为顶层 chunk 切,保留装饰器上下文。
				"decorated_definition": ChunkKindFunction,
			},
			NameField: []string{"name"},
		},
		"typescript": {
			Name:     "typescript",
			Language: typescript.GetLanguage(),
			Definition: map[string]string{
				"function_declaration":  ChunkKindFunction,
				"class_declaration":     ChunkKindClass,
				"interface_declaration": ChunkKindClass,
				"method_definition":     ChunkKindMethod, // 顶层 method(在 object literal 里的)少见但可能
				// export 的 function/class:typescript AST 把 export 包在 export_statement 外层,这里把它也当作一个 chunk
				"export_statement": ChunkKindFunction, // kind 在下游 classifyKind 里按内容覆写
			},
			NameField: []string{"name"},
		},
		"javascript": {
			Name:     "javascript",
			Language: javascript.GetLanguage(),
			Definition: map[string]string{
				"function_declaration": ChunkKindFunction,
				"class_declaration":    ChunkKindClass,
				"method_definition":    ChunkKindMethod,
				// 同 TS,export 包住的顶层定义当独立 chunk
				"export_statement": ChunkKindFunction,
			},
			NameField: []string{"name"},
		},
		"java": {
			Name:     "java",
			Language: java.GetLanguage(),
			Definition: map[string]string{
				// Java 里 method 永远在 class 内部 —— 我们按 class 级切,class 整个作为一个 chunk
				// (包含所有 method),避免方法数多的 util class 被拆成几十个零碎 chunk。
				// 将来若要细化到 method,改成 DFS 遍历 + 展开 class_body 即可。
				"class_declaration":     ChunkKindClass,
				"interface_declaration": ChunkKindClass,
				"enum_declaration":      ChunkKindClass,
				"record_declaration":    ChunkKindClass,
			},
			NameField: []string{"name"},
		},
		"rust": {
			Name:     "rust",
			Language: rust.GetLanguage(),
			Definition: map[string]string{
				"function_item": ChunkKindFunction,
				"impl_item":     ChunkKindClass, // impl 块整体 —— 包含所有方法,避免 method 过细
				"struct_item":   ChunkKindClass,
				"enum_item":     ChunkKindClass,
				"trait_item":    ChunkKindClass,
				"mod_item":      ChunkKindClass, // inline mod 块整个作为 chunk
			},
			NameField: []string{"name"},
		},
	}
}
