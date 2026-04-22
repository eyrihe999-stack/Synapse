package main

import (
	"go/ast"
	"go/token"
	"strings"
	"unicode"
)

// checkGodocCoverage 检测导出函数缺少 godoc 注释。
//
// 规则：每个导出函数/方法前必须有 godoc 注释，内容应包含功能概述、关键参数、
// 返回值语义（尤其是 error 可能的类型）。缺少 godoc 导致代码难以理解和维护。
//
// 豁免：
//   - repository 层（CRUD 方法自明）
//   - init()、TableName() 等约定方法
//   - 测试文件
//
// 修复方式：补充 godoc，首行格式为 `// FuncName 功能描述。`
func checkGodocCoverage(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if !decl.Name.IsExported() || isRepoPkg(path) {
			return
		}
		if decl.Name.Name == "init" || decl.Name.Name == "TableName" {
			return
		}
		if decl.Doc == nil || len(decl.Doc.List) == 0 {
			findings = append(findings, Finding{
				Category: "annotation",
				Rule:     "godoc-missing",
				Severity: "warning",
				File:     path,
				Line:     lineOf(pass.Fset, decl.Pos()),
				Message:  funcName(decl) + ": 导出函数缺少 godoc",
			})
		}
	})
	return findings
}

// checkGodocShallow 检测 godoc 注释是否过于浅薄。
//
// 两条子规则：
//
//  1. godoc-shallow：去掉函数名前缀后，剩余描述不足 6 个字符视为过浅。
//     例如 `// Create 创建。` → 去掉 "Create " 后剩 "创建。"（3 字符）→ 过浅。
//
//  2. godoc-error-undoc：函数返回 error 但注释未提及错误/失败/异常相关关键词。
//     关键词：错误、失败、异常、error、err、返回.*error、sentinel。
//
// 豁免：
//   - repository 层（CRUD 方法自明）
//   - 测试文件
//   - 无 godoc 的函数（由 checkGodocCoverage 负责）
//   - init()、TableName() 等约定方法
//   - 不返回 error 的函数不检查 godoc-error-undoc
//
// 修复方式：丰富 godoc，补充功能细节和可能的错误说明。
func checkGodocShallow(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if !decl.Name.IsExported() || isRepoPkg(path) {
			return
		}
		if decl.Name.Name == "init" || decl.Name.Name == "TableName" {
			return
		}
		if decl.Doc == nil || len(decl.Doc.List) == 0 {
			return // 由 checkGodocCoverage 负责
		}

		text := decl.Doc.Text()
		name := decl.Name.Name

		// 子规则 1：去掉函数名前缀后描述过短
		trimmed := text
		if strings.HasPrefix(trimmed, name+" ") {
			trimmed = strings.TrimPrefix(trimmed, name+" ")
		} else if strings.HasPrefix(trimmed, name) {
			trimmed = strings.TrimPrefix(trimmed, name)
		}
		trimmed = strings.TrimSpace(trimmed)
		// 多行注释只看首行是否过浅
		if idx := strings.Index(trimmed, "\n"); idx > 0 {
			trimmed = trimmed[:idx]
		}
		trimmed = strings.TrimRight(trimmed, "。.") // 去掉末尾句号再算长度
		trimmed = strings.TrimSpace(trimmed)

		if len([]rune(trimmed)) < 6 {
			findings = append(findings, Finding{
				Category: "annotation",
				Rule:     "godoc-shallow",
				Severity: "warning",
				File:     path,
				Line:     lineOf(pass.Fset, decl.Pos()),
				Message:  funcName(decl) + ": godoc 描述过浅，应补充功能细节",
			})
		}

		// 子规则 2：返回 error 但注释未提及错误场景
		if returnsError(decl) {
			hasErrorDoc := strings.Contains(text, "错误") ||
				strings.Contains(text, "失败") ||
				strings.Contains(text, "异常") ||
				strings.Contains(text, "error") ||
				strings.Contains(text, "err") ||
				strings.Contains(text, "sentinel") ||
				strings.Contains(text, "返回") ||
				strings.Contains(text, "校验") ||
				strings.Contains(text, "检查") ||
				strings.Contains(text, "无效")
			if !hasErrorDoc {
				findings = append(findings, Finding{
					Category: "annotation",
					Rule:     "godoc-error-undoc",
					Severity: "info",
					File:     path,
					Line:     lineOf(pass.Fset, decl.Pos()),
					Message:  funcName(decl) + ": 函数返回 error 但 godoc 未说明可能的错误场景",
				})
			}
		}
	})
	return findings
}

