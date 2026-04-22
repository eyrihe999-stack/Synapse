// doc.go — sayso-lint 模块文档提取子命令。
//
// 用法:
//
//	sayso-lint doc <module-name>
//
// 用 AST + 类型系统从源码中提取模块结构化信息，输出 JSON 到 stdout。
// 提取内容：路由表、错误码、哨兵错误、模型、DTO、Service、Repository、事务点、加锁点、跨模块调用者。
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

// ── 输出数据结构 ────────────────────────────────────────────────────────────

// ModuleDoc 模块文档提取的完整输出。
type ModuleDoc struct {
	Module          string           `json:"module"`
	Routes          []RouteInfo      `json:"routes"`
	ErrorCodes      []ErrorCodeInfo  `json:"error_codes"`
	Sentinels       []SentinelInfo   `json:"sentinels"`
	Models          []ModelInfo      `json:"models"`
	DTOs            []StructInfo     `json:"dtos"`
	Services        []TypeMethodDoc  `json:"services"`
	Repositories    []TypeMethodDoc  `json:"repositories"`
	Transactions    []LocationInfo   `json:"transactions"`
	Locks           []LocationInfo   `json:"locks"`
	ExternalCallers []ExternalCaller     `json:"external_callers"`
	InterfaceDeps   []InterfaceDep       `json:"interface_deps"`
	CallerSources   []CallerModuleSource `json:"caller_sources"`
}

// ExternalCaller 其他模块对本模块导出函数的单次调用。
type ExternalCaller struct {
	CallerModule   string `json:"caller_module"`
	CallerFile     string `json:"caller_file"`
	CallerFunction string `json:"caller_function"`
	Target         string `json:"target"`
	Line           int    `json:"line"`
	ErrorHandling  string `json:"error_handling"` // checked / ignored / logged_and_ignored
}

// InterfaceDep 其他模块通过接口注入依赖本模块的 service 类型。
type InterfaceDep struct {
	CallerModule  string   `json:"caller_module"`
	InterfaceName string   `json:"interface_name"`
	InterfaceFile string   `json:"interface_file"`
	Methods       []string `json:"methods"`
	ImplementedBy string   `json:"implemented_by"` // 目标模块的 service 类型名
}

// CallerModuleSource 一个外部调用方模块的完整源码（包级别 + 嵌套依赖包）。
type CallerModuleSource struct {
	Module   string          `json:"module"`
	Packages []PackageSource `json:"packages"`
}

// PackageSource 一个包的所有源文件内容。
type PackageSource struct {
	Path  string       `json:"path"`
	Files []FileSource `json:"files"`
}

// FileSource 单个源文件。
type FileSource struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// RouteInfo 一条路由注册信息。
type RouteInfo struct {
	Method  string `json:"method"`
	Path    string `json:"path"`
	Handler string `json:"handler"`
	File    string `json:"file"`
	Line    int    `json:"line"`
}

// ErrorCodeInfo 一个错误码常量。
type ErrorCodeInfo struct {
	Name    string `json:"name"`
	Value   int64  `json:"value"`
	Comment string `json:"comment,omitempty"`
}

// SentinelInfo 一个哨兵错误变量。
type SentinelInfo struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Comment string `json:"comment,omitempty"`
}

// ModelInfo 一个数据模型及其字段。
type ModelInfo struct {
	Name   string      `json:"name"`
	Table  string      `json:"table"`
	File   string      `json:"file"`
	Line   int         `json:"line"`
	Fields []FieldInfo `json:"fields"`
}

// StructInfo 一个 DTO/请求/响应结构体。
type StructInfo struct {
	Name    string      `json:"name"`
	Comment string      `json:"comment,omitempty"`
	File    string      `json:"file"`
	Line    int         `json:"line"`
	Fields  []FieldInfo `json:"fields"`
	Example string      `json:"example,omitempty"` // 自动生成的 JSON 示例
}

// FieldInfo 结构体字段。
type FieldInfo struct {
	Name string `json:"name"`
	Type string `json:"type"`
	JSON string `json:"json,omitempty"`
	GORM string `json:"gorm,omitempty"`
	Tag  string `json:"tag,omitempty"`
}

// TypeMethodDoc 一个类型（struct/interface）及其方法。
type TypeMethodDoc struct {
	Name    string      `json:"name"`
	Kind    string      `json:"kind"` // "struct" or "interface"
	File    string      `json:"file"`
	Line    int         `json:"line"`
	Fields  []FieldInfo `json:"fields,omitempty"`
	Methods []MethodDoc `json:"methods"`
}

// MethodDoc 一个方法签名及其调用链。
type MethodDoc struct {
	Name    string     `json:"name"`
	Params  []string   `json:"params"`
	Returns []string   `json:"returns"`
	Comment string     `json:"comment,omitempty"`
	File    string     `json:"file,omitempty"`
	Line    int        `json:"line,omitempty"`
	Calls   []CallInfo `json:"calls,omitempty"`
}

// CallInfo 方法体内的一次关键调用。
type CallInfo struct {
	Target string `json:"target"` // 调用目标（如 repo.FindActivePlan、ApplyEntitlements）
	Line   int    `json:"line"`
	Kind   string `json:"kind"` // repo / service / tx / function
}

// LocationInfo 代码中的一个位置（事务/锁）。
type LocationInfo struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Function string `json:"function"`
	Detail   string `json:"detail,omitempty"`
}

// ── 子命令入口 ──────────────────────────────────────────────────────────────

