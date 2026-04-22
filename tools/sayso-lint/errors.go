package main

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"
)

// checkErrSwallow 检测被吞掉的 error。
//
// 规则：当代码中出现 `_ = f()` 或 `x, _ := f()` 且 f 的返回值包含 error 类型时，
// 标记为违规。被吞掉的 error 会导致故障静默发生，难以排查。
//
// 类型感知（go/packages 能力）：
//   - 通过 types.Info 查询 f() 的实际返回类型
//   - 只有当被丢弃的返回值确实是 error 类型时才标记
//   - 例如 `userID, _ := middleware.GetUserID(c)` 丢弃的是 bool，不标记
//   - 例如 `_ = json.Marshal(x)` 丢弃的是 error，标记
//
// 豁免：测试文件。
//
// 修复方式：处理 error 或添加注释说明为什么选择忽略（best-effort 操作）。
func checkErrSwallow(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil {
			return
		}
		ast.Inspect(decl.Body, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok || len(assign.Lhs) == 0 {
				return true
			}

			hasBlank := false
			for _, lhs := range assign.Lhs {
				if ident, ok := lhs.(*ast.Ident); ok && ident.Name == "_" {
					hasBlank = true
					break
				}
			}
			if !hasBlank {
				return true
			}

			var callExpr *ast.CallExpr
			if len(assign.Rhs) == 1 {
				callExpr, _ = assign.Rhs[0].(*ast.CallExpr)
			}
			if callExpr == nil {
				return true
			}

			if !callReturnsError(pass.TypesInfo, callExpr) {
				return true
			}

			findings = append(findings, Finding{
				Category: "error-handling",
				Rule:     "err-swallow",
				Severity: "warning",
				File:     path,
				Line:     lineOf(pass.Fset, assign.Pos()),
				Message:  funcName(decl) + ": 吞掉 error，应处理或注释说明原因",
			})
			return true
		})
	})
	return findings
}

// checkSentinelWrap 检测 service 层 error return 是否包含 sentinel error。
//
// 规则：service/ 包中所有返回 error 的函数，每个 error return 分支都必须包含
// 一个 Err 前缀的标识符（sentinel error）。裸 `return err` 或 `return fmt.Errorf("...", err)`
// 不含 sentinel 的视为违规。
//
// 检测方式：
//   - 遍历函数体中所有 ReturnStmt
//   - 取最后一个返回值（error 位置）
//   - 递归检查表达式中是否有 Err 前缀的 Ident 或 SelectorExpr
//   - 支持跨模块 sentinel（如 entitlement.ErrPlanNotFound）
//
// 豁免：repository 层、测试文件、return nil。
//
// 修复方式：用 `fmt.Errorf("上下文: %w: %w", err, module.ErrXxxInternal)` 包装。
func checkSentinelWrap(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if !isServicePkg(path) || !returnsError(decl) || decl.Body == nil {
			return
		}

		ast.Inspect(decl.Body, func(n ast.Node) bool {
			ret, ok := n.(*ast.ReturnStmt)
			if !ok || len(ret.Results) == 0 {
				return true
			}

			last := ret.Results[len(ret.Results)-1]
			if ident, ok := last.(*ast.Ident); ok && ident.Name == "nil" {
				return true
			}

			hasErrSentinel := false
			ast.Inspect(last, func(inner ast.Node) bool {
				switch x := inner.(type) {
				case *ast.SelectorExpr:
					if strings.HasPrefix(x.Sel.Name, "Err") {
						hasErrSentinel = true
					}
				case *ast.Ident:
					if strings.HasPrefix(x.Name, "Err") && x.Name != "Errorf" && x.Name != "Error" {
						hasErrSentinel = true
					}
				}
				return !hasErrSentinel
			})

			if !hasErrSentinel {
				findings = append(findings, Finding{
					Category: "error-handling",
					Rule:     "sentinel-wrap",
					Severity: "warning",
					File:     path,
					Line:     lineOf(pass.Fset, ret.Pos()),
					Message:  funcName(decl) + ": service 层 error return 缺少 sentinel",
				})
			}
			return true
		})
	})
	return findings
}

