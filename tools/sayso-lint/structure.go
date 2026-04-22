package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
)

// badNamePatterns 定义被禁止的文件命名正则模式。
//
// 被 checkFileName 使用，匹配任一模式的文件名视为无语义命名：
//   - 模式 1: utils.go / helpers.go / misc.go / common.go 等垃圾桶文件
//   - 模式 2: service2.go / handler3.go 等编号文件（应按功能命名）
//   - 模式 3: xxx_new.go / xxx_old.go / xxx_bak.go 等版本后缀（应删除旧版本）
//
// 注意正则精确匹配边界：v2_auth.go、sha256.go 等合法命名不会被误标。
var badNamePatterns = []*regexp.Regexp{
	regexp.MustCompile(`^(utils|helpers|misc|common|funcs|tools)\.go$`),
	regexp.MustCompile(`^(service|handler|model|repository|dto|router|middleware|controller)[0-9]+\.go$`),
	regexp.MustCompile(`^.+_(new|old|bak|copy|tmp)\.go$`),
}

// checkFileSize 检测非测试源文件超过 400 行。
//
// 规则：单个 .go 文件不超过 400 行。超过时应按职责拆分（如 precreate.go、settle.go）。
// 这是可读性要求，不是硬性限制。
//
// 豁免：
//   - model 包文件（多 struct 集中定义）
//   - router 文件（路由注册集中）
//   - index 文件（索引定义集中）
//   - 测试文件（不检查规模）
//
// 修复方式：按功能拆分为多个文件。
func checkFileSize(pass *CheckPass) []Finding {
	var findings []Finding

	for i, file := range pass.Files {
		path := pass.FilePaths[i]
		if isTestFile(path) {
			continue
		}
		base := filepath.Base(path)
		pkg := file.Name.Name
		if pkg == "model" || strings.Contains(base, "router") || strings.Contains(base, "index") {
			continue
		}

		end := pass.Fset.Position(file.End())
		lines := end.Line
		if lines > 400 {
			findings = append(findings, Finding{
				Category: "structure",
				Rule:     "file-size",
				Severity: "warning",
				File:     path,
				Line:     0,
				Message:  base + ": " + itoa(lines) + " 行，超过 400 行阈值",
			})
		}
	}
	return findings
}

// checkFuncSize 检测函数超过 200 行。
//
// 规则：单个函数不超过 200 行（含空行和注释行）。超过时应评估是否能提取子步骤。
//
// 豁免：
//   - 错误映射函数（handleServiceError 等，由 switch/case 组成）
//   - 路由注册函数（RegisterRoutes 等）
//   - 测试文件
//
// 修复方式：提取子函数，如 validate()、transform()、persist() 等。
func checkFuncSize(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil {
			return
		}
		start := pass.Fset.Position(decl.Pos()).Line
		end := pass.Fset.Position(decl.End()).Line
		lines := end - start + 1

		lower := strings.ToLower(decl.Name.Name)
		if strings.Contains(lower, "serviceerror") || strings.Contains(lower, "registerroute") {
			return
		}

		if lines > 200 {
			findings = append(findings, Finding{
				Category: "structure",
				Rule:     "func-size",
				Severity: "warning",
				File:     path,
				Line:     start,
				Message:  funcName(decl) + ": " + itoa(lines) + " 行，超过 200 行阈值",
			})
		}
	})
	return findings
}