func runDocExtract(module string) {
	codeRoot := filepath.Join("internal", module)

	if _, err := os.Stat(codeRoot); err != nil {
		fmt.Fprintf(os.Stderr, "错误: 目录 %s 不存在\n", codeRoot)
		os.Exit(2)
	}

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:   ".",
		Tests: false,
	}
	pkgs, err := packages.Load(cfg, "./"+codeRoot+"/...")
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 加载包失败: %v\n", err)
		os.Exit(1)
	}

	cwd, _ := os.Getwd()
	var passes []*CheckPass
	seen := make(map[string]bool)

	for _, pkg := range pkgs {
		if pkg.TypesInfo == nil {
			continue
		}
		pass := &CheckPass{
			Fset:      pkg.Fset,
			Pkg:       pkg.Types,
			TypesInfo: pkg.TypesInfo,
			Module:    module,
			CodeRoot:  codeRoot,
		}
		for i, file := range pkg.Syntax {
			if i >= len(pkg.CompiledGoFiles) {
				break
			}
			absPath := pkg.CompiledGoFiles[i]
			relPath, _ := filepath.Rel(cwd, absPath)
			if !strings.HasPrefix(relPath, codeRoot) || seen[relPath] {
				continue
			}
			if strings.HasSuffix(relPath, "_test.go") || isGenerated(file) || strings.Contains(relPath, "/cmd/") {
				continue
			}
			seen[relPath] = true
			pass.Files = append(pass.Files, file)
			pass.FilePaths = append(pass.FilePaths, relPath)
		}
		if len(pass.Files) > 0 {
			passes = append(passes, pass)
		}
	}

	doc := ModuleDoc{Module: module}
	doc.Routes = docExtractRoutes(passes)
	doc.ErrorCodes, doc.Sentinels = docExtractErrors(passes, codeRoot)
	doc.Models = docExtractModels(passes)
	doc.DTOs = docExtractDTOs(passes)
	doc.Services = docExtractServices(passes)
	doc.Repositories = docExtractRepositories(passes)
	doc.Transactions = docExtractTransactions(passes)
	doc.Locks = docExtractLocks(passes)
	doc.ExternalCallers = docExtractExternalCallers(module)
	doc.InterfaceDeps = docExtractInterfaceDeps(passes, module)
	doc.CallerSources = docExtractCallerSources(module, doc.ExternalCallers, doc.InterfaceDeps)

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(doc)

	// 摘要 → stderr
	totalMethods := 0
	for _, s := range doc.Services {
		totalMethods += len(s.Methods)
	}
	repoMethods := 0
	for _, r := range doc.Repositories {
		repoMethods += len(r.Methods)
	}
	fmt.Fprintf(os.Stderr, "\n═══ %s 模块文档提取 ═══\n", module)
	fmt.Fprintf(os.Stderr, "路由:        %d 个\n", len(doc.Routes))
	fmt.Fprintf(os.Stderr, "错误码:      %d 个\n", len(doc.ErrorCodes))
	fmt.Fprintf(os.Stderr, "哨兵错误:    %d 个\n", len(doc.Sentinels))
	fmt.Fprintf(os.Stderr, "模型:        %d 个\n", len(doc.Models))
	fmt.Fprintf(os.Stderr, "DTO:         %d 个\n", len(doc.DTOs))
	fmt.Fprintf(os.Stderr, "Service:     %d 个（%d 方法）\n", len(doc.Services), totalMethods)
	fmt.Fprintf(os.Stderr, "Repository:  %d 个（%d 方法）\n", len(doc.Repositories), repoMethods)
	fmt.Fprintf(os.Stderr, "事务点:      %d 处\n", len(doc.Transactions))
	fmt.Fprintf(os.Stderr, "加锁点:      %d 处\n", len(doc.Locks))

	// 统计外部调用者的模块数
	callerModules := make(map[string]bool)
	for _, c := range doc.ExternalCallers {
		callerModules[c.CallerModule] = true
	}
	for _, dep := range doc.InterfaceDeps {
		callerModules[dep.CallerModule] = true
	}
	totalCallerFiles := 0
	for _, cs := range doc.CallerSources {
		for _, pkg := range cs.Packages {
			totalCallerFiles += len(pkg.Files)
		}
	}
	fmt.Fprintf(os.Stderr, "外部调用:    %d 处（来自 %d 个模块，%d 个源文件已加载）\n",
		len(doc.ExternalCallers), len(callerModules), totalCallerFiles)
	fmt.Fprintf(os.Stderr, "接口依赖:    %d 个\n", len(doc.InterfaceDeps))
}

// ── 路由提取 ────────────────────────────────────────────────────────────────

// docExtractRoutes 从 handler 包的 Register*Routes 函数中提取路由注册信息。
func docExtractRoutes(passes []*CheckPass) []RouteInfo {
	var routes []RouteInfo
	for _, pass := range passes {
		for i, file := range pass.Files {
			path := pass.FilePaths[i]
			if !isHandlerPkg(path) {
				continue
			}
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				if !strings.HasPrefix(fd.Name.Name, "Register") || !strings.HasSuffix(fd.Name.Name, "Routes") {
					continue
				}
				routes = append(routes, docExtractRoutesFromFunc(fd, pass, path)...)
			}
		}
	}
	return routes
}

var httpMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "DELETE": true, "PATCH": true,
	"HEAD": true, "OPTIONS": true, "Any": true,
}

func docExtractRoutesFromFunc(fd *ast.FuncDecl, pass *CheckPass, filePath string) []RouteInfo {
	// 收集 Group 调用：varName → prefix
	groupPrefixes := make(map[string]string) // variable name → accumulated prefix

	ast.Inspect(fd.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) == 0 || len(assign.Rhs) == 0 {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Group" || len(call.Args) == 0 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		prefix, _ := strconv.Unquote(lit.Value)

		// 查找父级 group 前缀
		if parentIdent, ok := sel.X.(*ast.Ident); ok {
			if parentPrefix, exists := groupPrefixes[parentIdent.Name]; exists {
				prefix = parentPrefix + prefix
			}
		}

		if ident, ok := assign.Lhs[0].(*ast.Ident); ok {
			groupPrefixes[ident.Name] = prefix
		}
		return true
	})

	// 收集路由注册
	var routes []RouteInfo
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !httpMethods[sel.Sel.Name] || len(call.Args) < 2 {
			return true
		}

		receiverIdent, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		prefix := groupPrefixes[receiverIdent.Name]

		pathLit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || pathLit.Kind != token.STRING {
			return true
		}
		routePath, _ := strconv.Unquote(pathLit.Value)

		handler := ""
		if handlerSel, ok := call.Args[1].(*ast.SelectorExpr); ok {
			handler = handlerSel.Sel.Name
		}

		routes = append(routes, RouteInfo{
			Method:  sel.Sel.Name,
			Path:    prefix + routePath,
			Handler: handler,
			File:    filePath,
			Line:    lineOf(pass.Fset, call.Pos()),
		})
		return true
	})
	return routes
}

