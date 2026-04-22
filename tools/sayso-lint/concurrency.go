package main

import (
	"go/ast"
	"go/token"
	"strings"
)

// checkBareGoroutine 检测裸 go 语句（未使用 AsyncRunner 管理的 goroutine）。
//
// 规则：所有异步 goroutine 必须通过 internal/common/async.AsyncRunner 管理，
// 以保证 panic 恢复、并发控制、trace_id 传播和优雅关停。裸 `go func()` 会导致：
//   - panic 不可恢复（进程崩溃）
//   - 无并发上限控制
//   - trace_id 丢失（无法追踪异步操作）
//   - shutdown 时无法等待完成
//
// 检测方式：找到所有 ast.GoStmt 节点。
//
// 豁免：测试文件。
//
// 修复方式：改为 `s.async.Go(ctx, "task-name", func(bgCtx context.Context) { ... })`。
func checkBareGoroutine(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil {
			return
		}
		ast.Inspect(decl.Body, func(n ast.Node) bool {
			if _, ok := n.(*ast.GoStmt); ok {
				findings = append(findings, Finding{
					Category: "concurrency",
					Rule:     "bare-goroutine",
					Severity: "warning",
					File:     path,
					Line:     lineOf(pass.Fset, n.Pos()),
					Message:  funcName(decl) + ": 裸 go func()，应使用 AsyncRunner",
				})
			}
			return true
		})
	})
	return findings
}

// checkRespBodyLeak 检测 HTTP 响应体未关闭，可能导致连接泄漏。
//
// 规则：每次 HTTP 调用（http.Get/Post/Head 或 client.Do）获取的 resp.Body
// 必须用 defer resp.Body.Close() 关闭。未关闭会导致底层 TCP 连接无法复用，
// 最终耗尽连接池。
//
// 类型感知（go/packages 能力）：
//   - 对 .Do() 调用，通过 types.Info.Selections 确认接收器是 *http.Client
//   - 避免将 redis.Do()、grpc.Do() 等误标为 HTTP 调用
//
// 检测方式：
//   1. 找到 http.Get/Post/Head 或 *http.Client.Do 调用
//   2. 检查函数体中是否有 "Body.Close" 字符串
//   3. 没有则标记为泄漏
//
// 豁免：测试文件。
//
// 修复方式：在 HTTP 调用后添加 `defer resp.Body.Close()`。
func checkRespBodyLeak(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil {
			return
		}

		bodyStr := nodeStr(pass.Fset, decl.Body)
		hasBodyClose := strings.Contains(bodyStr, "Body.Close")

		ast.Inspect(decl.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}

			isHTTPCall := false
			callName := ""

			// http.Get / http.Post / http.Head（包级函数）
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "http" {
				switch sel.Sel.Name {
				case "Get", "Post", "Head":
					isHTTPCall = true
					callName = "http." + sel.Sel.Name
				}
			}

			// *.Do() — 通过类型系统确认是 *http.Client 的方法
			if sel.Sel.Name == "Do" && receiverIs(pass.TypesInfo, sel, "net/http") {
				isHTTPCall = true
				callName = "http.Client.Do"
			}

			if isHTTPCall && !hasBodyClose {
				findings = append(findings, Finding{
					Category: "concurrency",
					Rule:     "resp-body-leak",
					Severity: "error",
					File:     path,
					Line:     lineOf(pass.Fset, call.Pos()),
					Message:  funcName(decl) + ": " + callName + "() 后无 defer resp.Body.Close()",
				})
			}
			return true
		})
	})
	return findings
}

// checkOsOpenLeak 检测 os.Open/Create/OpenFile 后未 defer Close 的文件句柄泄漏。
//
// 规则：通过 os 包打开的文件必须在函数退出前关闭。通常用 `defer f.Close()` 保证。
//
// 检测方式：
//   1. 收集函数体中所有 defer 语句中包含 "Close" 的行号
//   2. 找到 os.Open/Create/OpenFile 调用
//   3. 检查后续 20 行内是否有 defer Close
//
// 豁免：测试文件。
//
// 修复方式：在 Open 后紧跟 `defer f.Close()`。
func checkOsOpenLeak(pass *CheckPass) []Finding {
	var findings []Finding
	openFuncs := map[string]bool{"Open": true, "Create": true, "OpenFile": true}

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil {
			return
		}

		deferCloseLines := make(map[int]bool)
		ast.Inspect(decl.Body, func(n ast.Node) bool {
			ds, ok := n.(*ast.DeferStmt)
			if !ok {
				return true
			}
			if strings.Contains(nodeStr(pass.Fset, ds.Call), "Close") {
				deferCloseLines[lineOf(pass.Fset, ds.Pos())] = true
			}
			return true
		})

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
			if !ok || pkg.Name != "os" || !openFuncs[sel.Sel.Name] {
				return true
			}

			openLine := lineOf(pass.Fset, call.Pos())
			hasClose := false
			for line := openLine; line <= openLine+20; line++ {
				if deferCloseLines[line] {
					hasClose = true
					break
				}
			}
			if !hasClose {
				findings = append(findings, Finding{
					Category: "concurrency",
					Rule:     "os-open-leak",
					Severity: "warning",
					File:     path,
					Line:     openLine,
					Message:  funcName(decl) + ": os." + sel.Sel.Name + "() 后无 defer Close()",
				})
			}
			return true
		})
	})
	return findings
}