// checkFileName 检测无语义的文件命名。
//
// 规则：文件名必须反映其内容。以下命名模式被禁止：
//   - utils.go / helpers.go / misc.go / common.go（应将函数移入相关功能文件）
//   - service2.go / handler3.go（应按功能重命名，如 notification.go）
//   - xxx_new.go / xxx_old.go / xxx_bak.go（应删除旧版本）
//
// 注意正则精确匹配：v2_auth.go、sha256.go 等合法命名不会被误标。
//
// 豁免：测试文件。
//
// 修复方式：重命名文件并更新所有 import 路径。
func checkFileName(pass *CheckPass) []Finding {
	var findings []Finding

	for i := range pass.Files {
		path := pass.FilePaths[i]
		if isTestFile(path) {
			continue
		}
		base := filepath.Base(path)
		for _, pat := range badNamePatterns {
			if pat.MatchString(base) {
				findings = append(findings, Finding{
					Category: "structure",
					Rule:     "file-naming",
					Severity: "warning",
					File:     path,
					Line:     0,
					Message:  "无语义命名: " + base,
				})
				break
			}
		}
	}

	// handler 包文件命名检查：只允许 handler / router / error_map
	for i, file := range pass.Files {
		path := pass.FilePaths[i]
		if isTestFile(path) || !isHandlerPkg(path) {
			continue
		}
		base := filepath.Base(path)
		isHandler := strings.Contains(base, "handler")
		isRouter := strings.Contains(base, "router")
		isErrorMap := strings.Contains(base, "error_map")

		// 命名检查
		if !isHandler && !isRouter && !isErrorMap {
			findings = append(findings, Finding{
				Category: "structure",
				Rule:     "file-naming",
				Severity: "warning",
				File:     path,
				Line:     0,
				Message:  "handler 包文件 " + base + " 应包含 handler 或 router（如 admin_plan_handler.go）",
			})
		}

		// 职责分离检查：handler 文件不应包含路由注册
		if isHandler && hasRegisterRoutesFunc(file) {
			findings = append(findings, Finding{
				Category: "structure",
				Rule:     "handler-router-split",
				Severity: "warning",
				File:     path,
				Line:     0,
				Message:  base + " 混合了 handler 逻辑和路由注册，应将 Register*Routes 拆到 router.go",
			})
		}

		// 职责分离检查：router 文件不应包含 handler 方法
		if isRouter && hasHandlerMethod(file) {
			findings = append(findings, Finding{
				Category: "structure",
				Rule:     "handler-router-split",
				Severity: "warning",
				File:     path,
				Line:     0,
				Message:  base + " 混合了路由注册和 handler 方法，应将 handler 方法拆到 xxx_handler.go",
			})
		}
	}

	return findings
}


// checkRequiredFiles 检测 V2 模块是否缺少必须的文件和目录。
//
// 规则：每个模块必须有 const.go、errors.go，以及 handler/ 和 service/ 子包。
// 有 admin handler 文件时还必须有 admin_error_map.go 和 admin_router.go。
//
// 检测方式：从 CheckPass 的文件列表和包名推断目录结构。
//
// 修复方式：创建缺失的文件。
// checkRequiredFilesCross 跨包检查 V2 模块是否缺少必须文件（只执行一次）。
func checkRequiredFilesCross(passes []*CheckPass, codeRoot string) []Finding {
	fileSet := make(map[string]bool)
	pkgSet := make(map[string]bool)
	hasAdminHandler := false
	hasUserHandler := false

	for _, pass := range passes {
		for i, file := range pass.Files {
			path := pass.FilePaths[i]
			if isTestFile(path) {
				continue
			}
			fileSet[path] = true
			pkgSet[file.Name.Name] = true
			if file.Name.Name == "handler" {
				if isAdminFile(path) {
					hasAdminHandler = true
				} else {
					hasUserHandler = true
				}
			}
		}
	}

	var findings []Finding
	type reqItem struct {
		name  string
		check func() bool
	}
	required := []reqItem{
		{"const.go", func() bool { return fileSet[codeRoot+"/const.go"] }},
		{"errors.go", func() bool { return fileSet[codeRoot+"/errors.go"] }},
		{"handler/ 目录", func() bool { return pkgSet["handler"] }},
		{"handler/error_map.go", func() bool {
			// 只在有用户侧 handler 文件时才要求 error_map.go；
			// 纯 admin handler 的模块（如 order）用户侧由其他模块处理，不需要此文件。
			return !hasUserHandler || fileSet[codeRoot+"/handler/error_map.go"]
		}},
		{"service/ 目录", func() bool { return pkgSet["service"] }},
	}
	if hasAdminHandler {
		required = append(required,
			reqItem{"handler/admin_error_map.go", func() bool { return fileSet[codeRoot+"/handler/admin_error_map.go"] }},
			reqItem{"handler/admin_router.go", func() bool { return fileSet[codeRoot+"/handler/admin_router.go"] }},
		)
	}

	for _, req := range required {
		if !req.check() {
			findings = append(findings, Finding{
				Category: "structure",
				Rule:     "required-file",
				Severity: "warning",
				File:     codeRoot,
				Line:     0,
				Message:  "缺少必须文件: " + req.name,
			})
		}
	}
	return findings
}