// ── 错误码与哨兵错误提取 ────────────────────────────────────────────────────

func docExtractErrors(passes []*CheckPass, codeRoot string) ([]ErrorCodeInfo, []SentinelInfo) {
	var codes []ErrorCodeInfo
	var sentinels []SentinelInfo

	for _, pass := range passes {
		for i, file := range pass.Files {
			path := pass.FilePaths[i]
			if filepath.Base(path) != "errors.go" || filepath.Dir(path) != codeRoot {
				continue
			}
			for _, decl := range file.Decls {
				genDecl, ok := decl.(*ast.GenDecl)
				if !ok {
					continue
				}
				// 取 GenDecl 上方的分组注释
				groupComment := ""
				if genDecl.Doc != nil {
					groupComment = strings.TrimSpace(genDecl.Doc.Text())
				}

				switch genDecl.Tok {
				case token.CONST:
					for _, spec := range genDecl.Specs {
						vs := spec.(*ast.ValueSpec)
						for j, name := range vs.Names {
							comment := docSpecComment(vs, groupComment)
							val := int64(0)
							if j < len(vs.Values) {
								if lit, ok := vs.Values[j].(*ast.BasicLit); ok && lit.Kind == token.INT {
									val, _ = strconv.ParseInt(lit.Value, 10, 64)
								}
							}
							codes = append(codes, ErrorCodeInfo{
								Name: name.Name, Value: val, Comment: comment,
							})
						}
					}
				case token.VAR:
					for _, spec := range genDecl.Specs {
						vs := spec.(*ast.ValueSpec)
						for j, name := range vs.Names {
							comment := docSpecComment(vs, groupComment)
							val := ""
							if j < len(vs.Values) {
								if call, ok := vs.Values[j].(*ast.CallExpr); ok && len(call.Args) > 0 {
									if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
										val, _ = strconv.Unquote(lit.Value)
									}
								}
							}
							sentinels = append(sentinels, SentinelInfo{
								Name: name.Name, Value: val, Comment: comment,
							})
						}
					}
				}
			}
		}
	}
	return codes, sentinels
}

// docSpecComment 提取 ValueSpec 的行尾注释或 Doc 注释，fallback 到分组注释。
func docSpecComment(vs *ast.ValueSpec, groupComment string) string {
	if vs.Comment != nil {
		return strings.TrimSpace(vs.Comment.Text())
	}
	if vs.Doc != nil {
		return strings.TrimSpace(vs.Doc.Text())
	}
	return groupComment
}

// ── 模型提取 ────────────────────────────────────────────────────────────────

func docExtractModels(passes []*CheckPass) []ModelInfo {
	// 第一遍：收集 TableName() 方法 → struct name → table name
	tableNames := make(map[string]string)
	for _, pass := range passes {
		for _, file := range pass.Files {
			if file.Name.Name != "model" {
				continue
			}
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Recv == nil || fd.Name.Name != "TableName" || fd.Body == nil {
					continue
				}
				recv := extractReceiver(fd)
				// 提取 return "table_name"
				for _, stmt := range fd.Body.List {
					ret, ok := stmt.(*ast.ReturnStmt)
					if !ok || len(ret.Results) == 0 {
						continue
					}
					if lit, ok := ret.Results[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						tableName, _ := strconv.Unquote(lit.Value)
						tableNames[recv] = tableName
					}
				}
			}
		}
	}

	// 第二遍：收集 struct 定义
	var models []ModelInfo
	for _, pass := range passes {
		for i, file := range pass.Files {
			path := pass.FilePaths[i]
			if file.Name.Name != "model" {
				continue
			}
			for _, decl := range file.Decls {
				genDecl, ok := decl.(*ast.GenDecl)
				if !ok || genDecl.Tok != token.TYPE {
					continue
				}
				for _, spec := range genDecl.Specs {
					ts := spec.(*ast.TypeSpec)
					st, ok := ts.Type.(*ast.StructType)
					if !ok {
						continue
					}
					table := tableNames[ts.Name.Name]
					if table == "" {
						continue // 没有 TableName 的 struct 不是 model
					}
					m := ModelInfo{
						Name:   ts.Name.Name,
						Table:  table,
						File:   path,
						Line:   lineOf(pass.Fset, ts.Pos()),
						Fields: docExtractFields(st, pass),
					}
					models = append(models, m)
				}
			}
		}
	}
	return models
}

// ── DTO 提取 ────────────────────────────────────────────────────────────────

func docExtractDTOs(passes []*CheckPass) []StructInfo {
	var dtos []StructInfo
	for _, pass := range passes {
		for i, file := range pass.Files {
			path := pass.FilePaths[i]
			if file.Name.Name != "dto" {
				continue
			}
			for _, decl := range file.Decls {
				genDecl, ok := decl.(*ast.GenDecl)
				if !ok || genDecl.Tok != token.TYPE {
					continue
				}
				for _, spec := range genDecl.Specs {
					ts := spec.(*ast.TypeSpec)
					st, ok := ts.Type.(*ast.StructType)
					if !ok {
						continue
					}
					comment := ""
					if ts.Doc != nil {
						comment = strings.TrimSpace(ts.Doc.Text())
					} else if genDecl.Doc != nil {
						comment = strings.TrimSpace(genDecl.Doc.Text())
					}
					fields := docExtractFields(st, pass)
					dtos = append(dtos, StructInfo{
						Name:    ts.Name.Name,
						Comment: comment,
						File:    path,
						Line:    lineOf(pass.Fset, ts.Pos()),
						Fields:  fields,
						Example: generateJSONExample(fields),
					})
				}
			}
		}
	}
	return dtos
}

