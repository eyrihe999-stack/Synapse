package main

import (
	"go/ast"
	"strings"
)

// isLogMethod 判断方法名是否为项目约定的日志方法。
//
// 项目使用自定义 LoggerInterface，支持普通和带 Context 两种调用形式：
//   - 普通形式：Debug / Info / Warn / Error / Fatal
//   - Context 形式：DebugCtx / InfoCtx / WarnCtx / ErrorCtx / FatalCtx
//
// *Ctx 形式会自动从 context 中提取 trace_id、user_id 等字段写入日志。
// 此函数被日志覆盖检查、敏感字段检查、重复日志检查等多个规则复用。
func isLogMethod(name string) bool {
	switch name {
	case "Debug", "DebugCtx", "Info", "InfoCtx", "Warn", "WarnCtx",
		"Error", "ErrorCtx", "Fatal", "FatalCtx":
		return true
	}
	return false
}

// checkLogCoverage 检测 error return 前缺少日志记录。
//
// 规则：每个 error return 分支前 15 行内应有日志调用（ErrorCtx/WarnCtx 等），
// 确保异常路径在生产环境可追踪。宁可冗余也不漏——上层已有日志时不算违规（双层保障）。
//
// 检测方式：
//   1. 收集函数体中所有日志调用的行号
//   2. 对每个 error return（最后返回值非 nil），检查前 15 行是否有日志
//
// 豁免：
//   - repository 层（约定由 service 调用方负责日志）
//   - 测试文件
//
// 修复方式：在 return err 前添加 s.logger.ErrorCtx 或 WarnCtx。
func checkLogCoverage(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if !returnsError(decl) || decl.Body == nil {
			return
		}
		if isRepoPkg(path) {
			return
		}

		logLines := make(map[int]bool)
		ast.Inspect(decl.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && isLogMethod(sel.Sel.Name) {
					logLines[lineOf(pass.Fset, call.Pos())] = true
				}
			}
			return true
		})

		ast.Inspect(decl.Body, func(n ast.Node) bool {
			ret, ok := n.(*ast.ReturnStmt)
			if !ok || len(ret.Results) == 0 {
				return true
			}
			last := ret.Results[len(ret.Results)-1]
			if ident, ok := last.(*ast.Ident); ok && ident.Name == "nil" {
				return true
			}

			retLine := lineOf(pass.Fset, ret.Pos())
			hasLog := false
			for line := retLine - 15; line < retLine; line++ {
				if logLines[line] {
					hasLog = true
					break
				}
			}
			if !hasLog {
				findings = append(findings, Finding{
					Category: "logging",
					Rule:     "log-coverage",
					Severity: "warning",
					File:     path,
					Line:     retLine,
					Message:  funcName(decl) + ": error return 前无日志",
				})
			}
			return true
		})
	})
	return findings
}


// checkSensitiveLog 检测日志中包含敏感字段名。
//
// 规则：日志调用的 map[string]interface{} 参数中不应包含密码、token、API key 等
// 敏感字段。这些数据记录到日志会造成信息泄露。
//
// 检测方式：
//   1. 找到所有日志方法调用
//   2. 检查 map 参数中的 key 是否匹配敏感关键词列表
//   3. 匹配规则：key 的小写形式包含 password/secret/token/api_key 等
//
// 豁免：测试文件。
//
// 修复方式：脱敏后记录（如 maskPhone(phone)），或直接不记录该字段。
func checkSensitiveLog(pass *CheckPass) []Finding {
	sensitive := []string{"password", "passwd", "secret", "token", "api_key", "apikey", "credential", "private_key"}
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil {
			return
		}
		ast.Inspect(decl.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !isLogMethod(sel.Sel.Name) {
				return true
			}
			for _, arg := range call.Args {
				comp, ok := arg.(*ast.CompositeLit)
				if !ok {
					continue
				}
				for _, elt := range comp.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key := strings.Trim(nodeStr(pass.Fset, kv.Key), `"`)
					for _, s := range sensitive {
						if strings.Contains(strings.ToLower(key), s) {
							findings = append(findings, Finding{
								Category: "logging",
								Rule:     "sensitive-log",
								Severity: "error",
								File:     path,
								Line:     lineOf(pass.Fset, call.Pos()),
								Message:  funcName(decl) + ": 日志含敏感字段 " + key,
							})
						}
					}
				}
			}
			return true
		})
	})
	return findings
}