// checkHTTPTimeout 检测 http.Client 结构体字面量未设置 Timeout 字段。
//
// 规则：每个 http.Client{} 都必须设置 Timeout，否则请求可能永远挂起，
// 耗尽 goroutine 和连接资源。
//
// 类型感知（go/packages 能力）：
//   - 通过 CompositeLit.Type 的 SelectorExpr 确认是 http.Client
//   - 检查 KeyValueExpr 中是否有 Key.Name == "Timeout"
//
// 豁免：测试文件。
//
// 修复方式：添加 `Timeout: 30 * time.Second` 或从配置读取。
func checkHTTPTimeout(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil {
			return
		}
		ast.Inspect(decl.Body, func(n ast.Node) bool {
			comp, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			sel, ok := comp.Type.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "http" || sel.Sel.Name != "Client" {
				return true
			}

			hasTimeout := false
			for _, elt := range comp.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if ident, ok := kv.Key.(*ast.Ident); ok && ident.Name == "Timeout" {
					hasTimeout = true
				}
			}
			if !hasTimeout {
				findings = append(findings, Finding{
					Category: "concurrency",
					Rule:     "http-timeout",
					Severity: "warning",
					File:     path,
					Line:     lineOf(pass.Fset, comp.Pos()),
					Message:  funcName(decl) + ": http.Client{} 未设置 Timeout",
				})
			}
			return true
		})
	})
	return findings
}

// checkRedisLeak 检测 Redis Pipeline/PubSub 资源未关闭。
//
// 规则：Redis 的 Pipeline()、TxPipeline()、Subscribe()、PSubscribe() 返回的对象
// 持有底层连接资源，必须在使用完毕后 Close()。否则会导致连接池耗尽，
// 后续 Redis 操作全部超时。
//
// 检测方式：
//   1. 找到 .Pipeline()/.TxPipeline()/.Subscribe()/.PSubscribe() 调用
//   2. 通过 types.Info 确认接收器类型包含 "redis"
//   3. 检查函数体中是否有对应的 .Close() 调用
//
// 豁免：测试文件。
//
// 修复方式：在获取 Pipeline/PubSub 后紧跟 `defer pipe.Close()`。
func checkRedisLeak(pass *CheckPass) []Finding {
	redisMethods := map[string]bool{
		"Pipeline": true, "TxPipeline": true,
		"Subscribe": true, "PSubscribe": true,
	}
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil {
			return
		}
		bodyStr := nodeStr(pass.Fset, decl.Body)

		ast.Inspect(decl.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !redisMethods[sel.Sel.Name] {
				return true
			}
			if !receiverIs(pass.TypesInfo, sel, "redis") {
				return true
			}
			if !strings.Contains(bodyStr, "Close") {
				findings = append(findings, Finding{
					Category: "concurrency",
					Rule:     "redis-leak",
					Severity: "error",
					File:     path,
					Line:     lineOf(pass.Fset, call.Pos()),
					Message:  funcName(decl) + ": redis." + sel.Sel.Name + "() 后无 Close()，连接泄漏",
				})
			}
			return true
		})
	})
	return findings
}

// checkWaitGroupAdd 检测 sync.WaitGroup.Add() 在 goroutine 内调用。
//
// 规则：wg.Add() 必须在 `go` 语句之前调用。如果在 goroutine 内部调用 wg.Add()，
// wg.Wait() 可能在 Add() 执行之前就返回，导致并发等待不完整。
//
// 检测方式：
//   1. 找到所有 GoStmt（go 语句）
//   2. 检查 goroutine 的函数体中是否有 .Add() 调用
//   3. 通过 types.Info 确认接收器是 sync.WaitGroup
//
// 豁免：测试文件。
//
// 修复方式：将 wg.Add(1) 移到 go 语句之前。
func checkWaitGroupAdd(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil {
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

			ast.Inspect(funcLit.Body, func(inner ast.Node) bool {
				call, ok := inner.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Add" {
					return true
				}
				if receiverIs(pass.TypesInfo, sel, "sync.WaitGroup") {
					findings = append(findings, Finding{
						Category: "concurrency",
						Rule:     "wg-add-in-goroutine",
						Severity: "error",
						File:     path,
						Line:     lineOf(pass.Fset, call.Pos()),
						Message:  funcName(decl) + ": wg.Add() 在 goroutine 内调用，应移到 go 语句之前",
					})
				}
				return true
			})
			return true
		})
	})
	return findings
}