// checkContextFirst 检测导出函数的 context.Context 参数不在第一个位置。
//
// 规则：Go 惯例要求 context.Context 作为函数的第一个参数。不遵循此惯例会导致
// 代码不一致，也不符合社区最佳实践。
//
// 类型感知（go/packages 能力）：
//   - 通过 types.Info 确认参数类型是否为 context.Context
//   - 不依赖参数名判断（参数名可能不叫 ctx）
//
// 豁免：测试文件、非导出函数、无 context 参数的函数。
//
// 修复方式：将 context.Context 移到第一个参数位置。
func checkContextFirst(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if !decl.Name.IsExported() || decl.Type.Params == nil {
			return
		}
		params := decl.Type.Params.List
		if len(params) == 0 {
			return
		}

		// 检查第一个参数是否为 context.Context
		firstIsCtx := false
		hasCtx := false
		ctxPos := -1

		for i, field := range params {
			for _, name := range field.Names {
				// 用类型信息判断是否为 context.Context
				if obj := pass.TypesInfo.ObjectOf(name); obj != nil {
					typStr := obj.Type().String()
					if typStr == "context.Context" {
						hasCtx = true
						if i == 0 {
							firstIsCtx = true
						} else {
							ctxPos = i
						}
					}
				}
			}
		}

		if hasCtx && !firstIsCtx && ctxPos > 0 {
			findings = append(findings, Finding{
				Category: "structure",
				Rule:     "ctx-first",
				Severity: "info",
				File:     path,
				Line:     lineOf(pass.Fset, decl.Pos()),
				Message:  funcName(decl) + ": context.Context 应为第一个参数",
			})
		}
	})
	return findings
}

// checkDeepNesting 检测函数内 if/for/switch/select 嵌套深度超过 4 层。
//
// 规则：嵌套过深严重影响可读性。超过 4 层应考虑提前返回（guard clause）、
// 提取子函数、或重构逻辑。
//
// 检测方式：递归遍历函数体，跟踪当前嵌套深度，记录最大值。
//
// 豁免：测试文件。
//
// 修复方式：使用 early return 减少嵌套、提取子函数。
func checkDeepNesting(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil {
			return
		}
		maxDepth := measureNesting(decl.Body, 0)
		if maxDepth > 4 {
			findings = append(findings, Finding{
				Category: "structure",
				Rule:     "deep-nesting",
				Severity: "warning",
				File:     path,
				Line:     lineOf(pass.Fset, decl.Pos()),
				Message:  funcName(decl) + ": 最大嵌套深度 " + itoa(maxDepth) + "（应 ≤ 4）",
			})
		}
	})
	return findings
}

// measureNesting 计算 BlockStmt 内的最大控制流嵌套深度。
//
// 从给定的初始 depth 开始，遍历块中每条语句，递归计入 if/for/switch/select
// 带来的嵌套层级增长。返回整个块中达到的最大深度值。
//
// 与 stmtNesting 配合使用：measureNesting 负责遍历块中的语句列表，
// stmtNesting 负责对单条语句递归分析。
func measureNesting(block *ast.BlockStmt, depth int) int {
	maxDepth := depth
	for _, stmt := range block.List {
		d := stmtNesting(stmt, depth)
		if d > maxDepth {
			maxDepth = d
		}
	}
	return maxDepth
}

