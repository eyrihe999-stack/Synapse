// Package golang 是 code chunker 的 Go AST 后端。
//
// 走标准库 go/parser + go/ast,零 cgo。这是 PR-B 阶段唯一的后端;Python / TypeScript 等
// 由 PR-B2 的 tree-sitter 后端补齐。
//
// 解析策略:
//
//   - parser.ParseFile(comments | imports + skipObjectResolution):语法错时 ast.File 仍非 nil,
//     已认得的 decl 都进 File.Decls,我们保留这些(健壮性优先,坏文件不应炸整轮 sync)
//   - preamble = package decl + import 块 + file-level doc comment
//   - 顶层 FuncDecl → function / method(Recv != nil)
//   - 顶层 GenDecl(TypeSpec) → struct / interface / type alias 走 "class" 统一标签
//   - 顶层 GenDecl(VarSpec / ConstSpec) 不切独立 chunk(信息量太低,LLM 召回价值有限),
//     一并并入下一个符号或丢弃。一期简化:**全部丢弃**(测试覆盖)
//   - 注释关联:每个 Decl 用 doc.Doc(* CommentGroup)直接拿到紧贴的 doc comment;
//     行间散注释不进 chunk(避免双重计算)
//
// 行号:走 fileSet.Position 拿 1-based 行号,起点取 doc comment 的 Pos(无 doc 则取 decl Pos)。
package golang

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"

	codechunker "github.com/eyrihe999-stack/Synapse/internal/ingestion/chunker/code"
)

// Backend 满足 codechunker.LanguageBackend。
type Backend struct{}

// New 构造。无配置项,直接零值即可。
func New() *Backend { return &Backend{} }

// Language 见 codechunker.LanguageBackend。固定返 "go"。
func (*Backend) Language() string { return "go" }

// Parse 见 codechunker.LanguageBackend。
//
// 健壮性:语法错的源码 → parser 返 ast.File != nil 但带 error 的 case,我们仍走完整 walk;
// 完全无法解析(返 nil File)→ 退化:返 ParsedFile{Preamble: 整个 content},让上层 chunker
// 至少把它当 preamble 一段 emit,不是整个 sync 失败。
func (b *Backend) Parse(ctx context.Context, content []byte) (*codechunker.ParsedFile, error) {
	if len(content) == 0 {
		return nil, nil
	}
	fset := token.NewFileSet()
	// ParseComments:保留 doc comment;ImportsOnly=false 要完整 AST。
	// SkipObjectResolution:跳过名字解析,提速且不影响结构识别。
	file, err := parser.ParseFile(fset, "", content, parser.ParseComments|parser.SkipObjectResolution)
	if file == nil {
		// 完全无法 parse(极罕见)。降级:整个 content 当 preamble 一段返回,
		// 上层会 emit 一个 ContentType=preamble chunk,LLM 仍能基于全文检索。
		// 注意 err 不上抛 —— sync 单文件不应让整轮失败。
		return &codechunker.ParsedFile{
			Preamble:          string(content),
			PreambleLineStart: 1,
			PreambleLineEnd:   countLines(content),
		}, nil
	}
	// err != nil 的情况(部分语法错):file 仍可用,我们继续走;chunker 拿到的就是
	// "已认得部分"。正式日志由调用方决定;这里不打,避免大规模 sync 时刷屏。
	_ = err

	parsed := &codechunker.ParsedFile{}

	// ── Preamble:package + imports + file-level doc ────────────────────
	preamble, preStart, preEnd := buildPreamble(fset, file, content)
	parsed.Preamble = preamble
	parsed.PreambleLineStart = preStart
	parsed.PreambleLineEnd = preEnd

	// ── Symbols:顺序遍历 Decls ─────────────────────────────────────────
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if sym, ok := buildFuncSymbol(fset, d, content); ok {
				parsed.Symbols = append(parsed.Symbols, sym)
			}
		case *ast.GenDecl:
			// 只取 type:var / const / import 不单独切 chunk(信息密度低)
			if d.Tok != token.TYPE {
				continue
			}
			parsed.Symbols = append(parsed.Symbols, buildTypeSymbols(fset, d, content)...)
		}
	}

	return parsed, nil
}

// ─── preamble ───────────────────────────────────────────────────────────────

// buildPreamble 取 package decl 起点 → 最后一个 import 块结束 之间的字节(含 file-level doc)。
//
// 边界规则:
//   - 起点:file.Doc(若有 file-level doc 在 package 之前) → 否则 file.Package
//   - 终点:最后一个 import GenDecl 的 End();无 import → package decl 的 End()
//   - file-level doc 是不绑定到任何 decl 的、紧跟 package 之前的 comment,go/parser 会放进 file.Doc
func buildPreamble(fset *token.FileSet, file *ast.File, content []byte) (string, int, int) {
	startPos := file.Package
	if file.Doc != nil && file.Doc.Pos() < startPos {
		startPos = file.Doc.Pos()
	}
	endPos := file.Name.End()
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.IMPORT {
			break // 第一个非 import decl 即停止
		}
		endPos = gd.End()
	}
	startOff := fset.Position(startPos).Offset
	endOff := fset.Position(endPos).Offset
	if startOff < 0 || endOff > len(content) || startOff > endOff {
		return "", 0, 0
	}
	body := strings.TrimRight(string(content[startOff:endOff]), "\n")
	if body == "" {
		return "", 0, 0
	}
	return body, fset.Position(startPos).Line, fset.Position(endPos).Line
}