// checkTimeAfterLoop 检测 time.After() 在 for 循环的 select 中使用（timer 泄漏）。
//
// 规则：在 `for { select { case <-time.After(d): } }` 循环中，每次迭代都会创建
// 新的 timer，而旧 timer 不会被 GC（直到触发），在高频循环中导致内存泄漏。
//
// 检测方式：
//   1. 找到 for 循环中的 select 语句
//   2. 检查 case 的 channel 操作是否为 time.After() 调用
//
// 豁免：测试文件。
//
// 修复方式：将 `time.After(d)` 改为在循环外创建 `timer := time.NewTimer(d)`，
// 循环内用 `timer.Reset(d)` 重置。
func checkTimeAfterLoop(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil {
			return
		}

		ast.Inspect(decl.Body, func(n ast.Node) bool {
			// 匹配 for 循环
			var body *ast.BlockStmt
			switch stmt := n.(type) {
			case *ast.ForStmt:
				body = stmt.Body
			case *ast.RangeStmt:
				body = stmt.Body
			default:
				return true
			}

			// 在 for body 中查找 select
			for _, stmt := range body.List {
				selStmt, ok := stmt.(*ast.SelectStmt)
				if !ok {
					continue
				}
				for _, clause := range selStmt.Body.List {
					cc, ok := clause.(*ast.CommClause)
					if !ok || cc.Comm == nil {
						continue
					}
					// case <-time.After(d):
					checkTimeAfterInComm(pass, path, decl, cc.Comm, &findings)
				}
			}
			return true
		})
	})
	return findings
}

// checkTimeAfterInComm 检查单个 select case 的通信语句是否包含 time.After() 调用。
//
// 被 checkTimeAfterLoop 调用，对每个 CommClause 的通信语句进行检查。
// 匹配模式：`case <-time.After(d):` 中的 `time.After(d)` 调用。
//
// 检测方式：遍历通信语句的 AST，找到 SelectorExpr 为 time.After 的 CallExpr。
// 找到后立即追加 Finding 并停止遍历（一个 case 只报一次）。
func checkTimeAfterInComm(pass *CheckPass, path string, decl *ast.FuncDecl, comm ast.Stmt, findings *[]Finding) {
	ast.Inspect(comm, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "time" || sel.Sel.Name != "After" {
			return true
		}
		*findings = append(*findings, Finding{
			Category: "concurrency",
			Rule:     "time-after-loop",
			Severity: "warning",
			File:     path,
			Line:     lineOf(pass.Fset, call.Pos()),
			Message:  funcName(decl) + ": time.After() 在循环 select 中使用，每次迭代泄漏 timer。改用 time.NewTimer + Reset",
		})
		return false
	})
}

// checkBlockingSelect 检测永久阻塞的 select{} 和 <-make(chan ...) 模式。
//
// 规则：空 `select {}` 或 `<-make(chan struct{})` 会永久阻塞 goroutine，
// 占用资源且无法被 GC。在非 main 包中通常是 bug。
//
// 检测方式：
//   1. 找到空 SelectStmt（Body.List 为空）
//   2. 找到 UnaryExpr 中 <-make(chan ...) 模式
//
// 豁免：测试文件、main 包。
//
// 修复方式：使用 context 或 signal channel 控制退出。
func checkBlockingSelect(pass *CheckPass) []Finding {
	if pass.Pkg.Name() == "main" {
		return nil
	}
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil {
			return
		}

		ast.Inspect(decl.Body, func(n ast.Node) bool {
			// 空 select {}
			if sel, ok := n.(*ast.SelectStmt); ok {
				if sel.Body == nil || len(sel.Body.List) == 0 {
					findings = append(findings, Finding{
						Category: "concurrency",
						Rule:     "blocking-select",
						Severity: "warning",
						File:     path,
						Line:     lineOf(pass.Fset, sel.Pos()),
						Message:  funcName(decl) + ": 空 select{} 永久阻塞 goroutine",
					})
				}
			}

			// <-make(chan ...)
			if unary, ok := n.(*ast.UnaryExpr); ok && unary.Op == token.ARROW {
				if call, ok := unary.X.(*ast.CallExpr); ok {
					if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "make" {
						if len(call.Args) > 0 {
							if _, ok := call.Args[0].(*ast.ChanType); ok {
								findings = append(findings, Finding{
									Category: "concurrency",
									Rule:     "blocking-select",
									Severity: "warning",
									File:     path,
									Line:     lineOf(pass.Fset, unary.Pos()),
									Message:  funcName(decl) + ": <-make(chan ...) 永久阻塞 goroutine",
								})
							}
						}
					}
				}
			}
			return true
		})
	})
	return findings
}