// ── 字段提取 ────────────────────────────────────────────────────────────────

func docExtractFields(st *ast.StructType, pass *CheckPass) []FieldInfo {
	if st.Fields == nil {
		return nil
	}
	var fields []FieldInfo
	for _, field := range st.Fields.List {
		typStr := nodeStr(pass.Fset, field.Type)
		jsonTag, gormTag, rawTag := "", "", ""
		if field.Tag != nil {
			raw, _ := strconv.Unquote(field.Tag.Value)
			rawTag = raw
			tag := reflect.StructTag(raw)
			jsonTag = strings.Split(string(tag.Get("json")), ",")[0]
			gormTag = string(tag.Get("gorm"))
		}
		if len(field.Names) == 0 {
			// 嵌入字段
			fields = append(fields, FieldInfo{Name: typStr, Type: typStr, JSON: jsonTag, GORM: gormTag, Tag: rawTag})
		} else {
			for _, name := range field.Names {
				fields = append(fields, FieldInfo{Name: name.Name, Type: typStr, JSON: jsonTag, GORM: gormTag, Tag: rawTag})
			}
		}
	}
	return fields
}

// ── Service 提取 ────────────────────────────────────────────────────────────

func docExtractServices(passes []*CheckPass) []TypeMethodDoc {
	// 收集 service 包中的 struct 类型
	typeMap := make(map[string]*TypeMethodDoc)
	var order []string

	for _, pass := range passes {
		for i, file := range pass.Files {
			path := pass.FilePaths[i]
			if !isServicePkg(path) {
				continue
			}
			// 收集 struct 定义
			for _, decl := range file.Decls {
				genDecl, ok := decl.(*ast.GenDecl)
				if !ok || genDecl.Tok != token.TYPE {
					continue
				}
				for _, spec := range genDecl.Specs {
					ts := spec.(*ast.TypeSpec)
					if _, ok := ts.Type.(*ast.StructType); !ok {
						continue
					}
					if !ts.Name.IsExported() {
						continue
					}
					name := ts.Name.Name
					if _, exists := typeMap[name]; !exists {
						st := ts.Type.(*ast.StructType)
						typeMap[name] = &TypeMethodDoc{
							Name:   name,
							Kind:   "struct",
							File:   path,
							Line:   lineOf(pass.Fset, ts.Pos()),
							Fields: docExtractFields(st, pass),
						}
						order = append(order, name)
					}
				}
			}
		}
	}

	// 收集方法
	for _, pass := range passes {
		for i, file := range pass.Files {
			path := pass.FilePaths[i]
			if !isServicePkg(path) {
				continue
			}
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Recv == nil || !fd.Name.IsExported() {
					continue
				}
				recv := extractReceiver(fd)
				td, exists := typeMap[recv]
				if !exists {
					continue
				}
				comment := ""
				if fd.Doc != nil {
					// 取第一行
					lines := strings.Split(strings.TrimSpace(fd.Doc.Text()), "\n")
					comment = lines[0]
				}
				td.Methods = append(td.Methods, MethodDoc{
					Name:    fd.Name.Name,
					Params:  docExtractParamTypes(fd, pass),
					Returns: extractTypes(fd.Type.Results, pass),
					Comment: comment,
					File:    path,
					Line:    lineOf(pass.Fset, fd.Pos()),
					Calls:   docExtractCalls(fd, pass),
				})
			}
		}
	}

	var result []TypeMethodDoc
	for _, name := range order {
		td := typeMap[name]
		if len(td.Methods) > 0 {
			result = append(result, *td)
		}
	}
	return result
}

// docExtractParamTypes 提取函数参数类型列表，用 name:type 格式。
func docExtractParamTypes(fd *ast.FuncDecl, pass *CheckPass) []string {
	if fd.Type.Params == nil {
		return nil
	}
	var params []string
	for _, field := range fd.Type.Params.List {
		typStr := nodeStr(pass.Fset, field.Type)
		if len(field.Names) == 0 {
			params = append(params, typStr)
		} else {
			for _, name := range field.Names {
				params = append(params, name.Name+" "+typStr)
			}
		}
	}
	return params
}

// ── 调用链提取 ──────────────────────────────────────────────────────────────

// docExtractCalls 从函数体中提取关键调用（repo 方法、包级函数、WithTx）。
func docExtractCalls(fd *ast.FuncDecl, pass *CheckPass) []CallInfo {
	if fd.Body == nil {
		return nil
	}
	var calls []CallInfo
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		line := lineOf(pass.Fset, call.Pos())

		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			methodName := fn.Sel.Name

			// a.b.Method() — 链式调用（如 s.repo.FindActivePlan）
			if inner, ok := fn.X.(*ast.SelectorExpr); ok {
				receiver := inner.Sel.Name
				target := receiver + "." + methodName
				kind := docClassifyCall(receiver, methodName)
				if kind != "" {
					calls = append(calls, CallInfo{Target: target, Line: line, Kind: kind})
					return true
				}
			}

			// a.Method() — 直接方法调用（如 s.GetEntitlements、txRepo.FindPlan）
			if ident, ok := fn.X.(*ast.Ident); ok {
				target := ident.Name + "." + methodName
				kind := docClassifyCall(ident.Name, methodName)
				if kind != "" {
					calls = append(calls, CallInfo{Target: target, Line: line, Kind: kind})
					return true
				}
			}

			// GORM 链式调用兜底: tx.Model().Where().Updates() 等
			if root, ok := resolveChainReceiver(fn.X); ok {
				kind := docClassifyCall(root, methodName)
				if kind != "" {
					calls = append(calls, CallInfo{Target: root + "." + methodName, Line: line, Kind: kind})
				}
			}

		case *ast.Ident:
			// 包级函数调用（如 ApplyEntitlements、EnsureExpiredSetToFree）
			if fn.IsExported() && fn.Name != "Error" && fn.Name != "New" {
				calls = append(calls, CallInfo{Target: fn.Name, Line: line, Kind: "function"})
			}
		}
		return true
	})
	return calls
}