// checkFatalPanic 检测非 main 包中使用 log.Fatal / os.Exit / panic。
//
// 规则：只有 main 包和 init() 函数可以调用进程终止函数。library 包中使用这些会
// 导致调用方无法控制错误处理流程，应改为返回 error。
//
// 检测范围：
//   - log.Fatal / log.Fatalf / log.Fatalln
//   - os.Exit
//   - panic（非 main 包）
//
// 豁免：main 包、init() 函数、测试文件。
//
// 修复方式：改为 return error，由调用方决定是否终止进程。
func checkFatalPanic(pass *CheckPass) []Finding {
	if pass.Pkg.Name() == "main" {
		return nil
	}
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Name.Name == "init" || decl.Body == nil {
			return
		}

		ast.Inspect(decl.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			var bad string
			switch fn := call.Fun.(type) {
			case *ast.SelectorExpr:
				if pkg, ok := fn.X.(*ast.Ident); ok {
					if pkg.Name == "log" && strings.HasPrefix(fn.Sel.Name, "Fatal") {
						bad = "log." + fn.Sel.Name
					}
					if pkg.Name == "os" && fn.Sel.Name == "Exit" {
						bad = "os.Exit"
					}
				}
			case *ast.Ident:
				if fn.Name == "panic" {
					bad = "panic"
				}
			}

			if bad != "" {
				findings = append(findings, Finding{
					Category: "error-handling",
					Rule:     "fatal-panic",
					Severity: "error",
					File:     path,
					Line:     lineOf(pass.Fset, call.Pos()),
					Message:  funcName(decl) + ": 非 main 包使用 " + bad + "()",
				})
			}
			return true
		})
	})
	return findings
}

// checkTypeAssert 检测单返回值类型断言（x.(Type) 而非 x, ok := ...(Type)），会导致 panic。
//
// 规则：类型断言必须用双返回值形式 `v, ok := x.(Type)`。单返回值形式在类型不匹配时
// 会 panic，导致进程崩溃。
//
// 检测方式：
//   1. 收集所有在 AssignStmt 中作为 RHS、且 LHS 有 2 个变量的 TypeAssertExpr 位置（安全形式）
//   2. 遍历所有 TypeAssertExpr，不在安全集合中的即为违规
//
// 豁免：测试文件、switch x.(type)（type switch 是安全的）。
//
// 修复方式：改为 `v, ok := x.(Type); if !ok { ... }`。
func checkTypeAssert(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil {
			return
		}

		// 收集安全的类型断言位置（双返回值赋值）
		safePositions := make(map[token.Pos]bool)
		ast.Inspect(decl.Body, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok || len(assign.Lhs) < 2 || len(assign.Rhs) != 1 {
				return true
			}
			if ta, ok := assign.Rhs[0].(*ast.TypeAssertExpr); ok {
				safePositions[ta.Pos()] = true
			}
			return true
		})

		// 找不安全的类型断言
		ast.Inspect(decl.Body, func(n ast.Node) bool {
			// 跳过 type switch
			if _, ok := n.(*ast.TypeSwitchStmt); ok {
				return false
			}
			ta, ok := n.(*ast.TypeAssertExpr)
			if !ok || ta.Type == nil { // ta.Type == nil 是 x.(type) 在 type switch 中
				return true
			}
			if safePositions[ta.Pos()] {
				return true
			}
			findings = append(findings, Finding{
				Category: "error-handling",
				Rule:     "type-assert",
				Severity: "error",
				File:     path,
				Line:     lineOf(pass.Fset, ta.Pos()),
				Message:  funcName(decl) + ": 类型断言无 ok 检查，失败会 panic",
			})
			return true
		})
	})
	return findings
}