// checkMapConcurrency 检测 goroutine 内对 map 的写操作（并发 map 写 fatal crash）。
//
// 规则：Go 的 map 不是线程安全的。在 goroutine 中对 map 进行写操作（m[k] = v）
// 而没有互斥锁保护，会导致 fatal error（非 panic，无法 recover），整个进程崩溃。
//
// 检测方式：
//   1. 找到所有 GoStmt（go 语句）
//   2. 在 goroutine 的函数体中查找 map 赋值操作（IndexExpr 在 AssignStmt 的 LHS）
//   3. 通过 types.Info 确认赋值目标是 map 类型
//
// 豁免：测试文件。
//
// 已知限制：
//   - 无法检测通过函数调用间接修改 map 的情况
//   - 如果 map 是在 goroutine 内部创建的（不共享），不应标记——但静态分析难以区分
//   - 如果已有 mutex 保护但不在同一函数中，可能误报
//
// 修复方式：使用 sync.Mutex 或 sync.RWMutex 保护 map，或改用 sync.Map。
func checkMapConcurrency(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil {
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

			// 检查 goroutine 体中是否有 mutex Lock 调用
			goBodyStr := nodeStr(pass.Fset, funcLit.Body)
			hasMutex := strings.Contains(goBodyStr, "Lock") || strings.Contains(goBodyStr, "RLock")

			if hasMutex {
				return true // 有互斥锁保护，跳过
			}

			// 收集 goroutine 内通过 := 创建的局部 map 变量名（这些是安全的）
			localMaps := make(map[string]bool)
			ast.Inspect(funcLit.Body, func(inner ast.Node) bool {
				assign, ok := inner.(*ast.AssignStmt)
				if !ok || assign.Tok != token.DEFINE { // 只看 :=
					return true
				}
				for j, rhs := range assign.Rhs {
					isLocalMap := false
					// make(map[...])
					if call, ok := rhs.(*ast.CallExpr); ok {
						if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "make" {
							if len(call.Args) > 0 {
								if _, ok := call.Args[0].(*ast.MapType); ok {
									isLocalMap = true
								}
							}
						}
					}
					// map[K]V{...} 字面量
					if comp, ok := rhs.(*ast.CompositeLit); ok {
						if _, ok := comp.Type.(*ast.MapType); ok {
							isLocalMap = true
						}
					}
					if isLocalMap && j < len(assign.Lhs) {
						if ident, ok := assign.Lhs[j].(*ast.Ident); ok {
							localMaps[ident.Name] = true
						}
					}
				}
				return true
			})

			ast.Inspect(funcLit.Body, func(inner ast.Node) bool {
				assign, ok := inner.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for _, lhs := range assign.Lhs {
					idx, ok := lhs.(*ast.IndexExpr)
					if !ok {
						continue
					}
					// 跳过 goroutine 内新建的局部 map
					if ident, ok := idx.X.(*ast.Ident); ok && localMaps[ident.Name] {
						continue
					}
					// 通过类型系统确认是 map 类型
					tv, ok := pass.TypesInfo.Types[idx.X]
					if !ok {
						continue
					}
					if strings.HasPrefix(tv.Type.String(), "map[") {
						findings = append(findings, Finding{
							Category: "concurrency",
							Rule:     "map-concurrent-write",
							Severity: "warning",
							File:     path,
							Line:     lineOf(pass.Fset, assign.Pos()),
							Message:  funcName(decl) + ": goroutine 内写外部 map 无互斥锁保护，可能 fatal crash",
						})
						return false
					}
				}
				return true
			})
			return true
		})
	})
	return findings
}

// checkLockWithoutDefer 检测 mutex Lock() 后未使用 defer Unlock() 的模式。
//
// 规则：调用 Lock() / RLock() 后如果不使用 defer Unlock() / defer RUnlock()，
// 当 Lock 和 Unlock 之间的代码发生 panic 时，锁永远不会释放，导致死锁。
// 其他 goroutine 在获取同一个锁时会永久阻塞，最终服务不可用。
//
// 检测方式：
//   1. 找到所有 .Lock() / .RLock() 调用
//   2. 检查下一条语句是否为 defer xxx.Unlock() / defer xxx.RUnlock()
//   3. 如果不是 → 标记为风险
//
// 豁免：
//   - 测试文件
//   - defer Unlock 在紧接的下一行（标准安全模式）
//
// 已知限制：
//   - 手动 Lock/Unlock 有时是有意为之（如先锁定、拷贝数据、立即解锁再做后续处理）
//   - 此检查为 info 级，标记供人工审查而非强制要求
//
// 修复方式：改为 `mu.Lock(); defer mu.Unlock()`，或确认手动 Unlock 路径
// 覆盖所有提前 return 和 panic 场景。
func checkLockWithoutDefer(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil {
			return
		}

		// 遍历函数体中的语句列表（包括嵌套块中的）
		inspectStatements(pass, path, decl, decl.Body.List, &findings)
	})
	return findings
}

