package main

import (
	"go/ast"
	"strings"
)

// ginResponseMethods 列举 Gin 框架中写入 HTTP 响应的方法。
//
// 包括正常响应方法（JSON、XML、String 等）和中止方法（Abort 系列）。
// 被 checkGinNoReturn 使用：如果 if-block 中调用了这些方法但没有 return，
// 后续代码会继续执行，可能导致多次写入响应。
var ginResponseMethods = map[string]bool{
	"JSON": true, "XML": true, "YAML": true, "String": true,
	"HTML": true, "Data": true, "Redirect": true,
	"AbortWithStatusJSON": true, "AbortWithStatus": true, "AbortWithError": true,
	"Abort": true,
}

// responseWrapperMethods 列举项目封装的 HTTP 响应辅助函数（internal/common/response 包）。
//
// 这些函数内部调用 c.JSON() / c.Status()，在 handler 中使用时等同于直接写入响应。
// 被 checkGinNoReturn 和 checkHandlerResponseCoverage 同时使用。
// 与 response 包实际导出的 helper 保持同步 —— 增删函数时两边一起改。
var responseWrapperMethods = map[string]bool{
	"Success": true, "SuccessWithCode": true, "Created": true, "NoContent": true,
	"Error":               true,
	"BadRequest":          true,
	"Unauthorized":        true,
	"NotFound":            true,
	"TooManyRequests":     true,
	"InternalServerError": true,
}

// checkGinNoReturn 检测 Gin handler 在 c.JSON()/c.Abort*() 后缺少 return。
//
// 规则：Gin 不会在调用 c.JSON() 或 c.Abort*() 后自动终止 handler 函数的执行。
// 如果在 if-block 中调用了这些方法但没有 return，后续代码会继续执行，可能导致：
//   - 多次写入响应（HTTP 协议违规）
//   - 执行不该执行的业务逻辑
//   - 日志和监控数据错乱
//
// 检测方式：
//   1. 在 handler/ 文件中查找 if-block
//   2. 如果 if-block 中包含 c.JSON()/c.Abort*() 调用
//   3. 但 if-block 的最后一条语句不是 return → 标记
//
// 豁免：测试文件、非 handler 包。
//
// 修复方式：在 c.JSON()/c.Abort*() 后添加 return。
func checkGinNoReturn(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if !isHandlerPkg(path) || decl.Body == nil {
			return
		}

		ast.Inspect(decl.Body, func(n ast.Node) bool {
			ifStmt, ok := n.(*ast.IfStmt)
			if !ok {
				return true
			}

			checkBlockNoReturn(pass, path, decl, ifStmt.Body, &findings)

			// 也检查 else 块
			if elseBlock, ok := ifStmt.Else.(*ast.BlockStmt); ok {
				checkBlockNoReturn(pass, path, decl, elseBlock, &findings)
			}
			return true
		})
	})
	return findings
}

// checkBlockNoReturn 检查一个 if/else 块中是否有 Gin 响应调用但缺少 return。
//
// 检测逻辑：
//   1. 遍历块中所有语句，查找 c.JSON()/c.Abort*() 等 Gin 响应方法调用
//   2. 通过 types.Info 确认接收器是 *gin.Context（同时兼容名为 "c"/"ctx" 的变量名启发式）
//   3. 如果找到响应调用，检查块的最后一条语句是否为 ReturnStmt
//   4. 最后一条是 return → 安全；不是 return → 标记违规
//
// 被 checkGinNoReturn 调用，分别检查 if-body 和 else-body。
func checkBlockNoReturn(pass *CheckPass, path string, decl *ast.FuncDecl, block *ast.BlockStmt, findings *[]Finding) {
	if len(block.List) == 0 {
		return
	}

	// 检查块中是否有 Gin 响应调用
	var ginCallLine int
	var ginCallMethod string
	hasGinCall := false

	for _, stmt := range block.List {
		ast.Inspect(stmt, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if isAnyResponseCall(call) {
				hasGinCall = true
				ginCallLine = lineOf(pass.Fset, call.Pos())
				ginCallMethod = sel.Sel.Name
			}
			return true
		})
	}

	if !hasGinCall {
		return
	}

	// 检查块的最后一条语句是否为 return
	lastStmt := block.List[len(block.List)-1]
	if _, ok := lastStmt.(*ast.ReturnStmt); ok {
		return
	}
	// 也接受最后一条是包含 return 的 if 语句（如 guard clause）
	if ifStmt, ok := lastStmt.(*ast.IfStmt); ok {
		if len(ifStmt.Body.List) > 0 {
			if _, ok := ifStmt.Body.List[len(ifStmt.Body.List)-1].(*ast.ReturnStmt); ok {
				return
			}
		}
	}

	*findings = append(*findings, Finding{
		Category: "gin-safety",
		Rule:     "gin-no-return",
		Severity: "error",
		File:     path,
		Line:     ginCallLine,
		Message:  funcName(decl) + ": c." + ginCallMethod + "() 后缺少 return，后续代码会继续执行",
	})
}

