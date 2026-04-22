package main

import (
	"go/ast"
	"strings"
)

// checkCtxCancelLeak 检测 context.WithCancel/WithTimeout/WithDeadline 的 cancel 函数未 defer 调用。
//
// 规则：context.WithCancel、WithTimeout、WithDeadline 返回的 cancel 函数必须被 defer 调用，
// 否则会导致 goroutine 和 timer 泄漏。即使父 context 被取消，子 context 的资源也不会被释放，
// 直到 cancel 被调用或进程退出。
//
// 检测方式：
//   1. 找到 context.WithCancel/WithTimeout/WithDeadline 的赋值语句
//   2. 提取第二个返回值（cancel func）的变量名
//   3. 检查函数体中是否有 `defer cancelVar()` 调用
//   4. 如果 cancel 被赋值给 `_`，也标记为违规（cancel 被丢弃）
//
// 豁免：测试文件。
//
// 修复方式：在 WithCancel/WithTimeout/WithDeadline 后紧跟 `defer cancel()`。
func checkCtxCancelLeak(pass *CheckPass) []Finding {
	ctxFuncs := map[string]bool{
		"WithCancel": true, "WithTimeout": true, "WithDeadline": true,
	}
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil {
			return
		}

		// 收集所有 defer xxx() 中被调用的函数名
		deferredCalls := make(map[string]bool)
		ast.Inspect(decl.Body, func(n ast.Node) bool {
			ds, ok := n.(*ast.DeferStmt)
			if !ok {
				return true
			}
			// defer cancel()
			if ident, ok := ds.Call.Fun.(*ast.Ident); ok {
				deferredCalls[ident.Name] = true
			}
			// defer func() { cancel() }() — 也是合法的
			if fl, ok := ds.Call.Fun.(*ast.FuncLit); ok {
				ast.Inspect(fl.Body, func(inner ast.Node) bool {
					if call, ok := inner.(*ast.CallExpr); ok {
						if id, ok := call.Fun.(*ast.Ident); ok {
							deferredCalls[id.Name] = true
						}
					}
					return true
				})
			}
			return true
		})

		ast.Inspect(decl.Body, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok || len(assign.Rhs) != 1 {
				return true
			}
			call, ok := assign.Rhs[0].(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !ctxFuncs[sel.Sel.Name] {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "context" {
				return true
			}

			// 需要至少 2 个 LHS：ctx, cancel := context.WithCancel(...)
			if len(assign.Lhs) < 2 {
				return true
			}

			cancelIdent, ok := assign.Lhs[1].(*ast.Ident)
			if !ok {
				return true
			}

			// cancel 被赋值给 _ → 泄漏
			if cancelIdent.Name == "_" {
				findings = append(findings, Finding{
					Category: "concurrency",
					Rule:     "ctx-cancel-leak",
					Severity: "error",
					File:     path,
					Line:     lineOf(pass.Fset, assign.Pos()),
					Message:  funcName(decl) + ": context." + sel.Sel.Name + " 的 cancel 被丢弃（_ =），资源泄漏",
				})
				return true
			}

			// 检查是否有 defer cancel()
			if !deferredCalls[cancelIdent.Name] {
				findings = append(findings, Finding{
					Category: "concurrency",
					Rule:     "ctx-cancel-leak",
					Severity: "error",
					File:     path,
					Line:     lineOf(pass.Fset, assign.Pos()),
					Message:  funcName(decl) + ": context." + sel.Sel.Name + " 后缺少 defer " + cancelIdent.Name + "()",
				})
			}
			return true
		})
	})
	return findings
}

// checkCtxBackground 检测 handler/service 层滥用 context.Background() / context.TODO()。
//
// 规则：handler 和 service 层应始终传递请求 context 以保持 trace_id、deadline、
// cancel 信号的传播。使用 context.Background() 或 context.TODO() 会导致：
//   - OpenTelemetry trace 断裂（无法追踪请求链路）
//   - 上游 cancel 无法传播（请求取消后后端仍继续执行）
//   - 超时控制失效（无 deadline 约束）
//
// 检测方式：
//   1. 在 handler/ 和 service/ 包中查找 context.Background() / context.TODO() 调用
//   2. 通过 SelectorExpr 匹配 `context.Background` 和 `context.TODO`
//
// 豁免：
//   - 测试文件
//   - init() 函数（初始化阶段无请求 context）
//   - main 包
//
// 修复方式：使用从请求传递的 ctx 参数，而非新建空 context。
func checkCtxBackground(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil || decl.Name.Name == "init" {
			return
		}
		if !isServicePkg(path) && !isHandlerPkg(path) {
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
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "context" {
				return true
			}
			if sel.Sel.Name == "Background" || sel.Sel.Name == "TODO" {
				findings = append(findings, Finding{
					Category: "concurrency",
					Rule:     "ctx-background",
					Severity: "warning",
					File:     path,
					Line:     lineOf(pass.Fset, call.Pos()),
					Message:  funcName(decl) + ": " + strings.ToLower(sel.Sel.Name) + " 层使用 context." + sel.Sel.Name + "()，应传递请求 context",
				})
			}
			return true
		})
	})
	return findings
}