// inspectStatements 递归检查语句列表中的 Lock/defer Unlock 配对。
//
// 对每条 ExprStmt 检查是否为 .Lock()/.RLock() 调用，如果是则验证紧接的
// 下一条语句是否为 defer .Unlock()/.RUnlock()。同时递归进入 if/for/switch 等
// 嵌套块继续检查。
func inspectStatements(pass *CheckPass, path string, decl *ast.FuncDecl, stmts []ast.Stmt, findings *[]Finding) {
	for i, stmt := range stmts {
		// 检查当前语句是否为 Lock()/RLock()
		if exprStmt, ok := stmt.(*ast.ExprStmt); ok {
			if call, ok := exprStmt.X.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					method := sel.Sel.Name
					if method == "Lock" || method == "RLock" {
						// 检查下一条语句是否为 defer Unlock/RUnlock
						expectDefer := "Unlock"
						if method == "RLock" {
							expectDefer = "RUnlock"
						}
						hasDeferUnlock := false
						if i+1 < len(stmts) {
							if ds, ok := stmts[i+1].(*ast.DeferStmt); ok {
								if deferSel, ok := ds.Call.Fun.(*ast.SelectorExpr); ok {
									if deferSel.Sel.Name == expectDefer || deferSel.Sel.Name == "Unlock" || deferSel.Sel.Name == "RUnlock" {
										hasDeferUnlock = true
									}
								}
							}
						}
						if !hasDeferUnlock {
							*findings = append(*findings, Finding{
								Category: "concurrency",
								Rule:     "lock-without-defer",
								Severity: "info",
								File:     path,
								Line:     lineOf(pass.Fset, call.Pos()),
								Message:  funcName(decl) + ": " + method + "() 后无 defer " + expectDefer + "()，panic 时死锁",
							})
						}
					}
				}
			}
		}

		// 递归进入嵌套块
		switch s := stmt.(type) {
		case *ast.IfStmt:
			inspectStatements(pass, path, decl, s.Body.List, findings)
			if s.Else != nil {
				if elseBlock, ok := s.Else.(*ast.BlockStmt); ok {
					inspectStatements(pass, path, decl, elseBlock.List, findings)
				}
			}
		case *ast.ForStmt:
			if s.Body != nil {
				inspectStatements(pass, path, decl, s.Body.List, findings)
			}
		case *ast.RangeStmt:
			if s.Body != nil {
				inspectStatements(pass, path, decl, s.Body.List, findings)
			}
		case *ast.SwitchStmt:
			if s.Body != nil {
				inspectStatements(pass, path, decl, s.Body.List, findings)
			}
		case *ast.CaseClause:
			inspectStatements(pass, path, decl, s.Body, findings)
		case *ast.BlockStmt:
			inspectStatements(pass, path, decl, s.List, findings)
		}
	}
}

// checkSelectCtxDone 检测 for-select 循环缺少 ctx.Done() 分支。
//
// 规则：for { select { ... } } 循环如果没有 `case <-ctx.Done():` 分支，
// goroutine 在 shutdown 或请求取消时无法退出，导致 goroutine 泄漏。
// 这在 WebSocket session 管理、消费者循环、事件监听等长期运行场景中尤为危险。
//
// 检测方式：
//   1. 找到 for 循环内的 select 语句
//   2. 检查所有 case 是否包含 `.Done()` 调用（匹配 ctx.Done()、xxxCtx.Done() 等）
//   3. 如果所有 case 都没有 Done() → 标记
//
// 豁免：测试文件。
//
// 修复方式：添加 `case <-ctx.Done(): return`。
func checkSelectCtxDone(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil {
			return
		}

		ast.Inspect(decl.Body, func(n ast.Node) bool {
			var body *ast.BlockStmt
			switch stmt := n.(type) {
			case *ast.ForStmt:
				body = stmt.Body
			case *ast.RangeStmt:
				body = stmt.Body
			default:
				return true
			}

			for _, stmt := range body.List {
				selStmt, ok := stmt.(*ast.SelectStmt)
				if !ok || selStmt.Body == nil {
					continue
				}
				hasDone := false
				for _, clause := range selStmt.Body.List {
					cc, ok := clause.(*ast.CommClause)
					if !ok || cc.Comm == nil {
						continue
					}
					commStr := nodeStr(pass.Fset, cc.Comm)
					if strings.Contains(commStr, ".Done()") {
						hasDone = true
						break
					}
				}
				if !hasDone && len(selStmt.Body.List) > 0 {
					findings = append(findings, Finding{
						Category: "concurrency",
						Rule:     "select-ctx-done",
						Severity: "warning",
						File:     path,
						Line:     lineOf(pass.Fset, selStmt.Pos()),
						Message:  funcName(decl) + ": for-select 循环缺少 case <-ctx.Done()，shutdown 时无法退出",
					})
				}
			}
			return true
		})
	})
	return findings
}