// ─── functions / methods ────────────────────────────────────────────────────

// buildFuncSymbol 顶层 FuncDecl → Symbol。
//
//   - Doc 在的话(d.Doc 非 nil)起点取 doc.Pos();否则取 d.Pos()
//   - Recv != nil → method;否则 function
//   - Signature 走 ast.NodeBody 不含;手动拼:func Name(Params) Results
//
// 异常:函数体是 nil(声明分离)→ 仍 emit 一个签名 chunk,Body = signature。
func buildFuncSymbol(fset *token.FileSet, d *ast.FuncDecl, content []byte) (codechunker.Symbol, bool) {
	startPos := d.Pos()
	if d.Doc != nil && d.Doc.Pos() < startPos {
		startPos = d.Doc.Pos()
	}
	endPos := d.End()

	startOff := fset.Position(startPos).Offset
	endOff := fset.Position(endPos).Offset
	if startOff < 0 || endOff > len(content) || startOff > endOff {
		return codechunker.Symbol{}, false
	}

	kind := codechunker.KindFunction
	if d.Recv != nil {
		kind = codechunker.KindMethod
	}

	sig := buildFuncSignature(d)
	body := strings.TrimRight(string(content[startOff:endOff]), "\n")

	return codechunker.Symbol{
		Name:      funcName(d),
		Signature: sig,
		Kind:      kind,
		Body:      body,
		LineStart: fset.Position(startPos).Line,
		LineEnd:   fset.Position(endPos).Line,
	}, true
}

// funcName 返回函数名。Recv 上有 method 名 = "Type.Method" 形式(便于检索唯一定位)。
func funcName(d *ast.FuncDecl) string {
	if d.Name == nil {
		return ""
	}
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return d.Name.Name
	}
	// Recv 形如 (r *Type) 或 (Type),取 Type 名
	recv := d.Recv.List[0].Type
	recvName := exprText(recv)
	// 去掉指针 *
	recvName = strings.TrimPrefix(recvName, "*")
	return recvName + "." + d.Name.Name
}

// buildFuncSignature 拼"func [(Recv)] Name[TypeParams](Params) [Results]" 形式。
//
// 用 ast 字段重组,不走 go/printer.Fprint —— 后者需要 fset + 整个 FuncDecl 副本(去 Body),
// 复杂度不值得。代价:复杂泛型 / 嵌套类型在 exprText 里有 fallback 规则会丢精度,
// 但**不影响 chunk Body 的精确性**(Body 永远是源码原文),只让"独立检索签名"略糙一些。
func buildFuncSignature(d *ast.FuncDecl) string {
	if d.Body == nil {
		// 声明分离 / interface method:整 Decl 就是签名
		return strings.TrimSpace(exprText(d))
	}
	var sb strings.Builder
	sb.WriteString("func ")
	if d.Recv != nil && len(d.Recv.List) > 0 {
		sb.WriteString("(")
		sb.WriteString(fieldListText(d.Recv))
		sb.WriteString(") ")
	}
	if d.Name != nil {
		sb.WriteString(d.Name.Name)
	}
	if d.Type.TypeParams != nil {
		sb.WriteString("[")
		sb.WriteString(fieldListText(d.Type.TypeParams))
		sb.WriteString("]")
	}
	sb.WriteString("(")
	sb.WriteString(fieldListText(d.Type.Params))
	sb.WriteString(")")
	if d.Type.Results != nil && len(d.Type.Results.List) > 0 {
		sb.WriteString(" ")
		if needsResultParens(d.Type.Results) {
			sb.WriteString("(")
			sb.WriteString(fieldListText(d.Type.Results))
			sb.WriteString(")")
		} else {
			sb.WriteString(fieldListText(d.Type.Results))
		}
	}
	return sb.String()
}

// needsResultParens go 语法上 results 单返且无 name 时可省括号;有 name 或多返必须括号。
func needsResultParens(fl *ast.FieldList) bool {
	if fl == nil {
		return false
	}
	if len(fl.List) > 1 {
		return true
	}
	first := fl.List[0]
	return len(first.Names) > 0
}

// fieldListText 把 *ast.FieldList 拼成"name1, name2 Type, name3 Type2"形式。
// 用 exprText 做 Type 节点的字符串化,没有 type 写死的复杂 case 不展开(注释中 generic 体现)。
func fieldListText(fl *ast.FieldList) string {
	if fl == nil || len(fl.List) == 0 {
		return ""
	}
	parts := make([]string, 0, len(fl.List))
	for _, f := range fl.List {
		var seg strings.Builder
		for i, n := range f.Names {
			if i > 0 {
				seg.WriteString(", ")
			}
			seg.WriteString(n.Name)
		}
		if f.Type != nil {
			if seg.Len() > 0 {
				seg.WriteString(" ")
			}
			seg.WriteString(exprText(f.Type))
		}
		parts = append(parts, seg.String())
	}
	return strings.Join(parts, ", ")
}