// checkErrShadow 检测内层作用域用 := 遮蔽外层的 err 变量。
//
// 规则：在同一函数内，如果外层已定义 err 变量，内层块中用 `:=` 重新定义同名 err，
// 会导致外层 err 不被更新，可能遗漏错误处理。
//
// 类型感知（go/packages 能力）：
//   - 通过 types.Info.Defs 获取每个 err 定义的 Object
//   - 通过 Object.Parent()（Scope）向上查找是否有同名变量被遮蔽
//
// 豁免：测试文件、函数级别的 err 定义（不是遮蔽）。
//
// 修复方式：将 `:=` 改为 `=`，或用不同变量名。
func checkErrShadow(pass *CheckPass) []Finding {
	var findings []Finding

	// 收集所有 if-init 中定义的 err 位置（这是 Go 惯用模式，不算遮蔽）
	ifInitPositions := make(map[token.Pos]bool)
	for i, file := range pass.Files {
		if isTestFile(pass.FilePaths[i]) {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			ifStmt, ok := n.(*ast.IfStmt)
			if !ok || ifStmt.Init == nil {
				return true
			}
			if assign, ok := ifStmt.Init.(*ast.AssignStmt); ok {
				for _, lhs := range assign.Lhs {
					if ident, ok := lhs.(*ast.Ident); ok && ident.Name == "err" {
						ifInitPositions[ident.Pos()] = true
					}
				}
			}
			return true
		})
	}

	for i, file := range pass.Files {
		path := pass.FilePaths[i]
		if isTestFile(path) {
			continue
		}

		for ident, obj := range pass.TypesInfo.Defs {
			if obj == nil || ident.Name != "err" {
				continue
			}
			// 跳过 if-init 中的 err（Go 惯用模式）
			if ifInitPositions[ident.Pos()] {
				continue
			}
			// 确保在当前文件内
			pos := pass.Fset.Position(ident.Pos())
			fpos := pass.Fset.Position(file.Pos())
			if pos.Filename != fpos.Filename {
				continue
			}

			scope := obj.Parent()
			if scope == nil {
				continue
			}
			// 只检查 := 定义的（不是函数参数或返回值命名）
			// scope 深度 > 2 说明在函数内的嵌套块中（0=universe, 1=package, 2=function, 3+=nested）
			nestLevel := 0
			for s := scope; s != nil; s = s.Parent() {
				nestLevel++
			}
			if nestLevel <= 3 {
				continue // 函数顶层的 err 定义不算遮蔽
			}

			// 向上查找外层作用域是否有同名 err
			for outer := scope.Parent(); outer != nil; outer = outer.Parent() {
				outerObj := outer.Lookup("err")
				if outerObj == nil {
					continue
				}
				if _, ok := outerObj.(*types.Var); !ok {
					continue
				}
				if outerObj == obj {
					continue
				}
				findings = append(findings, Finding{
					Category: "error-handling",
					Rule:     "err-shadow",
					Severity: "warning",
					File:     path,
					Line:     lineOf(pass.Fset, ident.Pos()),
					Message:  "err 变量遮蔽外层定义，可能导致错误丢失",
				})
				break
			}
		}
	}
	return findings
}

// checkEmptyErrBlock 检测 `if err != nil {}` 空块——检查了 error 但什么都没做。
//
// 规则：如果代码显式检查了 err != nil 但块体为空，说明要么忘了处理，要么应该删除这个检查。
// 比 `_ = f()` 更隐蔽，因为看起来像是处理了 error。
//
// 检测方式：
//   - 找到所有 if 语句，条件包含 err != nil（或 err == nil 的 else 块）
//   - 检查 Body 是否为空（len(Body.List) == 0）
//
// 豁免：测试文件。
//
// 修复方式：添加错误处理逻辑或删除空块。
func checkEmptyErrBlock(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil {
			return
		}
		ast.Inspect(decl.Body, func(n ast.Node) bool {
			ifStmt, ok := n.(*ast.IfStmt)
			if !ok {
				return true
			}
			// 检查条件是否为 err != nil
			bin, ok := ifStmt.Cond.(*ast.BinaryExpr)
			if !ok || bin.Op != token.NEQ {
				return true
			}
			xIdent, ok := bin.X.(*ast.Ident)
			if !ok || xIdent.Name != "err" {
				return true
			}
			yIdent, ok := bin.Y.(*ast.Ident)
			if !ok || yIdent.Name != "nil" {
				return true
			}
			// 检查块体是否为空
			if len(ifStmt.Body.List) == 0 {
				findings = append(findings, Finding{
					Category: "error-handling",
					Rule:     "empty-err-block",
					Severity: "error",
					File:     path,
					Line:     lineOf(pass.Fset, ifStmt.Pos()),
					Message:  funcName(decl) + ": if err != nil {} 空块，error 未处理",
				})
			}
			return true
		})
	})
	return findings
}