// checkGodocChinese 检测 godoc 注释是否使用中文。
//
// 规则：项目要求所有注释必须使用中文（代码标识符可保留英文）。godoc 中至少应包含
// 中文字符（CJK 统一汉字范围）。极短注释（< 10 字符）可能只是函数名前缀，跳过。
//
// 豁免：测试文件、无 godoc 的函数（由 checkGodocCoverage 负责）。
//
// 修复方式：将英文注释改写为中文。
func checkGodocChinese(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if !decl.Name.IsExported() || decl.Doc == nil || len(decl.Doc.List) == 0 {
			return
		}
		text := decl.Doc.Text()
		hasChinese := false
		for _, r := range text {
			if unicode.Is(unicode.Han, r) {
				hasChinese = true
				break
			}
		}
		if !hasChinese && len(text) > 10 {
			findings = append(findings, Finding{
				Category: "annotation",
				Rule:     "godoc-language",
				Severity: "info",
				File:     path,
				Line:     lineOf(pass.Fset, decl.Pos()),
				Message:  funcName(decl) + ": godoc 非中文",
			})
		}
	})
	return findings
}

// checkSentinelComment 检测 errors.go 中的 sentinel error 变量缺少逐条注释。
//
// 规则：每个 `var ErrXxx = errors.New("...")` 前或行尾必须有注释，说明该 error
// 的业务含义和使用场景。只有 var 块级注释（描述整个块）不算——每个 sentinel 需要独立注释。
//
// 检测方式：
//   - 遍历所有 var 声明中 Err 前缀的变量
//   - 检查 valSpec.Doc（行前注释）或 valSpec.Comment（行尾注释）
//   - 不检查 genDecl.Doc（块级注释不代表逐条注释）
//
// 豁免：测试文件。
//
// 修复方式：在 sentinel 变量上方添加 `// ErrXxx 业务含义描述。`
func checkSentinelComment(pass *CheckPass) []Finding {
	var findings []Finding

	for i, file := range pass.Files {
		path := pass.FilePaths[i]
		if isTestFile(path) {
			continue
		}
		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.VAR {
				continue
			}
			for _, spec := range genDecl.Specs {
				valSpec := spec.(*ast.ValueSpec)
				for _, name := range valSpec.Names {
					if !strings.HasPrefix(name.Name, "Err") {
						continue
					}
					hasComment := (valSpec.Doc != nil && len(valSpec.Doc.List) > 0) ||
						(valSpec.Comment != nil && len(valSpec.Comment.List) > 0)
					if !hasComment {
						findings = append(findings, Finding{
							Category: "annotation",
							Rule:     "sentinel-comment",
							Severity: "warning",
							File:     path,
							Line:     lineOf(pass.Fset, name.Pos()),
							Message:  "sentinel error " + name.Name + " 缺少注释",
						})
					}
				}
			}
		}
	}
	return findings
}