// exprText 把 ast.Expr 转成短字符串。对内置 / star / selector 处理;复杂 generic 走 fallback "_"。
//
// 标准做法是用 go/printer.Fprint 走 fset,但本函数用在签名拼接里,fset 不一定在手;
// 错处理用 fallback 而不是 panic,签名变粗一点不影响 chunk 内容(Body 始终是源码原文)。
func exprText(e ast.Node) string {
	if e == nil {
		return ""
	}
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.StarExpr:
		return "*" + exprText(v.X)
	case *ast.SelectorExpr:
		return exprText(v.X) + "." + v.Sel.Name
	case *ast.ArrayType:
		return "[" + exprText(v.Len) + "]" + exprText(v.Elt)
	case *ast.MapType:
		return "map[" + exprText(v.Key) + "]" + exprText(v.Value)
	case *ast.ChanType:
		return "chan " + exprText(v.Value)
	case *ast.FuncType:
		return "func(" + fieldListText(v.Params) + ")"
	case *ast.InterfaceType:
		return "interface{...}"
	case *ast.StructType:
		return "struct{...}"
	case *ast.Ellipsis:
		return "..." + exprText(v.Elt)
	case *ast.IndexExpr:
		return exprText(v.X) + "[" + exprText(v.Index) + "]"
	case *ast.IndexListExpr:
		ts := make([]string, 0, len(v.Indices))
		for _, t := range v.Indices {
			ts = append(ts, exprText(t))
		}
		return exprText(v.X) + "[" + strings.Join(ts, ", ") + "]"
	case *ast.BasicLit:
		return v.Value
	}
	return "" // 复杂 expr fallback,签名上看不到具体类型不致命
}

// ─── types (struct / interface / type alias) ────────────────────────────────

// buildTypeSymbols 一个 type GenDecl 可能有多个 spec(`type ( A int; B struct{}; C interface{} )`),
// 为每一条 TypeSpec emit 一个 Symbol。注释优先用 spec.Doc,无则 fallback 到 GenDecl.Doc。
func buildTypeSymbols(fset *token.FileSet, gd *ast.GenDecl, content []byte) []codechunker.Symbol {
	out := make([]codechunker.Symbol, 0, len(gd.Specs))
	for _, spec := range gd.Specs {
		ts, ok := spec.(*ast.TypeSpec)
		if !ok || ts.Name == nil {
			continue
		}
		startPos := ts.Pos()
		if ts.Doc != nil && ts.Doc.Pos() < startPos {
			startPos = ts.Doc.Pos()
		}
		// 没有 spec.Doc 但有 GenDecl.Doc 且 GenDecl 非分组(`type X int` 而非 `type ( ... )`)
		if ts.Doc == nil && gd.Doc != nil && gd.Lparen == token.NoPos {
			startPos = gd.Doc.Pos()
		}
		endPos := ts.End()

		startOff := fset.Position(startPos).Offset
		endOff := fset.Position(endPos).Offset
		if startOff < 0 || endOff > len(content) || startOff > endOff {
			continue
		}
		body := strings.TrimRight(string(content[startOff:endOff]), "\n")

		// TypeSpec.Pos 指 Name(Reader / User 等),不含 "type" 关键字 ——
		// 单 spec / 分组 type 都一样。补前缀使其作为独立 chunk 仍是合法 Go 片段。
		// 如果有 doc comment 在开头(我们已 startPos 推到 doc.Pos),前缀加在 doc 之后。
		body = insertTypeKeyword(body)

		out = append(out, codechunker.Symbol{
			Name:      ts.Name.Name,
			Signature: "", // type 不展示签名,完整定义就在 body 里
			Kind:      codechunker.KindClass,
			Body:      body,
			LineStart: fset.Position(startPos).Line,
			LineEnd:   fset.Position(endPos).Line,
		})
	}
	return out
}

// insertTypeKeyword 在 type body 的 Name 之前插入 "type "。
// body 形如:`Reader interface{...}` 或 `// Doc...\nReader interface{...}`(含 doc)。
// 实现:跳过开头连续的注释 / 空行,找到第一行真实声明行,在它前面插。
func insertTypeKeyword(body string) string {
	if strings.HasPrefix(body, "type ") {
		return body
	}
	lines := strings.Split(body, "\n")
	for i, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") {
			continue
		}
		if !strings.HasPrefix(trimmed, "type ") {
			lines[i] = "type " + ln
		}
		return strings.Join(lines, "\n")
	}
	// 兜底:都是注释行,直接前置(罕见 case)
	return "type " + body
}

// ─── utils ─────────────────────────────────────────────────────────────────

// countLines 计算字节流的行数。空 → 0;末尾有 \n 不重复计行。
func countLines(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	n := bytes.Count(b, []byte("\n"))
	if b[len(b)-1] != '\n' {
		n++
	}
	return n
}

