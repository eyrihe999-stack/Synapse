package main

import (
	"bytes"
	"go/ast"
	"go/printer"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
)

// ── 类型判断 ────────────────────────────────────────────────────────────────

// isErrorType 检查 types.Type 是否为内置 error 接口。
//
// 判断方式：通过 types.Identical 与 Universe scope 中的 error 类型进行结构比较，
// 确保即使别名或重定义也能正确识别。nil 类型返回 false。
func isErrorType(t types.Type) bool {
	return t != nil && types.Identical(t, types.Universe.Lookup("error").Type())
}

// callReturnsError 检查函数调用表达式的返回值列表中是否包含 error 类型。
//
// 通过 types.Info.Types 查询 CallExpr 的类型信息：
//   - 如果返回值本身是 error → true
//   - 如果返回值是 Tuple（多返回值），逐一检查每个元素是否为 error
//   - 类型信息不可用时返回 false（不产生误报）
func callReturnsError(info *types.Info, call *ast.CallExpr) bool {
	tv, ok := info.Types[call]
	if !ok {
		return false
	}
	t := tv.Type
	if isErrorType(t) {
		return true
	}
	if tuple, ok := t.(*types.Tuple); ok {
		for i := 0; i < tuple.Len(); i++ {
			if isErrorType(tuple.At(i).Type()) {
				return true
			}
		}
	}
	return false
}

// receiverIs 检查 SelectorExpr（方法调用）的接收器类型字符串中是否包含 substr。
//
// 通过 types.Info.Selections 获取方法选择信息，提取接收器的完整类型路径
// （如 "*gorm.io/gorm.DB"），然后做子串匹配。用于区分同名方法属于不同包的情况
// （如区分 gorm.DB.Save 和 os.File.Save）。
//
// 如果 sel 不在 Selections 中（非方法调用或类型信息不可用），返回 false。
func receiverIs(info *types.Info, sel *ast.SelectorExpr, substr string) bool {
	selection, ok := info.Selections[sel]
	if !ok {
		return false
	}
	return strings.Contains(selection.Recv().String(), substr)
}

// isGormMethod 检查 SelectorExpr 是否为 *gorm.DB 上的方法调用。
//
// 委托 receiverIs 检查接收器类型路径是否包含 "gorm.io/gorm"。
// 用于所有 GORM 相关检查中过滤非 GORM 同名方法的误报。
func isGormMethod(info *types.Info, sel *ast.SelectorExpr) bool {
	return receiverIs(info, sel, "gorm.io/gorm")
}

// ── 路径/名称工具 ──────────────────────────────────────────────────────────

// isTestFile 判断文件路径是否为测试文件。
//
// 匹配规则：文件名后缀为 "_test.go"。用于在各检查函数中豁免测试文件。
func isTestFile(path string) bool {
	return strings.HasSuffix(path, "_test.go")
}

// isAdminFile 判断文件是否为 Admin 相关文件。
//
// 匹配规则：文件名（不含目录路径）中包含 "admin" 子串。
// 用于 checkRequiredFilesCross 判断是否需要 admin_error_map.go / admin_router.go。
func isAdminFile(path string) bool {
	base := filepath.Base(path)
	return strings.Contains(base, "admin")
}

// isServicePkg 判断文件路径是否属于 service 层。
//
// 匹配规则：路径中包含 "/service/"。用于识别 service 层特有的检查规则
// （如 sentinel error 要求、日志覆盖要求）。
func isServicePkg(path string) bool {
	return strings.Contains(path, "/service/")
}

// isRepoPkg 判断文件路径是否属于 repository 层。
//
// 匹配规则：路径中包含 "/repository/"。repository 层在多个检查中被豁免
// （如日志覆盖、sentinel wrap），由上层 service 负责。
func isRepoPkg(path string) bool {
	return strings.Contains(path, "/repository/")
}

// isHandlerPkg 判断文件路径是否属于 handler 层。
//
// 匹配规则：路径中包含 "/handler/"。用于 Gin 相关检查和 JSON tag 检查
// 等仅在 handler 层适用的规则。
func isHandlerPkg(path string) bool {
	return strings.Contains(path, "/handler/")
}