// checkHandlerResponseCoverage 检测 handler 函数中存在不写响应就 return 的代码路径。
//
// 规则：handler 函数（handler/ 包中参数含 *gin.Context 的函数）的每个 return 分支
// 都应在 return 前写入 HTTP 响应（c.JSON / c.Abort* 等）。如果某个 return 路径
// 没有响应调用，客户端会收不到任何响应直到超时。
//
// 检测方式：
//   1. 找到 handler/ 包中所有函数
//   2. 收集函数体中所有 Gin 响应调用的行号
//   3. 对每个 return 语句，检查前 10 行内是否有响应调用
//   4. 如果函数只有一个 return（无参 return 或函数末尾），且之前有响应调用则跳过
//
// 豁免：测试文件、非 handler 包、私有辅助函数（无 *gin.Context 参数）。
//
// 修复方式：在 return 前添加 c.JSON() 或 c.AbortWithStatusJSON()。
func checkHandlerResponseCoverage(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if !isHandlerPkg(path) || decl.Body == nil || !decl.Name.IsExported() {
			return
		}
		// 确认函数有 *gin.Context 参数
		hasGinParam := false
		if decl.Type.Params != nil {
			for _, field := range decl.Type.Params.List {
				typStr := nodeStr(pass.Fset, field.Type)
				if strings.Contains(typStr, "gin.Context") {
					hasGinParam = true
					break
				}
			}
		}
		if !hasGinParam {
			return
		}

		// 收集所有 HTTP 响应调用的行号（包括直接 c.JSON、response.Success 封装、
		// 以及带 *gin.Context 参数的辅助方法如 h.checkReady(c)、h.handleServiceError(c, err)）
		respLines := make(map[int]bool)
		ast.Inspect(decl.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if isAnyResponseCall(call) {
				respLines[lineOf(pass.Fset, call.Pos())] = true
			}
			return true
		})

		if len(respLines) == 0 {
			// 函数没有任何响应调用
			findings = append(findings, Finding{
				Category: "gin-safety",
				Rule:     "handler-no-response",
				Severity: "error",
				File:     path,
				Line:     lineOf(pass.Fset, decl.Pos()),
				Message:  funcName(decl) + ": handler 函数无任何 HTTP 响应调用",
			})
			return
		}

		// 检查每个 return 前是否有响应
		ast.Inspect(decl.Body, func(n ast.Node) bool {
			ret, ok := n.(*ast.ReturnStmt)
			if !ok {
				return true
			}
			retLine := lineOf(pass.Fset, ret.Pos())
			hasResp := false
			for line := retLine - 10; line <= retLine; line++ {
				if respLines[line] {
					hasResp = true
					break
				}
			}
			if !hasResp {
				findings = append(findings, Finding{
					Category: "gin-safety",
					Rule:     "handler-no-response",
					Severity: "error",
					File:     path,
					Line:     retLine,
					Message:  funcName(decl) + ": return 前未写入 HTTP 响应，客户端将收不到回复",
				})
			}
			return true
		})
	})
	return findings
}