// checkLogContextFields 检测 ErrorCtx/WarnCtx 调用缺少结构化上下文字段。
//
// 规则：Error 级别和 Warn 级别的日志应包含足够的排查上下文（通过结构化字段传递）。
// 只有消息文本而没有关联的 user_id、order_id、request_id 等字段，在生产环境中
// 几乎无法定位问题——你只知道"出错了"，但不知道是谁、哪个请求、哪条数据出的错。
//
// 检测方式：
//   1. 找到 ErrorCtx / WarnCtx 调用（非 *Ctx 形式不强制，因为已缺少 trace_id）
//   2. 检查参数数量：ErrorCtx(ctx, "msg", fields) 应有 3 个参数
//   3. 如果只有 2 个参数（ctx + msg）→ 缺少结构化字段
//
// 豁免：测试文件、repository 层（service 负责日志上下文）。
//
// 修复方式：添加结构化字段：
//
//	s.logger.ErrorCtx(ctx, "创建订单失败", map[string]interface{}{
//	    "user_id": userID, "error": err.Error(),
//	})
func checkLogContextFields(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil || isRepoPkg(path) {
			return
		}

		ast.Inspect(decl.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			// 只检查 ErrorCtx 和 WarnCtx
			if sel.Sel.Name != "ErrorCtx" && sel.Sel.Name != "WarnCtx" {
				return true
			}
			// ErrorCtx(ctx, "msg", fields) — 期望 3 个参数
			if len(call.Args) < 3 {
				findings = append(findings, Finding{
					Category: "logging",
					Rule:     "log-context-fields",
					Severity: "info",
					File:     path,
					Line:     lineOf(pass.Fset, call.Pos()),
					Message:  funcName(decl) + ": " + sel.Sel.Name + "() 缺少结构化字段参数（应附带 user_id/order_id 等排查上下文）",
				})
			}
			return true
		})
	})
	return findings
}

// checkDuplicateLog 检测同一函数内出现多次相同消息的日志调用。
//
// 规则：同一个函数中如果有两条消息内容完全相同的日志，说明存在复制粘贴或逻辑重复。
// 应合并或使用不同的消息来区分不同的代码路径。
//
// 检测方式：
//   1. 遍历函数体中所有日志调用
//   2. 提取消息参数（*Ctx 方法第 2 个参数，非 Ctx 第 1 个参数）
//   3. 统计每个消息出现次数，> 1 则标记
//
// 豁免：测试文件。
//
// 修复方式：合并重复日志，或修改消息以区分不同路径。
func checkDuplicateLog(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil {
			return
		}
		msgCount := make(map[string]int)

		ast.Inspect(decl.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !isLogMethod(sel.Sel.Name) {
				return true
			}
			idx := 0
			if strings.HasSuffix(sel.Sel.Name, "Ctx") {
				idx = 1
			}
			if idx < len(call.Args) {
				if lit, ok := call.Args[idx].(*ast.BasicLit); ok {
					msg := strings.Trim(lit.Value, `"`)
					msgCount[msg]++
				}
			}
			return true
		})

		for msg, count := range msgCount {
			if count > 1 {
				findings = append(findings, Finding{
					Category: "redundancy",
					Rule:     "duplicate-log",
					Severity: "warning",
					File:     path,
					Line:     lineOf(pass.Fset, decl.Pos()),
					Message:  funcName(decl) + ": 日志消息重复 " + truncate(msg, 40),
				})
			}
		}
	})
	return findings
}