// docClassifyCall 根据调用的接收器名和方法名判断调用类型。
func docClassifyCall(receiver, method string) string {
	// WithTx → 事务
	if method == "WithTx" {
		return "tx"
	}
	// repo/txRepo/db/tx 上的方法 → 数据访问
	switch {
	case strings.Contains(strings.ToLower(receiver), "repo"):
		return "repo"
	case receiver == "db" || receiver == "tx":
		return "repo"
	}
	// s.xxx / svc.xxx → service 内部调用（含未导出 self-method）
	if receiver == "s" || receiver == "svc" {
		if ast.IsExported(method) {
			return "service"
		}
		return "self"
	}
	// xxxService / xxxSvc 上的方法 → 服务依赖调用
	lower := strings.ToLower(receiver)
	if strings.Contains(lower, "service") || strings.HasSuffix(lower, "svc") {
		return "service"
	}
	// logger / gateway 等不记录
	return ""
}

// resolveChainReceiver 沿 GORM 链式调用向内遍历，找到可识别的 receiver。
//
// 例如 tx.Model(&X{}).Where("...").Updates(m) 会依次展开:
//
//	CallExpr(Where) → SelectorExpr("Where") → CallExpr(Model) → SelectorExpr("Model") → Ident("tx")
//
// 遍历过程中若遇到 db/tx/repo 等已知中间节点，提前返回。
func resolveChainReceiver(expr ast.Expr) (string, bool) {
	for {
		switch e := expr.(type) {
		case *ast.CallExpr:
			expr = e.Fun
		case *ast.SelectorExpr:
			name := e.Sel.Name
			lower := strings.ToLower(name)
			if name == "db" || name == "tx" || strings.Contains(lower, "repo") {
				return name, true
			}
			if ident, ok := e.X.(*ast.Ident); ok {
				return ident.Name, true
			}
			expr = e.X
		case *ast.Ident:
			return e.Name, true
		default:
			return "", false
		}
	}
}

// ── Repository 提取 ────────────────────────────────────────────────────────

func docExtractRepositories(passes []*CheckPass) []TypeMethodDoc {
	var repos []TypeMethodDoc
	for _, pass := range passes {
		for i, file := range pass.Files {
			path := pass.FilePaths[i]
			if !isRepoPkg(path) {
				continue
			}
			for _, decl := range file.Decls {
				genDecl, ok := decl.(*ast.GenDecl)
				if !ok || genDecl.Tok != token.TYPE {
					continue
				}
				for _, spec := range genDecl.Specs {
					ts := spec.(*ast.TypeSpec)
					iface, ok := ts.Type.(*ast.InterfaceType)
					if !ok || iface.Methods == nil || !ts.Name.IsExported() {
						continue
					}
					td := TypeMethodDoc{
						Name: ts.Name.Name,
						Kind: "interface",
						File: path,
						Line: lineOf(pass.Fset, ts.Pos()),
					}
					for _, method := range iface.Methods.List {
						if len(method.Names) == 0 {
							continue // 嵌入接口
						}
						ft, ok := method.Type.(*ast.FuncType)
						if !ok {
							continue
						}
						comment := ""
						if method.Doc != nil {
							lines := strings.Split(strings.TrimSpace(method.Doc.Text()), "\n")
							comment = lines[0]
						} else if method.Comment != nil {
							comment = strings.TrimSpace(method.Comment.Text())
						}
						td.Methods = append(td.Methods, MethodDoc{
							Name:    method.Names[0].Name,
							Params:  docExtractFieldListTypes(ft.Params, pass),
							Returns: docExtractFieldListTypes(ft.Results, pass),
							Comment: comment,
						})
					}
					if len(td.Methods) > 0 {
						repos = append(repos, td)
					}
				}
			}
		}
	}
	return repos
}

// docExtractFieldListTypes 从 FieldList 提取 name type 格式的参数列表。
func docExtractFieldListTypes(fl *ast.FieldList, pass *CheckPass) []string {
	if fl == nil {
		return nil
	}
	var result []string
	for _, field := range fl.List {
		typStr := nodeStr(pass.Fset, field.Type)
		if len(field.Names) == 0 {
			result = append(result, typStr)
		} else {
			for _, name := range field.Names {
				result = append(result, name.Name+" "+typStr)
			}
		}
	}
	return result
}

// ── 事务点提取 ──────────────────────────────────────────────────────────────

func docExtractTransactions(passes []*CheckPass) []LocationInfo {
	var txs []LocationInfo
	for _, pass := range passes {
		for i, file := range pass.Files {
			path := pass.FilePaths[i]
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				fn := funcName(fd)
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "WithTx" {
						return true
					}
					txs = append(txs, LocationInfo{
						File:     path,
						Line:     lineOf(pass.Fset, call.Pos()),
						Function: fn,
					})
					return true
				})
			}
		}
	}
	return txs
}

// ── 加锁点提取 ──────────────────────────────────────────────────────────────

func docExtractLocks(passes []*CheckPass) []LocationInfo {
	var locks []LocationInfo
	for _, pass := range passes {
		for i, file := range pass.Files {
			path := pass.FilePaths[i]
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				fn := funcName(fd)
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					// clause.Locking{...}
					comp, ok := n.(*ast.CompositeLit)
					if ok {
						litStr := nodeStr(pass.Fset, comp.Type)
						if strings.Contains(litStr, "Locking") {
							detail := nodeStr(pass.Fset, comp)
							locks = append(locks, LocationInfo{
								File:     path,
								Line:     lineOf(pass.Fset, comp.Pos()),
								Function: fn,
								Detail:   truncate(detail, 80),
							})
						}
					}
					// "FOR UPDATE" 字符串
					lit, ok := n.(*ast.BasicLit)
					if ok && lit.Kind == token.STRING {
						val, _ := strconv.Unquote(lit.Value)
						if strings.Contains(strings.ToUpper(val), "FOR UPDATE") {
							locks = append(locks, LocationInfo{
								File:     path,
								Line:     lineOf(pass.Fset, lit.Pos()),
								Function: fn,
								Detail:   val,
							})
						}
					}
					return true
				})
			}
		}
	}
	return locks
}