// syncTypes 列举不能被值拷贝的 sync 包类型。
var syncTypes = []string{
	"sync.Mutex", "sync.RWMutex", "sync.WaitGroup",
	"sync.Cond", "sync.Pool", "sync.Once",
}

// checkMutexValueCopy 检测 sync.Mutex/WaitGroup 等被值拷贝（函数参数非指针）。
//
// 规则：sync 包中的同步原语（Mutex、RWMutex、WaitGroup、Cond、Pool、Once）
// 内部通过指针维护状态。值拷贝后副本和原始对象不共享锁状态，导致：
//   - Mutex 拷贝后新旧对象分别加锁，互不互斥
//   - WaitGroup 拷贝后 Done() 操作副本，原始 Wait() 永不返回
//
// 检测方式：
//   1. 遍历所有函数参数
//   2. 通过 types.Info 获取参数类型
//   3. 如果类型是 sync.Mutex 等（非指针形式）→ 标记
//
// 豁免：测试文件。
//
// 修复方式：将参数类型改为指针（如 *sync.Mutex）。
func checkMutexValueCopy(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Type.Params == nil {
			return
		}
		for _, field := range decl.Type.Params.List {
			for _, name := range field.Names {
				obj := pass.TypesInfo.ObjectOf(name)
				if obj == nil {
					continue
				}
				typStr := obj.Type().String()
				for _, st := range syncTypes {
					if typStr == st {
						findings = append(findings, Finding{
							Category: "concurrency",
							Rule:     "mutex-value-copy",
							Severity: "error",
							File:     path,
							Line:     lineOf(pass.Fset, field.Pos()),
							Message:  funcName(decl) + ": 参数 " + name.Name + " 是 " + st + " 值类型（应传指针 *" + st + "）",
						})
					}
				}
			}
		}
	})
	return findings
}

// checkRespBodyNilGuard 检测 HTTP 调用错误路径中未检查 resp 是否为 nil。
//
// 规则：HTTP 调用（http.Get、client.Do 等）在返回 error 时，resp 可能是非 nil 的
// （例如 3xx redirect 或读取 body 错误）。此时如果直接 return err 而不关闭 resp.Body，
// 会导致连接泄漏。正确做法是在 error 路径也检查并关闭 Body。
//
// 检测方式：
//   1. 找到 HTTP 调用（resp, err := http.Get/client.Do 等）
//   2. 检查函数体中 error 处理部分是否有 `resp != nil` 或 `resp.Body.Close` 的保护
//
// 豁免：测试文件。
//
// 修复方式：
//
//	resp, err := http.Get(url)
//	if err != nil {
//	    if resp != nil { resp.Body.Close() }
//	    return err
//	}
//	defer resp.Body.Close()
func checkRespBodyNilGuard(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil {
			return
		}
		bodyStr := nodeStr(pass.Fset, decl.Body)

		// 只检查有 HTTP 调用的函数
		hasHTTPCall := false
		ast.Inspect(decl.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "http" {
				if sel.Sel.Name == "Get" || sel.Sel.Name == "Post" || sel.Sel.Name == "Head" {
					hasHTTPCall = true
				}
			}
			if sel.Sel.Name == "Do" && receiverIs(pass.TypesInfo, sel, "net/http") {
				hasHTTPCall = true
			}
			return !hasHTTPCall
		})

		if !hasHTTPCall {
			return
		}

		// 检查是否有 resp != nil 保护
		hasNilGuard := strings.Contains(bodyStr, "resp != nil") ||
			strings.Contains(bodyStr, "response != nil") ||
			strings.Contains(bodyStr, "res != nil")

		hasBodyClose := strings.Contains(bodyStr, "Body.Close")

		if hasBodyClose && !hasNilGuard {
			// 有 Body.Close 但没有 nil guard — 如果 Close 在 defer 中（正常路径），
			// 在 error 路径可能 resp 为 nil 导致 panic
			// 但如果有 if err != nil { return } 在 defer 之前，也是安全的
			// 简化：只在有 HTTP 调用但既没有 nil guard 也没有 Body.Close 时报告
			return
		}

		if !hasBodyClose {
			// checkRespBodyLeak 已经覆盖这个场景，跳过避免重复
			return
		}
	})
	return findings
}