// stmtNesting 计算单条语句引入的最大嵌套深度。
//
// 递归规则：
//   - IfStmt → body depth+1，else 分支也 depth+1（else-if 不额外增加）
//   - ForStmt / RangeStmt → body depth+1
//   - SwitchStmt / TypeSwitchStmt / SelectStmt → body depth+1
//   - BlockStmt → 委托 measureNesting（不增加 depth，裸块不算嵌套）
//   - CaseClause → 遍历内部语句（case 本身不增加 depth，switch 已计入）
//   - 其他语句 → 返回当前 depth（无嵌套贡献）
func stmtNesting(stmt ast.Stmt, depth int) int {
	switch s := stmt.(type) {
	case *ast.IfStmt:
		d := measureNesting(s.Body, depth+1)
		if s.Else != nil {
			if elseBlock, ok := s.Else.(*ast.BlockStmt); ok {
				if ed := measureNesting(elseBlock, depth+1); ed > d {
					d = ed
				}
			} else if elseIf, ok := s.Else.(*ast.IfStmt); ok {
				if ed := stmtNesting(elseIf, depth); ed > d {
					d = ed
				}
			}
		}
		return d
	case *ast.ForStmt:
		return measureNesting(s.Body, depth+1)
	case *ast.RangeStmt:
		return measureNesting(s.Body, depth+1)
	case *ast.SwitchStmt:
		return measureNesting(s.Body, depth+1)
	case *ast.TypeSwitchStmt:
		return measureNesting(s.Body, depth+1)
	case *ast.SelectStmt:
		return measureNesting(s.Body, depth+1)
	case *ast.BlockStmt:
		return measureNesting(s, depth)
	case *ast.CaseClause:
		max := depth
		for _, inner := range s.Body {
			if d := stmtNesting(inner, depth); d > max {
				max = d
			}
		}
		return max
	}
	return depth
}

// itoa 将整数转换为十进制字符串。
//
// 封装 fmt.Sprintf，避免在 Finding.Message 拼接中反复写 fmt.Sprintf("%d", n)。
// 用于文件行数、嵌套深度、方法数量等数字到字符串的转换。
func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}

// checkInterfacePollution 检测方法数过多的 interface（接口膨胀）。
//
// 规则：interface 的方法数不应超过 6 个。过大的 interface 违反接口隔离原则（ISP），
// 导致：
//   - 实现者被迫实现不需要的方法
//   - mock 成本高，测试困难
//   - 耦合度高，难以替换实现
//
// 检测方式：
//   1. 遍历所有导出的 interface 类型声明
//   2. 统计 interface 中的方法数
//   3. 超过阈值 6 的标记为违规
//
// 豁免：测试文件、非导出 interface。
//
// 修复方式：拆分为多个小 interface，按职责分组。
func checkInterfacePollution(pass *CheckPass) []Finding {
	const maxMethods = 6
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
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || !ts.Name.IsExported() {
					continue
				}
				iface, ok := ts.Type.(*ast.InterfaceType)
				if !ok || iface.Methods == nil {
					continue
				}

				methodCount := 0
				for _, method := range iface.Methods.List {
					// 普通方法（有名字）
					if len(method.Names) > 0 {
						methodCount += len(method.Names)
					}
					// 嵌入接口的方法不直接计入，避免重复
				}

				if methodCount > maxMethods {
					findings = append(findings, Finding{
						Category: "structure",
						Rule:     "interface-pollution",
						Severity: "warning",
						File:     path,
						Line:     lineOf(pass.Fset, ts.Pos()),
						Message:  ts.Name.Name + ": interface 有 " + itoa(methodCount) + " 个方法（应 ≤ " + itoa(maxMethods) + "），考虑拆分",
					})
				}
			}
		}
	}
	return findings
}

// hasRegisterRoutesFunc 检查文件中是否包含名称以 Register 开头且以 Routes 结尾的函数声明。
func hasRegisterRoutesFunc(file *ast.File) bool {
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		name := fd.Name.Name
		if strings.HasPrefix(name, "Register") && strings.HasSuffix(name, "Routes") {
			return true
		}
	}
	return false
}

// hasHandlerMethod 检查文件中是否包含有 receiver 的导出方法（即 handler 方法）。
func hasHandlerMethod(file *ast.File) bool {
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || !fd.Name.IsExported() {
			continue
		}
		return true
	}
	return false
}