// checkDeferErr 检测 defer 语句中未处理的 error 返回值。
//
// 规则：如 `defer f.Close()` 中 Close 返回 error 但被丢弃。对于写操作（如 Flush、Sync），
// defer 丢弃 error 可能导致数据丢失。
//
// 类型感知（go/packages 能力）：
//   - 通过 types.Info 查询被 defer 的函数调用的返回类型
//   - 只有当返回值包含 error 时才标记
//   - 例如 `defer cancel()`（无返回值）不标记
//
// 豁免：测试文件。
//
// 修复方式：改为 `defer func() { if err := f.Close(); err != nil { log.Warn(...) } }()`。
func checkDeferErr(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil {
			return
		}
		ast.Inspect(decl.Body, func(n ast.Node) bool {
			ds, ok := n.(*ast.DeferStmt)
			if !ok {
				return true
			}
			// defer func() { ... }() 形式——内部已处理，跳过
			if _, ok := ds.Call.Fun.(*ast.FuncLit); ok {
				return true
			}
			// 类型检查：被 defer 的函数是否返回 error
			if callReturnsError(pass.TypesInfo, ds.Call) {
				findings = append(findings, Finding{
					Category: "error-handling",
					Rule:     "defer-err",
					Severity: "info",
					File:     path,
					Line:     lineOf(pass.Fset, ds.Pos()),
					Message:  funcName(decl) + ": defer 丢弃了 error 返回值",
				})
			}
			return true
		})
	})
	return findings
}

// checkRecoverSilent 检测 recover() 捕获 panic 后静默丢弃，不记日志也不 re-panic。
//
// 规则：recover() 捕获的 panic 信息是关键的崩溃诊断数据（包含错误描述和调用栈）。
// 如果 recover() 后既不记日志也不 re-panic，panic 的根本原因会被永久丢失，
// 导致生产环境中的严重 bug 无法被发现和修复。
//
// 检测方式：
//   1. 找到 defer func() { ... }() 中的 recover() 调用
//   2. 检查 recover() 所在函数体中是否有日志调用或 panic 调用
//   3. 如果两者都没有 → 标记为静默吞 panic
//
// 豁免：测试文件。
//
// 修复方式：
//
//	defer func() {
//	    if r := recover(); r != nil {
//	        logger.Error("panic recovered", map[string]interface{}{"panic": r})
//	    }
//	}()
func checkRecoverSilent(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil {
			return
		}

		ast.Inspect(decl.Body, func(n ast.Node) bool {
			ds, ok := n.(*ast.DeferStmt)
			if !ok {
				return true
			}
			funcLit, ok := ds.Call.Fun.(*ast.FuncLit)
			if !ok || funcLit.Body == nil {
				return true
			}

			hasRecover := false
			var recoverLine int

			ast.Inspect(funcLit.Body, func(inner ast.Node) bool {
				call, ok := inner.(*ast.CallExpr)
				if !ok {
					return true
				}
				if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "recover" {
					hasRecover = true
					recoverLine = lineOf(pass.Fset, call.Pos())
				}
				return true
			})

			if !hasRecover {
				return true
			}

			bodyStr := nodeStr(pass.Fset, funcLit.Body)
			hasLog := false
			for _, method := range []string{"Error", "ErrorCtx", "Warn", "WarnCtx", "Fatal", "FatalCtx", "Println", "Printf"} {
				if strings.Contains(bodyStr, method) {
					hasLog = true
					break
				}
			}
			hasPanic := strings.Contains(bodyStr, "panic(")

			if !hasLog && !hasPanic {
				findings = append(findings, Finding{
					Category: "error-handling",
					Rule:     "recover-silent",
					Severity: "warning",
					File:     path,
					Line:     recoverLine,
					Message:  funcName(decl) + ": recover() 后未记日志也未 re-panic，panic 信息丢失",
				})
			}
			return true
		})
	})
	return findings
}