// ── 跨模块调用者提取 ────────────────────────────────────────────────────────

// docExtractExternalCallers 扫描 internal/ 下所有非目标模块的 Go 文件，
// 找出导入并调用目标模块导出函数的位置。
func docExtractExternalCallers(module string) []ExternalCaller {
	codeRoot := filepath.Join("internal", module)
	// 获取 go module path 前缀（从 go.mod）
	goModPath := detectGoModulePath()

	// 构建目标模块的可能 import 路径
	targetImportPrefixes := []string{
		goModPath + "/" + codeRoot, // e.g. github.com/.../internal/entitlement
	}

	var callers []ExternalCaller

	_ = filepath.Walk("internal", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// 跳过目标模块自身
		if strings.HasPrefix(path, codeRoot+"/") || path == codeRoot {
			return nil
		}

		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}

		// 快速过滤：文件中是否包含目标模块路径
		contentStr := string(content)
		hasImport := false
		for _, prefix := range targetImportPrefixes {
			if strings.Contains(contentStr, prefix) {
				hasImport = true
				break
			}
		}
		if !hasImport {
			return nil
		}

		// 解析 AST
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, content, parser.ParseComments)
		if parseErr != nil {
			return nil
		}

		// 收集导入的目标模块包的本地名称
		importLocalNames := make(map[string]bool)
		for _, imp := range file.Imports {
			impPath, _ := strconv.Unquote(imp.Path.Value)
			isTarget := false
			for _, prefix := range targetImportPrefixes {
				if strings.HasPrefix(impPath, prefix) {
					isTarget = true
					break
				}
			}
			if !isTarget {
				continue
			}
			localName := ""
			if imp.Name != nil {
				localName = imp.Name.Name
			} else {
				parts := strings.Split(impPath, "/")
				localName = parts[len(parts)-1]
			}
			if localName != "_" && localName != "." {
				importLocalNames[localName] = true
			}
		}
		if len(importLocalNames) == 0 {
			return nil
		}

		// 提取调用者所属模块名
		callerModule := docExtractModuleName(path)

		// 遍历函数声明，查找对目标模块的调用
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			callerFunc := funcName(fd)

			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				if importLocalNames[ident.Name] {
					callers = append(callers, ExternalCaller{
						CallerModule:   callerModule,
						CallerFile:     path,
						CallerFunction: callerFunc,
						Target:         ident.Name + "." + sel.Sel.Name,
						Line:           fset.Position(call.Pos()).Line,
						ErrorHandling:  classifyErrorHandling(fd, call, fset),
					})
				}
				return true
			})
		}
		return nil
	})
	return callers
}

// docExtractModuleName 从文件路径提取模块名（如 internal/order/service/create.go → order）。
func docExtractModuleName(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i, p := range parts {
		if p == "internal" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// docExtractCallerSources 为每个调用方模块加载完整包源码（包级别 + 嵌套依赖包）。
//
// 流程：
//  1. 从 external_callers 中去重出调用方模块列表
//  2. 对每个模块，找到直接调用目标模块的包（如 order/service）
//  3. 解析这些包的 import，递归收集同模块内的依赖包（如 order/model、order/repository）
//  4. 读取所有相关包的非测试 .go 文件内容
func docExtractCallerSources(module string, callers []ExternalCaller, ifaceDeps []InterfaceDep) []CallerModuleSource {
	goModPath := detectGoModulePath()
	targetPrefix := goModPath + "/internal/" + module

	// 1. 按模块去重，收集每个模块中的直接调用包路径
	modulePackages := make(map[string]map[string]bool) // module → set of package dirs
	for _, c := range callers {
		if c.CallerModule == "" {
			continue
		}
		if modulePackages[c.CallerModule] == nil {
			modulePackages[c.CallerModule] = make(map[string]bool)
		}
		modulePackages[c.CallerModule][filepath.Dir(c.CallerFile)] = true
	}
	// 接口依赖的模块也加入
	for _, dep := range ifaceDeps {
		if dep.CallerModule == "" {
			continue
		}
		if modulePackages[dep.CallerModule] == nil {
			modulePackages[dep.CallerModule] = make(map[string]bool)
		}
		modulePackages[dep.CallerModule][filepath.Dir(dep.InterfaceFile)] = true
	}

	// 按模块名排序，确保输出顺序确定
	sortedModules := make([]string, 0, len(modulePackages))
	for m := range modulePackages {
		sortedModules = append(sortedModules, m)
	}
	sort.Strings(sortedModules)

	var result []CallerModuleSource
	for _, callerModule := range sortedModules {
		seedPkgs := modulePackages[callerModule]
		callerRoot := filepath.Join("internal", callerModule)

		// 2. 从 seed 包出发，递归解析同模块内依赖包
		allPkgs := make(map[string]bool)
		// 对 seed 包排序，确保 BFS 队列初始顺序确定
		sortedSeeds := make([]string, 0, len(seedPkgs))
		for pkg := range seedPkgs {
			sortedSeeds = append(sortedSeeds, pkg)
		}
		sort.Strings(sortedSeeds)
		queue := make([]string, 0, len(sortedSeeds))
		for _, pkg := range sortedSeeds {
			allPkgs[pkg] = true
			queue = append(queue, pkg)
		}

		for len(queue) > 0 {
			pkgDir := queue[0]
			queue = queue[1:]
			deps := findIntraModuleImports(pkgDir, callerRoot, goModPath, targetPrefix)
			for _, dep := range deps {
				if !allPkgs[dep] {
					allPkgs[dep] = true
					queue = append(queue, dep)
				}
			}
		}

		// 3. 读取所有包的源文件（按路径排序确保输出确定）
		sortedPkgDirs := make([]string, 0, len(allPkgs))
		for pkgDir := range allPkgs {
			sortedPkgDirs = append(sortedPkgDirs, pkgDir)
		}
		sort.Strings(sortedPkgDirs)
		var packages []PackageSource
		for _, pkgDir := range sortedPkgDirs {
			ps := loadPackageSource(pkgDir)
			if len(ps.Files) > 0 {
				packages = append(packages, ps)
			}
		}
		if len(packages) > 0 {
			result = append(result, CallerModuleSource{
				Module:   callerModule,
				Packages: packages,
			})
		}
	}
	return result
}

// findIntraModuleImports 解析包目录下所有 .go 文件的 import，
// 返回同模块内（callerRoot 下）的依赖包目录列表。跳过目标模块的导入。
func findIntraModuleImports(pkgDir, callerRoot, goModPath, targetPrefix string) []string {
	callerPrefix := goModPath + "/" + callerRoot
	var deps []string
	seen := make(map[string]bool)

	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(pkgDir, e.Name()))
		if err != nil {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, e.Name(), content, parser.ImportsOnly)
		if err != nil {
			continue
		}
		for _, imp := range file.Imports {
			impPath, _ := strconv.Unquote(imp.Path.Value)
			// 只关注同模块内的依赖，跳过目标模块
			if !strings.HasPrefix(impPath, callerPrefix) {
				continue
			}
			if strings.HasPrefix(impPath, targetPrefix) {
				continue
			}
			// 将 import path 转为相对文件路径
			relPath := strings.TrimPrefix(impPath, goModPath+"/")
			if !seen[relPath] {
				seen[relPath] = true
				deps = append(deps, relPath)
			}
		}
	}
	return deps
}