// checkErrorMapCompleteness 跨包检查 error_map.go 是否覆盖了 errors.go 中的所有 sentinel。
//
// 规则：每个模块的 handler/error_map.go 应覆盖 errors.go 中定义的所有 sentinel error
// （Err 前缀变量）。未覆盖的 sentinel 在 handler 层无法被正确映射为 HTTP 状态码，
// 导致 fallback 到 500 Internal Server Error，给用户展示不友好的错误信息。
//
// 检测方式：
//   1. 从 errors.go 中收集所有 Err 前缀的变量名
//   2. 从 handler/error_map.go 中读取文件内容
//   3. 对每个 sentinel，检查 error_map.go 中是否引用了该变量名
//   4. 未引用的 → 标记为缺失
//
// 豁免：无 errors.go 或无 error_map.go 的模块（由 checkRequiredFilesCross 负责报告）。
//
// 修复方式：在 error_map.go 中为缺失的 sentinel 添加对应的 HTTP 状态码映射。
func checkErrorMapCompleteness(passes []*CheckPass, codeRoot string) []Finding {
	// 收集 errors.go 中的所有 Err 前缀变量
	var sentinels []string
	for _, pass := range passes {
		for i, file := range pass.Files {
			path := pass.FilePaths[i]
			if filepath.Base(path) != "errors.go" || isTestFile(path) {
				continue
			}
			// 只看模块根目录的 errors.go
			if filepath.Dir(path) != codeRoot {
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
						if strings.HasPrefix(name.Name, "Err") && name.IsExported() {
							sentinels = append(sentinels, name.Name)
						}
					}
				}
			}
		}
	}

	if len(sentinels) == 0 {
		return nil
	}

	// 读取 error_map.go 的内容
	errorMapPath := filepath.Join(codeRoot, "handler", "error_map.go")
	errorMapContent := ""
	for _, pass := range passes {
		for i := range pass.Files {
			if pass.FilePaths[i] == errorMapPath {
				errorMapContent = nodeStr(pass.Fset, pass.Files[i])
				break
			}
		}
	}

	if errorMapContent == "" {
		return nil // error_map.go 不存在，由 checkRequiredFilesCross 报告
	}

	// 同时检查 admin_error_map.go（如果存在）
	adminErrorMapPath := filepath.Join(codeRoot, "handler", "admin_error_map.go")
	adminErrorMapContent := ""
	for _, pass := range passes {
		for i := range pass.Files {
			if pass.FilePaths[i] == adminErrorMapPath {
				adminErrorMapContent = nodeStr(pass.Fset, pass.Files[i])
				break
			}
		}
	}
	combinedContent := errorMapContent + "\n" + adminErrorMapContent

	var findings []Finding
	for _, sentinel := range sentinels {
		if !containsWord(combinedContent, sentinel) {
			findings = append(findings, Finding{
				Category: "structure",
				Rule:     "error-map-completeness",
				Severity: "info",
				File:     errorMapPath,
				Line:     0,
				Message:  "error_map.go 未覆盖 sentinel: " + sentinel + "（如该 error 可到达 handler 层，会 fallback 到 500）",
			})
		}
	}
	return findings
}

// checkHandlerRouteCoverage 跨包检查 handler 包中导出方法是否都在 router.go 中注册了路由。
//
// 规则：handler 包中的每个导出方法（有 receiver 的函数）都应在 router.go 或
// admin_router.go 中被引用（注册为路由处理函数）。未注册的导出方法是死代码，
// 增加维护成本且可能误导其他开发者认为该接口已上线。
//
// 检测方式：
//   1. 收集 handler 包中所有导出方法名（非测试文件、有 receiver）
//   2. 读取 router.go 和 admin_router.go 的内容
//   3. 对每个方法名，检查是否在 router 文件中被引用
//   4. 未引用 → 标记为未注册
//
// 豁免：
//   - 测试文件
//   - 非 handler 包的导出方法
//   - package-level 函数（无 receiver，不是 handler 方法）
//   - 无 router.go 的模块（由 checkRequiredFilesCross 负责报告）
//
// 修复方式：在 router.go 中注册路由，或删除未使用的 handler 方法。
func checkHandlerRouteCoverage(passes []*CheckPass, codeRoot string) []Finding {
	// 收集 handler 包中的导出方法
	type handlerMethod struct {
		name string
		file string
		line int
	}
	var methods []handlerMethod

	for _, pass := range passes {
		for i, file := range pass.Files {
			path := pass.FilePaths[i]
			if isTestFile(path) || !isHandlerPkg(path) {
				continue
			}
			// 跳过 router/error_map 等基础设施文件
			base := filepath.Base(path)
			if strings.Contains(base, "router") || strings.Contains(base, "error_map") {
				continue
			}
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Recv == nil || !fd.Name.IsExported() {
					continue
				}
				methods = append(methods, handlerMethod{
					name: fd.Name.Name,
					file: path,
					line: lineOf(pass.Fset, fd.Pos()),
				})
			}
		}
	}

	if len(methods) == 0 {
		return nil
	}

	// 读取路由注册文件内容（文件名含 router 或包含 RegisterRoutes 函数）
	routerContent := ""
	for _, pass := range passes {
		for i, file := range pass.Files {
			path := pass.FilePaths[i]
			if !isHandlerPkg(path) || isTestFile(path) {
				continue
			}
			base := filepath.Base(path)
			if strings.Contains(base, "router") || hasRegisterRoutesFunc(file) {
				routerContent += nodeStr(pass.Fset, file) + "\n"
			}
		}
	}

	if routerContent == "" {
		return nil // 无 router.go，由 checkRequiredFilesCross 报告
	}

	var findings []Finding
	for _, m := range methods {
		if !containsWord(routerContent, m.name) {
			findings = append(findings, Finding{
				Category: "structure",
				Rule:     "handler-route-coverage",
				Severity: "warning",
				File:     m.file,
				Line:     m.line,
				Message:  "handler 方法 " + m.name + " 未在 router.go 中注册路由（死代码？）",
			})
		}
	}
	return findings
}