// checkTypeComment 检测导出类型（struct 和 interface）缺少注释。
//
// 规则：每个导出的 struct 和 interface 前必须有注释，说明其职责和使用场景。
// 这是 Go 的 godoc 惯例，也是 golint 的标准检查项。
//
// 检测方式：检查 TypeSpec 或所属 GenDecl 的 Doc 字段。
//
// 豁免：测试文件。
//
// 修复方式：在类型定义前添加 `// TypeName 类型描述。`
func checkTypeComment(pass *CheckPass) []Finding {
	var findings []Finding

	for i, file := range pass.Files {
		path := pass.FilePaths[i]
		if isTestFile(path) {
			continue
		}
		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.TYPE {
				continue
			}
			for _, spec := range genDecl.Specs {
				ts := spec.(*ast.TypeSpec)
				if !ts.Name.IsExported() {
					continue
				}
				hasComment := (genDecl.Doc != nil && len(genDecl.Doc.List) > 0) ||
					(ts.Doc != nil && len(ts.Doc.List) > 0)
				if !hasComment {
					findings = append(findings, Finding{
						Category: "annotation",
						Rule:     "type-comment",
						Severity: "warning",
						File:     path,
						Line:     lineOf(pass.Fset, ts.Pos()),
						Message:  "导出类型 " + ts.Name.Name + " 缺少注释",
					})
				}
			}
		}
	}
	return findings
}

// checkLockingComment 检测 clause.Locking 使用处缺少行内注释。
//
// 规则：每个 FOR UPDATE / FOR SHARE 锁语句必须有注释说明加锁的目的（防止什么竞态）。
// 没有注释的锁语句难以理解其必要性，也增加审计难度。
//
// 检测方式：
//   - 找到所有包含 "Locking" 的 CompositeLit（clause.Locking{...}）
//   - 检查当前行或上一行是否有注释
//
// 豁免：测试文件。
//
// 修复方式：在锁语句上方加 `// 锁定 xxx 行，防止并发 yyy 竞态`。
func checkLockingComment(pass *CheckPass) []Finding {
	var findings []Finding

	for i, file := range pass.Files {
		path := pass.FilePaths[i]
		if isTestFile(path) {
			continue
		}

		commentLines := make(map[int]bool)
		for _, cg := range file.Comments {
			for _, c := range cg.List {
				commentLines[lineOf(pass.Fset, c.Pos())] = true
			}
		}

		ast.Inspect(file, func(n ast.Node) bool {
			comp, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			s := nodeStr(pass.Fset, comp)
			if !strings.Contains(s, "Locking") {
				return true
			}
			line := lineOf(pass.Fset, comp.Pos())
			if !commentLines[line] && !commentLines[line-1] {
				findings = append(findings, Finding{
					Category: "annotation",
					Rule:     "locking-comment",
					Severity: "warning",
					File:     path,
					Line:     line,
					Message:  "clause.Locking 无行内注释（应说明锁的目的）",
				})
			}
			return true
		})
	}
	return findings
}

// checkTestComment 检测测试函数缺少结构化注释。
//
// 规则：每个顶层 Test* 函数前必须有结构化注释，包含：
//   - 第一行：函数名 + 场景描述
//   - 前置条件：mock/setup 的关键配置
//   - 预期结果：核心断言的文字描述
//
// 关键词检测：注释中必须包含「前置条件」和「预期结果」两个关键词。
//
// 豁免：TestMain、非 Test 前缀的函数、无参数的辅助函数。
//
// 修复方式：按三行格式补充注释。
func checkTestComment(pass *CheckPass) []Finding {
	var findings []Finding

	eachFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if !isTestFile(path) {
			return
		}
		name := decl.Name.Name
		if !strings.HasPrefix(name, "Test") || name == "TestMain" {
			return
		}
		if decl.Type.Params == nil || len(decl.Type.Params.List) == 0 {
			return
		}

		if decl.Doc == nil || len(decl.Doc.List) == 0 {
			findings = append(findings, Finding{
				Category: "annotation",
				Rule:     "test-comment",
				Severity: "info",
				File:     path,
				Line:     lineOf(pass.Fset, decl.Pos()),
				Message:  name + ": 缺少结构化注释（前置条件/预期结果）",
			})
			return
		}

		text := decl.Doc.Text()
		if !strings.Contains(text, "前置条件") || !strings.Contains(text, "预期结果") {
			findings = append(findings, Finding{
				Category: "annotation",
				Rule:     "test-comment",
				Severity: "info",
				File:     path,
				Line:     lineOf(pass.Fset, decl.Pos()),
				Message:  name + ": 注释缺少「前置条件」或「预期结果」",
			})
		}
	})
	return findings
}