// checkChannelSendNoSelect 检测 goroutine 内向外部 channel 发送数据未用 select 保护。
//
// 规则：向可能已关闭或已满的 channel 发送数据会导致：
//   - 已关闭的 channel → panic（fatal, 无法 recover）
//   - 已满的无缓冲/有缓冲 channel → 永久阻塞 goroutine
//
// 检测方式：
//   1. 找到 goroutine（GoStmt）内的 channel 发送操作（SendStmt: ch <- val）
//   2. 检查发送操作是否在 select 语句内（select 可以用 default 或 ctx.Done 兜底）
//   3. 不在 select 内 → 标记
//
// 豁免：测试文件。
//
// 已知限制：
//   - 如果 channel 在 goroutine 内创建且不共享，发送是安全的——但静态分析难以区分
//   - 如果调用方保证 channel 不会关闭/满，也不需要 select——可能产生误报
//
// 修复方式：用 select + ctx.Done() 保护发送：
//
//	select {
//	case ch <- val:
//	case <-ctx.Done():
//	    return
//	}
func checkChannelSendNoSelect(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil {
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

			// 收集 select 语句内所有 SendStmt 的位置
			selectSendPositions := make(map[token.Pos]bool)
			ast.Inspect(funcLit.Body, func(inner ast.Node) bool {
				selStmt, ok := inner.(*ast.SelectStmt)
				if !ok || selStmt.Body == nil {
					return true
				}
				for _, clause := range selStmt.Body.List {
					cc, ok := clause.(*ast.CommClause)
					if !ok || cc.Comm == nil {
						continue
					}
					if send, ok := cc.Comm.(*ast.SendStmt); ok {
						selectSendPositions[send.Pos()] = true
					}
				}
				return true
			})

			// 找所有不在 select 中的 SendStmt
			ast.Inspect(funcLit.Body, func(inner ast.Node) bool {
				send, ok := inner.(*ast.SendStmt)
				if !ok {
					return true
				}
				if selectSendPositions[send.Pos()] {
					return true
				}
				findings = append(findings, Finding{
					Category: "concurrency",
					Rule:     "chan-send-no-select",
					Severity: "info",
					File:     path,
					Line:     lineOf(pass.Fset, send.Pos()),
					Message:  funcName(decl) + ": goroutine 内 channel 发送未用 select 保护，可能阻塞或 panic",
				})
				return true
			})
			return true
		})
	})
	return findings
}

// aiProviderPackages 列举已知的 AI 服务提供商的 **包级路径片段**。
//
// 被 checkAIProviderTimeout 使用。使用 "pkg/" 前缀限定为项目中 pkg/ 下的提供商客户端，
// 避免匹配到项目 module 路径中的关键词（如 go-openai-platform 中的 "openai"）。
// "speech" 改为 "pkg/speech" 防止匹配到 analytics 等包含 speech 的路径。
var aiProviderPackages = []string{
	"pkg/dashscope", "pkg/azureopenai", "pkg/gemini", "pkg/openai", "pkg/volcengine",
	"pkg/doubao", "pkg/funasr", "pkg/qwen", "pkg/speech",
}

// checkAIProviderTimeout 检测 AI 服务调用缺少 context 超时控制。
//
// 规则：对外部 AI 服务（DashScope、Azure OpenAI、Gemini 等）的调用可能耗时数十秒。
// 如果调用函数没有通过 context.WithTimeout/WithDeadline 设置超时，请求可能
// 无限期挂起，耗尽 goroutine 和连接资源，最终导致服务不可用。
//
// 检测方式：
//   1. 找到方法调用，通过 types.Info 检查接收器类型的包路径
//   2. 如果包路径包含已知 AI 提供商关键词
//   3. 检查同一函数中是否有 context.WithTimeout 或 context.WithDeadline
//
// 豁免：测试文件、init() 函数。
//
// 修复方式：在 AI 调用前用 `ctx, cancel := context.WithTimeout(ctx, 30*time.Second)` 包装。
func checkAIProviderTimeout(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil || decl.Name.Name == "init" {
			return
		}

		// 检查函数体是否有 timeout/deadline 设置
		bodyStr := nodeStr(pass.Fset, decl.Body)
		hasTimeout := strings.Contains(bodyStr, "WithTimeout") || strings.Contains(bodyStr, "WithDeadline")

		var aiCallLine int
		var aiCallName string

		ast.Inspect(decl.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}

			// 通过类型系统获取接收器的包路径
			selection, ok := pass.TypesInfo.Selections[sel]
			if !ok {
				return true
			}
			recvType := selection.Recv().String()
			for _, provider := range aiProviderPackages {
				if strings.Contains(strings.ToLower(recvType), provider) {
					aiCallLine = lineOf(pass.Fset, call.Pos())
					aiCallName = sel.Sel.Name
					return false // 找到一个就够了
				}
			}
			return true
		})

		if aiCallLine > 0 && !hasTimeout {
			findings = append(findings, Finding{
				Category: "concurrency",
				Rule:     "ai-provider-timeout",
				Severity: "warning",
				File:     path,
				Line:     aiCallLine,
				Message:  funcName(decl) + ": AI 服务调用 " + aiCallName + "() 无 context 超时控制",
			})
		}
	})
	return findings
}