// checkShouldBindErr 检测 Gin ShouldBind*/Bind* 调用的错误返回值未处理。
//
// 规则：c.ShouldBindJSON / c.ShouldBind / c.ShouldBindQuery 等绑定方法在请求体
// 格式错误时返回 error。如果丢弃这个 error，handler 会使用零值 struct 继续处理，
// 导致空指针、业务逻辑错误或数据不一致。
//
// 检测方式：
//   1. 找到所有 ShouldBind*/Bind* 方法调用
//   2. 检查调用是否在 ExprStmt 中（返回值完全被丢弃）
//   3. 或者检查是否赋值给 _（err 被忽略）
//
// 豁免：测试文件、非 handler 包。
//
// 修复方式：检查错误并返回 400：
//
//	if err := c.ShouldBindJSON(&req); err != nil {
//	    c.JSON(400, gin.H{"error": err.Error()})
//	    return
//	}
func checkShouldBindErr(pass *CheckPass) []Finding {
	bindMethods := map[string]bool{
		"ShouldBindJSON": true, "ShouldBind": true, "ShouldBindQuery": true,
		"ShouldBindUri": true, "ShouldBindHeader": true, "ShouldBindYAML": true,
		"ShouldBindXML": true, "ShouldBindTOML": true,
		"BindJSON": true, "Bind": true, "BindQuery": true, "BindUri": true,
		"BindHeader": true, "BindYAML": true, "BindXML": true,
	}
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if !isHandlerPkg(path) || decl.Body == nil {
			return
		}

		for _, stmt := range decl.Body.List {
			// 情况 1：返回值完全被丢弃（作为 ExprStmt 调用）
			if exprStmt, ok := stmt.(*ast.ExprStmt); ok {
				if call, ok := exprStmt.X.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && bindMethods[sel.Sel.Name] {
						findings = append(findings, Finding{
							Category: "gin-safety",
							Rule:     "shouldbind-err",
							Severity: "error",
							File:     path,
							Line:     lineOf(pass.Fset, call.Pos()),
							Message:  funcName(decl) + ": c." + sel.Sel.Name + "() 错误返回值被丢弃",
						})
					}
				}
			}

			// 情况 2：赋值给 _ (  _ = c.ShouldBindJSON(&req) )
			if assign, ok := stmt.(*ast.AssignStmt); ok {
				if len(assign.Rhs) == 1 {
					if call, ok := assign.Rhs[0].(*ast.CallExpr); ok {
						if sel, ok := call.Fun.(*ast.SelectorExpr); ok && bindMethods[sel.Sel.Name] {
							for _, lhs := range assign.Lhs {
								if ident, ok := lhs.(*ast.Ident); ok && ident.Name == "_" {
									findings = append(findings, Finding{
										Category: "gin-safety",
										Rule:     "shouldbind-err",
										Severity: "error",
										File:     path,
										Line:     lineOf(pass.Fset, call.Pos()),
										Message:  funcName(decl) + ": c." + sel.Sel.Name + "() 错误被 _ 忽略",
									})
								}
							}
						}
					}
				}
			}
		}
	})
	return findings
}

// isHTTPResponseCall 判断 SelectorExpr 是否为 HTTP 响应调用。
//
// 匹配三类模式：
//   1. Gin 直接方法：c.JSON / c.Abort* 等（ginResponseMethods）
//   2. response 包封装：response.Success / response.BadRequest 等（responseWrapperMethods）
//   3. handler 错误处理方法：h.handleServiceError / h.handleAdminServiceError
//      （项目约定的错误响应封装，内部调用 c.JSON）
func isHTTPResponseCall(sel *ast.SelectorExpr) bool {
	// Gin 直接方法
	if ginResponseMethods[sel.Sel.Name] {
		return true
	}
	// response 包封装（支持别名如 pkgResponse、resp 等）
	if pkg, ok := sel.X.(*ast.Ident); ok {
		if responseWrapperMethods[sel.Sel.Name] && strings.Contains(strings.ToLower(pkg.Name), "response") {
			return true
		}
	}
	// handler 错误处理方法（h.handleServiceError / h.handleAdminServiceError）
	if strings.Contains(sel.Sel.Name, "handleServiceError") || strings.Contains(sel.Sel.Name, "handleAdminServiceError") {
		return true
	}
	return false
}

// isAnyResponseCall 综合判断一个 CallExpr 是否为 HTTP 响应调用。
//
// 按优先级依次检查：
//   1. SelectorExpr → isHTTPResponseCall（c.JSON、response.Success、h.handleServiceError 等）
//   2. 第一个参数是 *gin.Context → 辅助方法可能内部写了响应（如 h.checkReady(c)）
//   3. Ident（包级函数）→ 名称含 "handleServiceError"/"handleAdminServiceError" 或第一个参数是 c
func isAnyResponseCall(call *ast.CallExpr) bool {
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		if isHTTPResponseCall(sel) {
			return true
		}
	}
	// 包级函数：handleAdminServiceError(c, logger, err) 等
	if ident, ok := call.Fun.(*ast.Ident); ok {
		if strings.Contains(ident.Name, "handleServiceError") || strings.Contains(ident.Name, "handleAdminServiceError") {
			return true
		}
	}
	// 兜底：任何传递了 gin.Context 的辅助方法
	if callPassesGinContext(call) {
		return true
	}
	return false
}