// loadPackageSource 读取目录下所有非测试 .go 文件的内容。
func loadPackageSource(dir string) PackageSource {
	ps := PackageSource{Path: dir}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ps
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		filePath := filepath.Join(dir, e.Name())
		content, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}
		ps.Files = append(ps.Files, FileSource{
			Path:    filePath,
			Content: string(content),
		})
	}
	return ps
}

// classifyErrorHandling 分析调用表达式的错误处理方式。
//
// 返回值：
//   - "checked"            — err 被 if err != nil 检查
//   - "logged_and_ignored" — err 被记录日志但流程继续（best-effort）
//   - "ignored"            — err 被赋值给 _ 或完全忽略
//   - "unknown"            — 无法确定
func classifyErrorHandling(fd *ast.FuncDecl, call *ast.CallExpr, fset *token.FileSet) string {
	callLine := fset.Position(call.Pos()).Line

	// 向上查找包含此调用的语句
	var handling string
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if handling != "" {
			return false
		}

		// 模式 1: if err := pkg.Func(...); err != nil { ... }
		ifStmt, ok := n.(*ast.IfStmt)
		if ok && ifStmt.Init != nil {
			initLine := fset.Position(ifStmt.Init.Pos()).Line
			if initLine == callLine {
				// 检查 if 体内是否有 return → checked；否则 → logged_and_ignored
				hasReturn := false
				ast.Inspect(ifStmt.Body, func(inner ast.Node) bool {
					if _, ok := inner.(*ast.ReturnStmt); ok {
						hasReturn = true
						return false
					}
					return true
				})
				if hasReturn {
					handling = "checked"
				} else {
					handling = "logged_and_ignored"
				}
				return false
			}
		}

		// 模式 2: result, err := pkg.Func(...) 后跟 if err != nil
		assign, ok := n.(*ast.AssignStmt)
		if ok {
			assignLine := fset.Position(assign.Pos()).Line
			if assignLine == callLine {
				// 检查 LHS 是否有 _
				for _, lhs := range assign.Lhs {
					if ident, ok := lhs.(*ast.Ident); ok && ident.Name == "_" {
						// 检查是否是 _, err := 还是 _ = （完全忽略）
						hasErrVar := false
						for _, lh := range assign.Lhs {
							if id, ok := lh.(*ast.Ident); ok && id.Name == "err" {
								hasErrVar = true
							}
						}
						if !hasErrVar {
							handling = "ignored"
							return false
						}
					}
				}
			}
		}

		// 模式 3: 纯表达式语句 pkg.Func(...)（返回值完全忽略）
		exprStmt, ok := n.(*ast.ExprStmt)
		if ok {
			if exprStmt.X == call {
				handling = "ignored"
				return false
			}
		}

		return true
	})

	if handling == "" {
		handling = "checked"
	}
	return handling
}