// checkCircularDep 跨包检测模块间的循环依赖。
//
// 规则：internal/ 下的不同模块之间不应形成双向依赖（A 导入 B 且 B 导入 A）。
// 虽然 Go 编译器已禁止直接循环导入，但通过间接路径或在不同包级别可能形成
// 逻辑上的循环依赖，导致：
//   - 模块边界不清晰
//   - 重构困难（改一个模块会连锁影响另一个）
//   - 潜在的架构腐化
//
// 检测方式：
//   1. 收集当前模块所有文件的 import 路径
//   2. 过滤出指向其他 internal/ 模块的导入
//   3. 读取被导入模块的文件，检查是否反向导入当前模块
//   4. 如果存在双向依赖 → 标记
//
// 豁免：测试文件。
//
// 修复方式：提取公共逻辑到独立模块，或使用 interface 解耦。
func checkCircularDep(passes []*CheckPass, codeRoot string) []Finding {
	if len(passes) == 0 {
		return nil
	}
	module := passes[0].Module
	modulePkg := "internal/" + module

	// 收集当前模块导入的其他 internal/ 模块
	importedModules := make(map[string]int) // module → 首次出现行号
	var importFile string

	for _, pass := range passes {
		for i, file := range pass.Files {
			path := pass.FilePaths[i]
			if isTestFile(path) {
				continue
			}
			for _, imp := range file.Imports {
				impPath := strings.Trim(imp.Path.Value, `"`)
				if !strings.Contains(impPath, "internal/") || strings.Contains(impPath, modulePkg) {
					continue
				}
				// 提取目标模块名
				idx := strings.Index(impPath, "internal/")
				if idx < 0 {
					continue
				}
				rest := impPath[idx+len("internal/"):]
				targetModule := strings.Split(rest, "/")[0]
				if targetModule == module {
					continue
				}
				if _, ok := importedModules[targetModule]; !ok {
					importedModules[targetModule] = lineOf(pass.Fset, imp.Pos())
					importFile = path
				}
			}
		}
	}

	if len(importedModules) == 0 {
		return nil
	}

	// 读取其他模块文件，检查是否反向导入当前模块
	projectFiles := readAllGoFiles()
	var findings []Finding

	for targetModule, line := range importedModules {
		targetPrefix := "internal/" + targetModule + "/"
		for _, pf := range projectFiles {
			if !strings.HasPrefix(pf.path, targetPrefix) || strings.HasSuffix(pf.path, "_test.go") {
				continue
			}
			// 检查目标模块是否导入了当前模块
			if strings.Contains(pf.content, `"`+modulePkg+`"`) || strings.Contains(pf.content, `"`+modulePkg+"/") {
				findings = append(findings, Finding{
					Category: "structure",
					Rule:     "circular-dep",
					Severity: "warning",
					File:     importFile,
					Line:     line,
					Message:  fmt.Sprintf("模块循环依赖: %s ↔ %s，应提取公共逻辑或使用 interface 解耦", module, targetModule),
				})
				break // 每对模块只报一次
			}
		}
	}
	return findings
}