// callPassesGinContext 检查函数调用的第一个参数是否为 *gin.Context 变量。
//
// 用于识别 handler 内部的辅助方法调用（如 h.checkReady(c)、h.handleServiceError(c, err)），
// 这些方法通常内部会写入 HTTP 响应。如果第一个参数名为 "c" 或类型为 *gin.Context，
// 则认为该调用可能已写入响应。
func callPassesGinContext(call *ast.CallExpr) bool {
	if len(call.Args) == 0 {
		return false
	}
	// 检查第一个参数是否是名为 "c" 的标识符（最常见的 gin.Context 变量名）
	if ident, ok := call.Args[0].(*ast.Ident); ok {
		if ident.Name == "c" {
			return true
		}
	}
	return false
}

// checkGinCtxEscape 检测 *gin.Context 被传入 goroutine（数据竞争风险）。
//
// 规则：Gin 在请求结束后会复用 *gin.Context 对象。如果 goroutine 持有 *gin.Context
// 的引用，可能在请求已结束后读写该对象，导致数据竞争。
//
// 合法模式：
//   - 在启动 goroutine 前调用 c.Copy() 创建副本
//   - 在 goroutine 启动前提取所有需要的值（如 userID := c.GetString("user_id")）
//
// 检测方式：
//   1. 在 handler/ 文件中查找 GoStmt（go func() { ... }()）
//   2. 检查 goroutine 函数体中是否使用了类型为 *gin.Context 的变量
//   3. 通过 types.Info 确认变量类型
//   4. 如果 goroutine 的参数列表中有 *gin.Context 也标记（即使通过参数传入也不安全）
//
// 豁免：测试文件、非 handler 包。
//
// 修复方式：在 goroutine 启动前提取值，或使用 c.Copy()。
func checkGinCtxEscape(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if !isHandlerPkg(path) || decl.Body == nil {
			return
		}

		ast.Inspect(decl.Body, func(n ast.Node) bool {
			goStmt, ok := n.(*ast.GoStmt)
			if !ok {
				return true
			}
			funcLit, ok := goStmt.Call.Fun.(*ast.FuncLit)
			if !ok {
				return true
			}

			// 检查 goroutine 前是否有 c.Copy()
			bodyStr := nodeStr(pass.Fset, decl.Body)
			goLine := lineOf(pass.Fset, goStmt.Pos())
			// 简单启发：如果函数体中在 go 语句之前有 .Copy()，认为已处理
			beforeGo := ""
			lines := strings.Split(bodyStr, "\n")
			startLine := lineOf(pass.Fset, decl.Body.Pos())
			for i, line := range lines {
				if startLine+i >= goLine {
					break
				}
				beforeGo += line + "\n"
			}
			if strings.Contains(beforeGo, ".Copy()") {
				return true
			}

			// 检查 goroutine 体中是否使用了 gin.Context 类型的变量
			ast.Inspect(funcLit.Body, func(inner ast.Node) bool {
				ident, ok := inner.(*ast.Ident)
				if !ok {
					return true
				}
				obj := pass.TypesInfo.ObjectOf(ident)
				if obj == nil {
					return true
				}
				typStr := obj.Type().String()
				if strings.Contains(typStr, "gin.Context") {
					findings = append(findings, Finding{
						Category: "gin-safety",
						Rule:     "gin-ctx-escape",
						Severity: "error",
						File:     path,
						Line:     lineOf(pass.Fset, goStmt.Pos()),
						Message:  funcName(decl) + ": *gin.Context 逃逸到 goroutine（请求结束后 Context 会被复用），应提前提取值或使用 c.Copy()",
					})
					return false // 一个 goroutine 只报一次
				}
				return true
			})

			return true
		})
	})
	return findings
}