// docExtractInterfaceDeps 检测其他模块通过接口注入依赖目标模块 service 的情况。
//
// 算法：
//  1. 从目标模块的 service 层收集所有导出方法（按 struct 类型分组）
//  2. 遍历其他模块的 interface 定义
//  3. 如果某个 interface 的所有方法（≥2 个）都能在目标模块某个 service 类型上找到 → 匹配
func docExtractInterfaceDeps(passes []*CheckPass, module string) []InterfaceDep {
	// 1. 收集目标模块 service 类型的导出方法集
	serviceMethods := make(map[string]map[string]bool) // type → method set
	for _, pass := range passes {
		for i := range pass.Files {
			path := pass.FilePaths[i]
			if !isServicePkg(path) {
				continue
			}
			for _, decl := range pass.Files[i].Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Recv == nil || !fd.Name.IsExported() {
					continue
				}
				recv := extractReceiver(fd)
				if serviceMethods[recv] == nil {
					serviceMethods[recv] = make(map[string]bool)
				}
				serviceMethods[recv][fd.Name.Name] = true
			}
		}
	}
	if len(serviceMethods) == 0 {
		return nil
	}

	// 2. 遍历其他模块的 interface
	codeRoot := filepath.Join("internal", module)
	var deps []InterfaceDep

	_ = filepath.Walk("internal", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if strings.HasPrefix(path, codeRoot+"/") {
			return nil
		}

		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, content, parser.ParseComments)
		if parseErr != nil {
			return nil
		}

		callerModule := docExtractModuleName(path)

		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.TYPE {
				continue
			}
			for _, spec := range genDecl.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				iface, ok := ts.Type.(*ast.InterfaceType)
				if !ok || iface.Methods == nil {
					continue
				}

				var methods []string
				for _, method := range iface.Methods.List {
					if len(method.Names) > 0 {
						methods = append(methods, method.Names[0].Name)
					}
				}
				// 至少 2 个方法才有意义（单方法 interface 误报率太高）
				if len(methods) < 2 {
					continue
				}

				// 按 service 类型名排序，确保匹配顺序确定
				sortedSvcTypes := make([]string, 0, len(serviceMethods))
				for st := range serviceMethods {
					sortedSvcTypes = append(sortedSvcTypes, st)
				}
				sort.Strings(sortedSvcTypes)
				for _, svcType := range sortedSvcTypes {
					svcMethodSet := serviceMethods[svcType]
					allMatch := true
					for _, m := range methods {
						if !svcMethodSet[m] {
							allMatch = false
							break
						}
					}
					if !allMatch {
						continue
					}
					// 防误报：至少一个方法名非泛用（长度 > 6），
					// 或接口名/文件路径包含目标模块名
					specific := strings.Contains(strings.ToLower(ts.Name.Name), module) ||
						strings.Contains(path, module)
					if !specific {
						for _, m := range methods {
							if len(m) > 6 { // Create/List/Update/Delete 等泛用名长度 ≤ 6
								specific = true
								break
							}
						}
					}
					if !specific {
						continue
					}
					deps = append(deps, InterfaceDep{
						CallerModule:  callerModule,
						InterfaceName: ts.Name.Name,
						InterfaceFile: path,
						Methods:       methods,
						ImplementedBy: svcType,
					})
					break
				}
			}
		}
		return nil
	})
	return deps
}

// detectGoModulePath 从 go.mod 读取模块路径。
func detectGoModulePath() string {
	content, err := os.ReadFile("go.mod")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module"))
		}
	}
	return ""
}

// ── JSON 示例生成 ─────────────────────────────────────────────────────────────

// generateJSONExample 根据 DTO 字段列表生成 JSON 示例字符串。
func generateJSONExample(fields []FieldInfo) string {
	if len(fields) == 0 {
		return "{}"
	}
	var parts []string
	for _, f := range fields {
		jsonKey := f.JSON
		if jsonKey == "" || jsonKey == "-" {
			continue
		}
		// 去掉 omitempty 和 (string) 标注
		jsonKey = strings.Split(jsonKey, ",")[0]
		jsonKey = strings.TrimSuffix(jsonKey, " (string)")
		if jsonKey == "" || jsonKey == "-" {
			continue
		}
		val := exampleValueForType(f.Type, f.Name, jsonKey)
		parts = append(parts, fmt.Sprintf("  %q: %s", jsonKey, val))
	}
	if len(parts) == 0 {
		return "{}"
	}
	return "{\n" + strings.Join(parts, ",\n") + "\n}"
}

// exampleValueForType 根据 Go 类型返回合理的 JSON 示例值。
func exampleValueForType(goType, fieldName, jsonKey string) string {
	// 去掉指针前缀
	baseType := strings.TrimPrefix(goType, "*")

	// 切片类型
	if strings.HasPrefix(goType, "[]") {
		elemType := strings.TrimPrefix(goType, "[]")
		elemVal := exampleValueForType(elemType, fieldName, jsonKey)
		return "[" + elemVal + "]"
	}

	switch baseType {
	case "string":
		return exampleStringValue(fieldName, jsonKey)
	case "int", "int32", "int64":
		if strings.Contains(strings.ToLower(jsonKey), "id") {
			return "1"
		}
		if strings.Contains(jsonKey, "count") || strings.Contains(jsonKey, "page") {
			return "1"
		}
		if strings.Contains(jsonKey, "amount") || strings.Contains(jsonKey, "delta") {
			return "2592000"
		}
		return "0"
	case "uint64":
		return `"1"`
	case "bool":
		if strings.Contains(jsonKey, "free") {
			return "true"
		}
		return "false"
	case "time.Time":
		return `"2026-04-10T00:00:00Z"`
	case "float64", "float32":
		return "0.0"
	default:
		// 嵌套 struct 或未知类型
		return "{}"
	}
}

// exampleStringValue 根据字段名/json key 返回有意义的示例字符串值。
func exampleStringValue(fieldName, jsonKey string) string {
	key := strings.ToLower(jsonKey)
	switch {
	case strings.Contains(key, "type") && strings.Contains(key, "entitlement"):
		return `"call_seconds"`
	case strings.Contains(key, "source_type"):
		return `"trial"`
	case strings.Contains(key, "source"):
		return `"subscription"`
	case strings.Contains(key, "tier"):
		return `"subscription"`
	case strings.Contains(key, "unit"):
		return `"seconds"`
	case strings.Contains(key, "status"):
		return `"active"`
	case strings.Contains(key, "period"):
		return `"monthly"`
	case strings.Contains(key, "name") && strings.Contains(key, "display"):
		return `"月度会员"`
	case strings.Contains(key, "name"):
		return `"monthly_plan"`
	case strings.Contains(key, "description"):
		return `"通话时长权益"`
	case strings.Contains(key, "reason"):
		return `"试用发放"`
	case strings.Contains(key, "trade"):
		return `"T202604100001"`
	case strings.Contains(key, "stripe"):
		return `"price_1234567890"`
	case strings.Contains(key, "keyword"):
		return `"会员"`
	default:
		return fmt.Sprintf("%q", jsonKey+"_example")
	}
}