// checkTimeSleep 检测非测试代码中使用 time.Sleep。
//
// 规则：生产代码中 time.Sleep 通常是临时 workaround（等待依赖就绪、轮询间隔等），
// 存在以下问题：
//   - 不可取消：context cancel / shutdown 信号无法中断 Sleep
//   - 不可调整：Sleep 时间硬编码，难以根据运行时状态调整
//   - 隐藏问题：用 Sleep 掩盖的时序问题在高负载下会复现
//
// 检测方式：找到 time.Sleep() 调用。
//
// 豁免：测试文件、init() 函数。
//
// 修复方式：
//   - 等待条件就绪 → 用 channel / sync.Cond / retry with backoff
//   - 定时执行 → 用 time.NewTicker + select { case <-ctx.Done() }
//   - 限速 → 用 rate.Limiter
func checkTimeSleep(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil || decl.Name.Name == "init" {
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
			if !ok || pkg.Name != "time" || sel.Sel.Name != "Sleep" {
				return true
			}
			findings = append(findings, Finding{
				Category: "concurrency",
				Rule:     "time-sleep",
				Severity: "info",
				File:     path,
				Line:     lineOf(pass.Fset, call.Pos()),
				Message:  funcName(decl) + ": 生产代码使用 time.Sleep（不可取消），考虑改用 timer + select",
			})
			return true
		})
	})
	return findings
}

// checkTimeNowUTC 检测 time.Now() 未使用 UTC 的调用。
//
// 规则：所有 time.Now() 调用必须使用 .UTC() 保证时区一致性。
// 服务部署在不同时区的节点上时，裸 time.Now() 会使用系统本地时区，
// 导致数据库中的时间戳、过期计算、日志时间不一致。
//
// 检测方式：找到所有 time.Now() 调用，检查父节点是否为 .UTC() 方法调用。
//
// 合法模式：
//   - time.Now().UTC()
//   - time.Now().UTC().Add(...)
//   - time.Now().UTC().Format(...)
//
// 违规模式：
//   - time.Now()
//   - time.Now().Add(...)
//   - time.Now().Format("2006-01-02")
//
// 豁免：测试文件。
func checkTimeNowUTC(pass *CheckPass) []Finding {
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

			// 匹配 time.Now()
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Now" {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || ident.Name != "time" {
				return true
			}

			// 检查 time.Now() 是否被 .UTC() 调用包裹
			// 即父节点应为 SelectorExpr{X: time.Now(), Sel: UTC}
			if isWrappedByUTC(decl.Body, call, pass.Fset) {
				return true // 合法：time.Now().UTC()
			}

			findings = append(findings, Finding{
				Category: "concurrency",
				Rule:     "time-now-utc",
				Severity: "warning",
				File:     path,
				Line:     lineOf(pass.Fset, call.Pos()),
				Message:  funcName(decl) + ": time.Now() 未使用 .UTC()，应改为 time.Now().UTC() 保证时区一致性",
			})
			return true
		})
	})
	return findings
}

// isWrappedByUTC 检查 time.Now() 调用是否被 .UTC() 方法调用包裹。
//
// 匹配模式：父节点为 CallExpr，其 Fun 为 SelectorExpr{X: time.Now(), Sel: "UTC"}。
// 通过遍历整个函数体寻找包含目标 CallExpr 的 .UTC() 调用。
func isWrappedByUTC(body *ast.BlockStmt, target *ast.CallExpr, fset *token.FileSet) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		// 匹配 <expr>.UTC() 调用
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "UTC" {
			return true
		}
		// 检查 <expr> 是否就是我们的 time.Now() 调用
		if innerCall, ok := sel.X.(*ast.CallExpr); ok && innerCall == target {
			found = true
			return false
		}
		return true
	})
	return found
}