// checkResponseShape 强制 handler 层 c.JSON 的 body 必须是 response.BaseResponse。
//
// 规则:handler 包里调 c.JSON(code, body) 时,body 的**类型**必须是
// internal/common/response.BaseResponse。否则前端约定的 {code,message,result,error}
// 契约会被破坏,前端只要不熟悉就会踩坑(比如"操作成功但 UI 不刷新",因为前端
// 误把"无 result 字段"当成"调用失败")。
//
// 判断方式:
//   1. 优先用 types.Info 查 body 表达式的静态类型,含 "response.BaseResponse" → 合法
//   2. 退路:body 是 CompositeLit 字面量,检查它的 Type 是不是 response.BaseResponse
//      (如 `response.BaseResponse{...}`、`&response.BaseResponse{...}`)
//   3. 其它情况(gin.H{}、map{...}、自定义 struct 字面量、&APIError{...} 等)→ 违规
//
// 豁免:
//   - 非 handler 包(中间件、cmd、tests):规则不适用
//   - c.Redirect / c.Status(204) / c.NoContent:不是 JSON 出口,天然不受本规则约束
//
// 修复方式:
//   - 用 helper: response.Success(c, "...", data) / response.BadRequest(c, ...)
//   - 或手写: c.JSON(code, response.BaseResponse{Code: code, Message: "...", Result: data})
func checkResponseShape(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(_ *ast.File, path string, decl *ast.FuncDecl) {
		if !isHandlerPkg(path) || decl.Body == nil {
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
			if sel.Sel.Name != "JSON" {
				return true
			}
			// 确认接收器是 *gin.Context;变量名启发式(c / ctx)+ types.Info 双保险
			recvIdent, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if !isGinContextIdent(pass, recvIdent) {
				return true
			}
			if len(call.Args) < 2 {
				return true
			}
			body := call.Args[1]

			// 通过 types.Info 查 body 的类型 —— 最可靠
			if tv, has := pass.TypesInfo.Types[body]; has && tv.Type != nil {
				if isBaseResponseType(tv.Type.String()) {
					return true
				}
			}

			// Fallback:body 是字面量时看它声明的 Type
			inner := body
			if unary, ok := inner.(*ast.UnaryExpr); ok {
				inner = unary.X // 支持 &response.BaseResponse{...}
			}
			if comp, ok := inner.(*ast.CompositeLit); ok {
				if isBaseResponseCompositeType(comp.Type) {
					return true
				}
			}

			findings = append(findings, Finding{
				Category: "gin-safety",
				Rule:     "response-shape",
				Severity: "error",
				File:     path,
				Line:     lineOf(pass.Fset, call.Pos()),
				Message:  funcName(decl) + ": c.JSON body 必须是 response.BaseResponse,禁止裸 map/gin.H/自定义 struct 以保持前端契约",
			})
			return true
		})
	})
	return findings
}

// isGinContextIdent 判断一个 Ident 是否为 *gin.Context 变量。
// 先用 types.Info 精确判断,失败时退回到常见变量名启发式(c / ctx)。
func isGinContextIdent(pass *CheckPass, ident *ast.Ident) bool {
	if obj := pass.TypesInfo.ObjectOf(ident); obj != nil && obj.Type() != nil {
		return strings.Contains(obj.Type().String(), "gin.Context")
	}
	return ident.Name == "c" || ident.Name == "ctx"
}

// isBaseResponseType 判断 types.Type 的字符串表达是否指向 response.BaseResponse。
// 容忍指针(*T)、包路径(.../common/response.BaseResponse)等变体。
func isBaseResponseType(typeStr string) bool {
	return strings.HasSuffix(typeStr, "/response.BaseResponse") ||
		strings.HasSuffix(typeStr, ".response.BaseResponse") ||
		typeStr == "response.BaseResponse"
}

// isBaseResponseCompositeType 判断 CompositeLit 的 Type 节点是不是 response.BaseResponse。
// 形如 `response.BaseResponse{...}` → SelectorExpr{X=Ident"response", Sel=Ident"BaseResponse"}
func isBaseResponseCompositeType(typ ast.Expr) bool {
	if sel, ok := typ.(*ast.SelectorExpr); ok {
		pkg, okPkg := sel.X.(*ast.Ident)
		if okPkg && sel.Sel.Name == "BaseResponse" && strings.Contains(strings.ToLower(pkg.Name), "response") {
			return true
		}
	}
	return false
}