// checkErrorsIsUsage 检测用 == 比较 sentinel error 而非 errors.Is。
//
// 规则：Go 1.13 引入 error wrapping 后，被 `fmt.Errorf("...: %w", err)` 包装过的
// error 不再等于原始 sentinel。用 `==` 比较只能匹配未包装的 error，而 errors.Is
// 会递归展开 wrap 链进行比较。用 `==` 比较 sentinel 是常见的生产 bug 来源：
//
//	// BAD — 包装过的 err 永远匹配不上
//	if err == gorm.ErrRecordNotFound { ... }
//
//	// GOOD
//	if errors.Is(err, gorm.ErrRecordNotFound) { ... }
//
// 检测方式：
//   1. 找到 BinaryExpr（== 或 !=）
//   2. 检查左右操作数之一是否为 Err 前缀标识符或已知 sentinel（如 gorm.ErrRecordNotFound）
//   3. 另一操作数不是 nil → 标记（== nil 是合法的）
//
// 豁免：测试文件、与 nil 的比较。
//
// 修复方式：将 `err == ErrX` 改为 `errors.Is(err, ErrX)`，
// 将 `err != ErrX` 改为 `!errors.Is(err, ErrX)`。
func checkErrorsIsUsage(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil {
			return
		}

		ast.Inspect(decl.Body, func(n ast.Node) bool {
			bin, ok := n.(*ast.BinaryExpr)
			if !ok || (bin.Op != token.EQL && bin.Op != token.NEQ) {
				return true
			}

			// 检查左右操作数是否为 sentinel error（Err 前缀）
			lhsSentinel := isSentinelExpr(bin.X)
			rhsSentinel := isSentinelExpr(bin.Y)

			if !lhsSentinel && !rhsSentinel {
				return true
			}

			// 排除与 nil 的比较
			if isNilIdent(bin.X) || isNilIdent(bin.Y) {
				return true
			}

			// 确认另一侧是 error 类型
			var errSide ast.Expr
			if lhsSentinel {
				errSide = bin.Y
			} else {
				errSide = bin.X
			}
			tv, ok := pass.TypesInfo.Types[errSide]
			if ok && !isErrorType(tv.Type) {
				return true // 非 error 类型的比较不管
			}

			sentinelStr := nodeStr(pass.Fset, bin.X)
			if rhsSentinel {
				sentinelStr = nodeStr(pass.Fset, bin.Y)
			}

			op := "=="
			fix := "errors.Is(err, " + sentinelStr + ")"
			if bin.Op == token.NEQ {
				op = "!="
				fix = "!errors.Is(err, " + sentinelStr + ")"
			}

			findings = append(findings, Finding{
				Category: "error-handling",
				Rule:     "errors-is",
				Severity: "warning",
				File:     path,
				Line:     lineOf(pass.Fset, bin.Pos()),
				Message:  funcName(decl) + ": 用 " + op + " 比较 sentinel error，应改为 " + fix,
			})
			return true
		})
	})
	return findings
}

// isSentinelExpr 判断表达式是否为 sentinel error（Err 前缀标识符或选择器）。
//
// 匹配模式：
//   - Ident: ErrNotFound、ErrTimeout 等
//   - SelectorExpr: gorm.ErrRecordNotFound、module.ErrInternal 等
func isSentinelExpr(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return strings.HasPrefix(e.Name, "Err") && e.Name != "Errorf" && e.Name != "Error"
	case *ast.SelectorExpr:
		return strings.HasPrefix(e.Sel.Name, "Err")
	}
	return false
}

// isNilIdent 判断表达式是否为 nil 标识符。
func isNilIdent(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == "nil"
}