// relPath 将绝对路径转为相对于 codeRoot 的相对路径。
//
// 如果 filepath.Rel 失败（跨卷等异常），返回原始路径。
// 用于生成报告中更简洁的文件路径显示。
func relPath(path, codeRoot string) string {
	if rel, err := filepath.Rel(codeRoot, path); err == nil {
		return rel
	}
	return path
}

// ── AST 工具 ────────────────────────────────────────────────────────────────

// nodeStr 将 AST 节点打印为 Go 源码字符串。
//
// 通过 go/printer 将节点反序列化为可读代码，常用于：
//   - 在函数体中搜索特定模式（如 "Body.Close"、"Where"）
//   - 提取调用链的完整文本用于启发式匹配
//   - 生成 Finding 的 message 中的代码片段
func nodeStr(fset *token.FileSet, n ast.Node) string {
	var buf bytes.Buffer
	_ = printer.Fprint(&buf, fset, n)
	return buf.String()
}

// truncate 将字符串截断到指定长度，超出部分用 "..." 替代。
//
// 用于 Finding 的 message 中避免过长的代码片段破坏报告可读性。
// 如果字符串长度 <= n，原样返回。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// lineOf 从 token.Pos 提取行号。
//
// 委托 FileSet.Position 获取完整的位置信息，返回 Line 字段。
// 用于所有 Finding 中的行号填充。
func lineOf(fset *token.FileSet, pos token.Pos) int {
	return fset.Position(pos).Line
}

// eachFunc 遍历 CheckPass 中所有文件的所有函数声明（包括测试文件）。
//
// 对每个 *ast.FuncDecl 调用回调 fn，传入所属文件、相对路径和函数声明。
// 遍历顺序：按 pass.Files 索引顺序，每个文件内按声明顺序。
// 注意：包括测试文件中的函数。如需跳过测试文件，使用 eachNonTestFunc。
func eachFunc(pass *CheckPass, fn func(file *ast.File, path string, decl *ast.FuncDecl)) {
	for i, file := range pass.Files {
		path := pass.FilePaths[i]
		for _, d := range file.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok {
				fn(file, path, fd)
			}
		}
	}
}

// eachNonTestFunc 遍历非测试文件中的所有函数声明。
//
// 在 eachFunc 基础上过滤掉 _test.go 文件。大部分 check 函数使用此遍历器，
// 因为测试文件中的代码风格约定不同（如允许 panic、允许吞 error 等）。
// 需要检查测试文件的规则（如 checkTestComment）应直接使用 eachFunc。
func eachNonTestFunc(pass *CheckPass, fn func(file *ast.File, path string, decl *ast.FuncDecl)) {
	eachFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if !isTestFile(path) {
			fn(file, path, decl)
		}
	})
}

// funcName 返回函数的完整限定名称（含 receiver 类型）。
//
// 格式规则：
//   - 普通函数 → "FuncName"
//   - 值接收器方法 → "TypeName.MethodName"
//   - 指针接收器方法 → "TypeName.MethodName"（星号被去除）
//
// 用于 Finding 的 message 字段，帮助定位问题所在的具体函数。
func funcName(decl *ast.FuncDecl) string {
	name := decl.Name.Name
	if decl.Recv != nil && len(decl.Recv.List) > 0 {
		recv := decl.Recv.List[0].Type
		switch t := recv.(type) {
		case *ast.Ident:
			name = t.Name + "." + name
		case *ast.StarExpr:
			if ident, ok := t.X.(*ast.Ident); ok {
				name = ident.Name + "." + name
			}
		}
	}
	return name
}

// returnsError 通过 AST 检查函数签名的最后一个返回值是否为 error 类型。
//
// 检测方式：取 FuncDecl.Type.Results 列表的最后一个字段，判断类型标识符名称
// 是否为 "error"。这是纯 AST 级别的检查（不依赖类型系统），用于快速过滤
// 不返回 error 的函数，避免后续更昂贵的类型分析。
//
// 限制：无法识别类型别名（如 type MyError = error），但实际项目中极少出现。
func returnsError(decl *ast.FuncDecl) bool {
	if decl.Type.Results == nil {
		return false
	}
	results := decl.Type.Results.List
	if len(results) == 0 {
		return false
	}
	last := results[len(results)-1]
	if ident, ok := last.Type.(*ast.Ident); ok {
		return ident.Name == "error"
	}
	return false
}

// ── 抑制与过滤 ────────────────────────────────────────────────────────────────

// ignoreDirective 存储某行的规则抑制信息。
//
// 两种模式：
//   - all=true → 抑制该行所有规则
//   - all=false, rules 非空 → 只抑制 rules 中指定的规则
type ignoreDirective struct {
	all   bool            // true = 抑制所有规则
	rules map[string]bool // 指定要抑制的规则集合
}

// buildIgnoreMap 扫描文件注释，构建行级抑制映射。
//
// 支持格式：
//   - //sayso-lint:ignore              → 抑制该行和下一行的所有规则
//   - //sayso-lint:ignore rule1,rule2  → 抑制该行和下一行的指定规则
//
// 注释可以在独立行（对下一行生效）或行尾（对当前行生效）。
// 同时覆盖注释所在行和下一行，确保两种写法都能命中。
func buildIgnoreMap(passes []*CheckPass) map[string]map[int]*ignoreDirective {
	m := make(map[string]map[int]*ignoreDirective)

	for _, pass := range passes {
		for i, file := range pass.Files {
			path := pass.FilePaths[i]
			for _, cg := range file.Comments {
				for _, c := range cg.List {
					text := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
					if !strings.HasPrefix(text, "sayso-lint:ignore") {
						continue
					}

					rest := strings.TrimPrefix(text, "sayso-lint:ignore")
					rest = strings.TrimSpace(rest)

					dir := &ignoreDirective{}
					if rest == "" {
						dir.all = true
					} else {
						dir.rules = make(map[string]bool)
						for _, r := range strings.Split(rest, ",") {
							r = strings.TrimSpace(r)
							if r != "" {
								dir.rules[r] = true
							}
						}
					}

					commentLine := lineOf(pass.Fset, c.Pos())
					applyIgnore(m, path, commentLine, dir)
					applyIgnore(m, path, commentLine+1, dir)
				}
			}
		}
	}
	return m
}

// applyIgnore 将抑制指令应用到指定位置，与已有指令合并。
//
// 合并规则：
//   - 如果新指令是 all=true，则覆盖为全量抑制
//   - 如果已有指令是 all=true，则保持全量抑制
//   - 否则合并两个 rules 集合
func applyIgnore(m map[string]map[int]*ignoreDirective, path string, line int, dir *ignoreDirective) {
	if m[path] == nil {
		m[path] = make(map[int]*ignoreDirective)
	}
	existing := m[path][line]
	if existing == nil {
		cp := &ignoreDirective{all: dir.all}
		if dir.rules != nil {
			cp.rules = make(map[string]bool, len(dir.rules))
			for r := range dir.rules {
				cp.rules[r] = true
			}
		}
		m[path][line] = cp
		return
	}
	if dir.all {
		existing.all = true
		return
	}
	if !existing.all {
		if existing.rules == nil {
			existing.rules = make(map[string]bool)
		}
		for r := range dir.rules {
			existing.rules[r] = true
		}
	}
}

// filterIgnored 移除被 //sayso-lint:ignore 抑制的 findings，返回过滤后的列表和被抑制的数量。
func filterIgnored(findings []Finding, ignoreMap map[string]map[int]*ignoreDirective) (filtered []Finding, suppressed int) {
	for _, f := range findings {
		if lineMap, ok := ignoreMap[f.File]; ok {
			if dir, ok := lineMap[f.Line]; ok && (dir.all || dir.rules[f.Rule]) {
				suppressed++
				continue
			}
		}
		filtered = append(filtered, f)
	}
	return
}

// severityLevel 定义严重级别的数值映射，用于过滤比较。
var severityLevel = map[string]int{"info": 1, "warning": 2, "error": 3}

// filterBySeverity 按最低严重级别过滤 findings。
//
// 级别排序：info(1) < warning(2) < error(3)。
// 传入 "warning" 表示只保留 warning 和 error 级别的 findings。
// 无效的 minSeverity 值不过滤（返回原列表）。
func filterBySeverity(findings []Finding, minSeverity string) []Finding {
	minLevel := severityLevel[minSeverity]
	if minLevel == 0 {
		return findings
	}
	var result []Finding
	for _, f := range findings {
		if severityLevel[f.Severity] >= minLevel {
			result = append(result, f)
		}
	}
	return result
}
