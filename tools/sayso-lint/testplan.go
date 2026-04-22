// testplan.go — sayso-lint 测试重规划子命令。
//
// 用法:
//
//	sayso-lint retest <module-name>
//
// 功能:
//  1. 删除 internal/{module}/ 下所有 _test.go 文件
//  2. 基于源码 AST + 类型分析，生成 JSON 测试计划到 stdout
//
// 输出格式: TestPlan JSON → stdout，删除日志和摘要 → stderr。
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/types"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"
)

// ── 测试计划数据结构 ────────────────────────────────────────────────────────

// TestPlan 表示一次测试重规划的完整输出。
//
// 包含三部分信息:
//   - Module:   被分析的模块名
//   - Deleted:  本次删除的旧测试文件路径列表
//   - Packages: 按包分组的测试计划
type TestPlan struct {
	Module   string        `json:"module"`
	Deleted  []string      `json:"deleted"`
	Packages []PackagePlan `json:"packages"`
}

// PackagePlan 表示单个包的测试规划。
//
// 包含:
//   - 包的路径和层级（service/handler/repository/model/root）
//   - 需要创建的 mock 文件及 mock 接口定义
//   - 需要创建的 helpers 文件
//   - 需要创建的测试文件列表
type PackagePlan struct {
	Path        string         `json:"path"`
	Package     string         `json:"package"`
	Layer       string         `json:"layer"`
	MockFile    string         `json:"mock_file,omitempty"`
	HelpersFile string         `json:"helpers_file,omitempty"`
	Mocks       []MockPlan     `json:"mocks,omitempty"`
	Files       []TestFilePlan `json:"files"`
}

// TestFilePlan 表示单个测试文件的规划，与源文件一一对应。
type TestFilePlan struct {
	TestPath   string         `json:"test_path"`
	SourcePath string         `json:"source_path"`
	Functions  []TestFuncPlan `json:"functions"`
}

// TestFuncPlan 表示单个测试函数的规划。
//
// 字段说明:
//   - TestName:  建议的测试函数名（如 TestOrderService_CancelOrder）
//   - Target:    被测的函数/方法完整名（如 OrderService.CancelOrder）
//   - Receiver:  方法的 receiver 类型名（函数时为空）
//   - Params:    参数类型列表
//   - Returns:   返回值类型列表
//   - Calls:     方法体内的关键调用链（repo/function/tx）
//   - Scenarios: 从调用链推导的确定性测试场景
type TestFuncPlan struct {
	TestName  string         `json:"test_name"`
	Target    string         `json:"target"`
	Receiver  string         `json:"receiver,omitempty"`
	Params    []string       `json:"params"`
	Returns   []string       `json:"returns"`
	Calls     []CallInfo     `json:"calls,omitempty"`
	Scenarios []TestScenario `json:"scenarios"`
}

// TestScenario 表示一个确定性测试场景。
//
// 由 AST 分析从调用链推导，每个可能失败的调用点对应一个 error 场景。
type TestScenario struct {
	Name           string   `json:"name"`                      // 场景名（如 "find_active_plan_error"）
	Type           string   `json:"type"`                      // success | error | not_found | idempotent
	FailingCall    string   `json:"failing_call,omitempty"`    // 失败的调用目标（如 "repo.FindActivePlan"）
	Sentinel       string   `json:"sentinel,omitempty"`        // 返回的哨兵错误（如 "ErrInternal"）
	ErrorHandling  string   `json:"error_handling,omitempty"`  // returned | logged_and_ignored
	NotCalledAfter []string `json:"not_called_after,omitempty"` // 失败后不应被调用的方法
}

// MockPlan 表示一个需要 mock 的接口及其方法列表。
type MockPlan struct {
	MockName  string       `json:"mock_name"`
	Interface string       `json:"interface"`
	Methods   []MockMethod `json:"methods"`
}

// MockMethod 表示 mock 接口中的一个方法签名。
type MockMethod struct {
	Name      string `json:"name"`
	Signature string `json:"signature"`
}

// ── 子命令入口 ──────────────────────────────────────────────────────────────

// runTestPlan 执行测试重规划: 删除旧测试 → 分析源码 → 输出 JSON 测试计划。
func runTestPlan(module string) {
	codeRoot := filepath.Join("internal", module)

	if _, err := os.Stat(codeRoot); err != nil {
		fmt.Fprintf(os.Stderr, "错误: 目录 %s 不存在\n", codeRoot)
		os.Exit(2)
	}

	// 1. 删除所有测试文件
	deleted := deleteTestFiles(codeRoot)

	// 2. 加载源码包（不含测试）
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

	// 构建 CheckPass 列表
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

	// 3. 生成测试计划
	plan := buildTestPlan(module, deleted, passes)

	// JSON → stdout
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(plan)

	// 摘要 → stderr
	var totalFiles, totalFuncs, totalMocks int
	for _, pkg := range plan.Packages {
		totalFiles += len(pkg.Files)
		totalMocks += len(pkg.Mocks)
		for _, f := range pkg.Files {
			totalFuncs += len(f.Functions)
		}
	}
	fmt.Fprintf(os.Stderr, "\n═══ %s 测试计划 ═══\n", module)
	fmt.Fprintf(os.Stderr, "删除旧测试文件: %d 个\n", len(deleted))
	fmt.Fprintf(os.Stderr, "计划测试文件:   %d 个\n", totalFiles)
	fmt.Fprintf(os.Stderr, "计划测试函数:   %d 个\n", totalFuncs)
	fmt.Fprintf(os.Stderr, "需要 mock:      %d 个\n", totalMocks)
}

// ── 删除测试文件 ────────────────────────────────────────────────────────────

// deleteTestFiles 遍历 codeRoot 目录树，删除所有 _test.go 文件，返回已删除的文件路径列表。
func deleteTestFiles(codeRoot string) []string {
	var deleted []string
	_ = filepath.Walk(codeRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			if removeErr := os.Remove(path); removeErr == nil {
				deleted = append(deleted, path)
				fmt.Fprintf(os.Stderr, "删除: %s\n", path)
			}
		}
		return nil
	})
	return deleted
}

// ── 测试计划构建 ────────────────────────────────────────────────────────────

// buildTestPlan 基于所有 CheckPass 构建完整的测试计划。
//
// 按包路径分组，为每个包生成:
//   - 测试文件规划（与源文件一一对应）
//   - Mock 接口规划（从 struct 字段和包内接口定义中收集）
//   - helpers_test.go 和 mock_test.go 的路径建议
func buildTestPlan(module string, deleted []string, passes []*CheckPass) TestPlan {
	plan := TestPlan{
		Module:  module,
		Deleted: deleted,
	}

	type pkgData struct {
		plan   PackagePlan
		passes []*CheckPass
	}
	pkgMap := make(map[string]*pkgData)
	var pkgOrder []string

	for _, pass := range passes {
		for i, file := range pass.Files {
			path := pass.FilePaths[i]
			pkgPath := filepath.Dir(path)

			pd, exists := pkgMap[pkgPath]
			if !exists {
				pd = &pkgData{
					plan: PackagePlan{
						Path:    pkgPath,
						Package: file.Name.Name,
						Layer:   detectLayer(path, file.Name.Name),
					},
				}
				pkgMap[pkgPath] = pd
				pkgOrder = append(pkgOrder, pkgPath)
			}

			fp := planSourceFile(file, path, pass)
			if len(fp.Functions) > 0 {
				pd.plan.Files = append(pd.plan.Files, fp)
			}
		}

		// 关联 pass 到对应的包（用于 mock 收集）
		if len(pass.FilePaths) > 0 {
			pkgPath := filepath.Dir(pass.FilePaths[0])
			if pd, ok := pkgMap[pkgPath]; ok {
				hasSame := false
				for _, p := range pd.passes {
					if p == pass {
						hasSame = true
						break
					}
				}
				if !hasSame {
					pd.passes = append(pd.passes, pass)
				}
			}
		}
	}

	// 为每个包收集 mock 依赖
	for _, pkgPath := range pkgOrder {
		pd := pkgMap[pkgPath]
		mockSeen := make(map[string]bool)
		for _, pass := range pd.passes {
			for _, m := range collectMockDeps(pass) {
				if !mockSeen[m.Interface] {
					pd.plan.Mocks = append(pd.plan.Mocks, m)
					mockSeen[m.Interface] = true
				}
			}
		}
		if len(pd.plan.Mocks) > 0 {
			pd.plan.MockFile = filepath.Join(pkgPath, "mock_test.go")
		}
		if len(pd.plan.Files) > 0 {
			pd.plan.HelpersFile = filepath.Join(pkgPath, "helpers_test.go")
		}
	}

	for _, pkgPath := range pkgOrder {
		pd := pkgMap[pkgPath]
		if len(pd.plan.Files) > 0 {
			plan.Packages = append(plan.Packages, pd.plan)
		}
	}
	return plan
}

// detectLayer 根据文件路径和包名推断代码层级。
func detectLayer(path, pkgName string) string {
	switch {
	case isHandlerPkg(path):
		return "handler"
	case isServicePkg(path):
		return "service"
	case isRepoPkg(path):
		return "repository"
	case pkgName == "model":
		return "model"
	default:
		return "root"
	}
}

// ── 源文件分析 ──────────────────────────────────────────────────────────────

// noTestFiles 列出不需要生成测试文件的源文件名。
//
// 这些文件为纯声明/配置文件，不包含可测试的业务逻辑。
var noTestFiles = map[string]bool{
	"const.go":    true,
	"errors.go":   true,
	"module.go":   true,
	"wire.go":     true,
	"wire_gen.go": true,
}

// planSourceFile 分析单个源文件，返回对应的测试文件计划。
//
// 跳过规则:
//   - 纯定义文件（const.go, errors.go, wire 相关）
//   - router / error_map 文件
//   - model 包中无方法的纯结构体文件
//
// 只为导出函数/方法生成测试计划。
func planSourceFile(file *ast.File, path string, pass *CheckPass) TestFilePlan {
	testPath := strings.TrimSuffix(path, ".go") + "_test.go"
	fp := TestFilePlan{TestPath: testPath, SourcePath: path}

	base := filepath.Base(path)
	if noTestFiles[base] {
		return fp
	}
	if strings.Contains(base, "router") || strings.Contains(base, "error_map") {
		return fp
	}
	// model 包中仅结构体定义的文件不需要测试
	if file.Name.Name == "model" && !hasMethodDecl(file) {
		return fp
	}

	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil || !fd.Name.IsExported() {
			continue
		}
		fp.Functions = append(fp.Functions, planTestFunc(fd, pass))
	}
	return fp
}

// hasMethodDecl 检查文件中是否包含有函数体的方法声明。
func hasMethodDecl(file *ast.File) bool {
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if ok && fd.Recv != nil && fd.Body != nil {
			return true
		}
	}
	return false
}

// ── 测试函数规划 ────────────────────────────────────────────────────────────

// planTestFunc 为单个导出函数/方法生成测试函数计划。
//
// 测试函数命名规则: Test{Receiver}_{FuncName}
// 场景从调用链 + 错误处理 AST 分析推导，不依赖 LLM 判断。
func planTestFunc(fd *ast.FuncDecl, pass *CheckPass) TestFuncPlan {
	receiver := extractReceiver(fd)
	testName := "Test"
	if receiver != "" {
		testName += receiver + "_"
	}
	testName += fd.Name.Name

	params := extractTypes(fd.Type.Params, pass)
	returns := extractTypes(fd.Type.Results, pass)
	calls := docExtractCalls(fd, pass)

	return TestFuncPlan{
		TestName:  testName,
		Target:    funcName(fd),
		Receiver:  receiver,
		Params:    params,
		Returns:   returns,
		Calls:     calls,
		Scenarios: generateScenarios(fd, calls, pass),
	}
}

// extractReceiver 提取方法的 receiver 类型名（去除指针星号）。
func extractReceiver(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return ""
	}
	switch t := fd.Recv.List[0].Type.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		if ident, ok := t.X.(*ast.Ident); ok {
			return ident.Name
		}
	}
	return ""
}

// extractTypes 从 FieldList 提取所有类型的字符串表示。
func extractTypes(fl *ast.FieldList, pass *CheckPass) []string {
	if fl == nil {
		return nil
	}
	var result []string
	for _, field := range fl.List {
		typStr := nodeStr(pass.Fset, field.Type)
		n := len(field.Names)
		if n == 0 {
			n = 1
		}
		for j := 0; j < n; j++ {
			result = append(result, typStr)
		}
	}
	return result
}

// ── 确定性场景生成 ──────────────────────────────────────────────────────────

// errorPathInfo 描述单个调用点的错误处理方式。
type errorPathInfo struct {
	CallTarget       string // 调用目标（如 "repo.FindActivePlan"）
	Handling         string // returned | logged_and_ignored | swallowed
	Sentinel         string // 哨兵错误名（如 "ErrInternal"）
	HasRecordNotFound bool   // 是否有 gorm.ErrRecordNotFound 分支
	NotFoundSentinel string // RecordNotFound 分支的哨兵错误
}

// generateScenarios 从调用链和错误处理 AST 分析生成确定性测试场景。
//
// 规则：
//  1. 永远生成一个 success 场景
//  2. 每个 returned 的 error 调用 → 一个 error 场景
//  3. 有 gorm.ErrRecordNotFound 分支 → 额外一个 not_found 场景
//  4. Idempotent 调用 → 一个 idempotent 场景
func generateScenarios(fd *ast.FuncDecl, calls []CallInfo, pass *CheckPass) []TestScenario {
	var scenarios []TestScenario

	// 规则 1：success 场景
	if returnsError(fd) {
		scenarios = append(scenarios, TestScenario{Name: "success", Type: "success"})
	} else {
		scenarios = append(scenarios, TestScenario{Name: "normal", Type: "success"})
	}

	// 收集所有调用目标名，用于 not_called_after
	var returnedTargets []string
	for _, c := range calls {
		if c.Kind != "tx" {
			returnedTargets = append(returnedTargets, c.Target)
		}
	}

	// 提取所有错误路径
	errorPaths := extractErrorPaths(fd, pass)
	pathMap := make(map[string]*errorPathInfo, len(errorPaths))
	for i := range errorPaths {
		pathMap[errorPaths[i].CallTarget] = &errorPaths[i]
	}

	targetIdx := 0
	for _, call := range calls {
		if call.Kind == "tx" {
			continue
		}
		currentIdx := targetIdx
		targetIdx++

		ep, ok := pathMap[call.Target]
		if !ok {
			continue
		}

		// 规则 3：RecordNotFound 分支
		if ep.HasRecordNotFound {
			scenarios = append(scenarios, TestScenario{
				Name:          callScenarioName(call.Target) + "_not_found",
				Type:          "not_found",
				FailingCall:   call.Target,
				Sentinel:      ep.NotFoundSentinel,
				ErrorHandling: "returned",
			})
		}

		// 规则 2：error 场景（仅 returned）
		if ep.Handling == "returned" {
			var notCalled []string
			if currentIdx+1 < len(returnedTargets) {
				notCalled = returnedTargets[currentIdx+1:]
			}
			scenarios = append(scenarios, TestScenario{
				Name:           callScenarioName(call.Target) + "_error",
				Type:           "error",
				FailingCall:    call.Target,
				Sentinel:       ep.Sentinel,
				ErrorHandling:  "returned",
				NotCalledAfter: notCalled,
			})
		}

		// 规则 4：Idempotent 场景
		if strings.Contains(call.Target, "Idempotent") && ep.Handling == "returned" {
			scenarios = append(scenarios, TestScenario{
				Name:        "idempotent_skip",
				Type:        "idempotent",
				FailingCall: call.Target,
			})
		}
	}

	return scenarios
}

// callScenarioName 从调用目标生成 snake_case 场景名。
//
// 例：
//
//	"repo.FindActivePlan"     → "find_active_plan"
//	"txRepo.FindSnapshotsForUpdate" → "find_snapshots_for_update"
//	"EnsureExpiredSetToFree"  → "ensure_expired_set_to_free"
func callScenarioName(target string) string {
	// 去掉 receiver 前缀
	if idx := strings.LastIndex(target, "."); idx >= 0 {
		target = target[idx+1:]
	}
	return camelToSnake(target)
}

// camelToSnake 将 CamelCase 转换为 snake_case，正确处理连续大写。
//
// 例：FindByID → find_by_id, ActivatePlanEntitlementsByPlanIDs → activate_plan_entitlements_by_plan_ids
func camelToSnake(s string) string {
	runes := []rune(s)
	n := len(runes)
	var buf strings.Builder
	for i := 0; i < n; i++ {
		r := runes[i]
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				prev := runes[i-1]
				// 小写后跟大写: findPlan → find_plan
				if prev >= 'a' && prev <= 'z' {
					buf.WriteByte('_')
				}
				// 连续大写 + 后面是小写时在倒数第二个大写前插入: HTMLParser → html_parser
				// 但不在连续大写末尾+s的情况下插入: IDs → ids (不是 i_ds)
				if prev >= 'A' && prev <= 'Z' && i+1 < n && runes[i+1] >= 'a' && runes[i+1] <= 'z' && runes[i+1] != 's' {
					buf.WriteByte('_')
				}
			}
			buf.WriteRune(r + 32) // toLower
		} else {
			buf.WriteRune(r)
		}
	}
	return buf.String()
}

// ── 错误路径 AST 分析 ──────────────────────────────────────────────────────

// extractErrorPaths 遍历函数体，对每个可能失败的调用点提取错误处理信息。
//
// 支持两种调用模式：
//  1. result, err := call(); if err != nil { ... }
//  2. if err := call(); err != nil { ... }
//
// 同时递归进入 WithTx 回调（FuncLit）分析事务内的调用。
func extractErrorPaths(fd *ast.FuncDecl, pass *CheckPass) []errorPathInfo {
	if fd.Body == nil {
		return nil
	}
	var paths []errorPathInfo
	walkStmtListForErrors(fd.Body.List, pass, &paths)
	return paths
}

// walkStmtListForErrors 顺序遍历语句列表，提取调用-错误处理对。
func walkStmtListForErrors(stmts []ast.Stmt, pass *CheckPass, paths *[]errorPathInfo) {
	for i := 0; i < len(stmts); i++ {
		stmt := stmts[i]

		// 模式 1: if err := call(); err != nil { ... }
		if ifStmt, ok := stmt.(*ast.IfStmt); ok && ifStmt.Init != nil {
			if target := extractCallTarget(ifStmt.Init, pass); target != "" {
				if isErrNilCheck(ifStmt.Cond) {
					ep := analyzeIfBlock(ifStmt, pass)
					ep.CallTarget = target
					*paths = append(*paths, ep)
				}
			}
			// 递归进入 if-init 中 CallExpr 参数里的 FuncLit（WithTx 回调模式）
			// 模式：if err := repo.WithTx(ctx, func(txRepo ...) error { ... }); err != nil
			if assign, ok := ifStmt.Init.(*ast.AssignStmt); ok {
				for _, rhs := range assign.Rhs {
					walkFuncLitsInExpr(rhs, pass, paths)
				}
			}
			// 递归进入 if 体（可能包含嵌套的调用模式）
			walkStmtListForErrors(ifStmt.Body.List, pass, paths)
			if ifStmt.Else != nil {
				if elseBlock, ok := ifStmt.Else.(*ast.BlockStmt); ok {
					walkStmtListForErrors(elseBlock.List, pass, paths)
				}
			}
			continue
		}

		// 模式 2: xxx, err := call() 然后下一条 if err != nil { ... }
		if assign, ok := stmt.(*ast.AssignStmt); ok {
			target := extractCallTarget(assign, pass)
			if target != "" && assignCapturesError(assign) {
				// 向后查找紧邻的 if err != nil
				if i+1 < len(stmts) {
					if nextIf, ok := stmts[i+1].(*ast.IfStmt); ok && isErrNilCheck(nextIf.Cond) {
						ep := analyzeIfBlock(nextIf, pass)
						ep.CallTarget = target
						*paths = append(*paths, ep)
						i++ // 跳过已处理的 if
						continue
					}
				}
				// 没有 if err 检查 → logged_and_ignored 或 swallowed
				*paths = append(*paths, errorPathInfo{
					CallTarget: target,
					Handling:   "logged_and_ignored",
				})
				continue
			}

			// 模式 4: result := chain(); if result.Error != nil { ... }
			// GORM 模式：赋值不捕获 err，但后续 if 检查 result.Error
			if target != "" && len(assign.Lhs) == 1 {
				if ident, ok := assign.Lhs[0].(*ast.Ident); ok && ident.Name != "_" {
					if i+1 < len(stmts) {
						if nextIf, ok := stmts[i+1].(*ast.IfStmt); ok && isFieldErrCheck(nextIf.Cond, ident.Name) {
							ep := analyzeIfBlock(nextIf, pass)
							ep.CallTarget = target
							*paths = append(*paths, ep)
							i++
							continue
						}
					}
				}
			}
		}

		// 模式 3: return call() — 直接返回函数调用结果（error 透传）
		// 同时递归进入 return 中的 FuncLit（WithTx 回调）
		if retStmt, ok := stmt.(*ast.ReturnStmt); ok {
			for _, result := range retStmt.Results {
				if call, ok := result.(*ast.CallExpr); ok {
					if target := classifyCallTarget(call); target != "" {
						*paths = append(*paths, errorPathInfo{
							CallTarget: target,
							Handling:   "returned",
						})
					}
				}
				walkFuncLitsInExpr(result, pass, paths)
			}
		}

		// 递归进入其他块语句
		switch s := stmt.(type) {
		case *ast.IfStmt:
			walkStmtListForErrors(s.Body.List, pass, paths)
			if s.Else != nil {
				if elseBlock, ok := s.Else.(*ast.BlockStmt); ok {
					walkStmtListForErrors(elseBlock.List, pass, paths)
				}
			}
		case *ast.ForStmt:
			if s.Body != nil {
				walkStmtListForErrors(s.Body.List, pass, paths)
			}
		case *ast.RangeStmt:
			if s.Body != nil {
				walkStmtListForErrors(s.Body.List, pass, paths)
			}
		}
	}
}

// walkFuncLitsInExpr 递归查找表达式中的 FuncLit 并遍历其语句列表。
//
// 用于处理 WithTx 回调等模式，无论 FuncLit 出现在 return 语句、if-init 还是普通赋值中。
func walkFuncLitsInExpr(expr ast.Expr, pass *CheckPass, paths *[]errorPathInfo) {
	ast.Inspect(expr, func(n ast.Node) bool {
		if fl, ok := n.(*ast.FuncLit); ok && fl.Body != nil {
			walkStmtListForErrors(fl.Body.List, pass, paths)
			return false
		}
		return true
	})
}

// extractCallTarget 从赋值语句中提取 repo/function 调用目标名。
//
// 返回空字符串表示该赋值不包含感兴趣的调用。
func extractCallTarget(stmt ast.Stmt, pass *CheckPass) string {
	assign, ok := stmt.(*ast.AssignStmt)
	if !ok || len(assign.Rhs) == 0 {
		return ""
	}
	for _, rhs := range assign.Rhs {
		// 标准模式: xxx, err := call()
		if call, ok := rhs.(*ast.CallExpr); ok {
			if target := classifyCallTarget(call); target != "" {
				return target
			}
		}
		// GORM 模式: err := chain.Error（字段访问而非函数返回）
		if sel, ok := rhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "Error" {
			if call, ok := sel.X.(*ast.CallExpr); ok {
				if target := classifyCallTarget(call); target != "" {
					return target
				}
			}
		}
	}
	return ""
}

// classifyCallTarget 判断 CallExpr 是否为 repo/service/function 调用，返回目标名。
func classifyCallTarget(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		methodName := fn.Sel.Name
		// a.b.Method() — 链式调用（如 s.repo.FindActivePlan）
		if inner, ok := fn.X.(*ast.SelectorExpr); ok {
			receiver := inner.Sel.Name
			kind := docClassifyCall(receiver, methodName)
			if kind != "" {
				return receiver + "." + methodName
			}
		}
		// a.Method() — 直接方法调用（如 txRepo.FindPlan、s.gateway.Cancel）
		if ident, ok := fn.X.(*ast.Ident); ok {
			kind := docClassifyCall(ident.Name, methodName)
			if kind != "" {
				return ident.Name + "." + methodName
			}
			// gateway 调用也需要追踪
			if ident.Name == "gateway" || strings.HasSuffix(ident.Name, "gateway") {
				return ident.Name + "." + methodName
			}
		}
		// GORM 链式调用兜底: tx.Model().Where().Updates() / s.db.WithContext().Create()
		if root, ok := resolveChainReceiver(fn.X); ok {
			kind := docClassifyCall(root, methodName)
			if kind != "" {
				return root + "." + methodName
			}
		}
	case *ast.Ident:
		// 包级函数调用（如 ApplyEntitlements、EnsureExpiredSetToFree）
		if fn.IsExported() && fn.Name != "Error" && fn.Name != "New" {
			return fn.Name
		}
	}
	return ""
}

// assignCapturesError 检查赋值语句是否捕获了 error 变量。
func assignCapturesError(assign *ast.AssignStmt) bool {
	for _, lhs := range assign.Lhs {
		if ident, ok := lhs.(*ast.Ident); ok {
			if ident.Name == "err" || strings.HasSuffix(ident.Name, "Err") {
				return true
			}
		}
	}
	return false
}

// isErrNilCheck 判断条件表达式是否为 err != nil 检查。
func isErrNilCheck(cond ast.Expr) bool {
	bin, ok := cond.(*ast.BinaryExpr)
	if !ok {
		return false
	}
	if bin.Op.String() != "!=" {
		return false
	}
	xIdent, xOk := bin.X.(*ast.Ident)
	yIdent, yOk := bin.Y.(*ast.Ident)
	if xOk && xIdent.Name == "err" && yOk && yIdent.Name == "nil" {
		return true
	}
	if yOk && yIdent.Name == "err" && xOk && xIdent.Name == "nil" {
		return true
	}
	return false
}

// isFieldErrCheck 判断条件表达式是否为 varName.Error != nil 检查（GORM result.Error 模式）。
func isFieldErrCheck(cond ast.Expr, varName string) bool {
	bin, ok := cond.(*ast.BinaryExpr)
	if !ok || bin.Op.String() != "!=" {
		return false
	}
	checkSel := func(expr ast.Expr) bool {
		sel, ok := expr.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Error" {
			return false
		}
		ident, ok := sel.X.(*ast.Ident)
		return ok && ident.Name == varName
	}
	checkNil := func(expr ast.Expr) bool {
		ident, ok := expr.(*ast.Ident)
		return ok && ident.Name == "nil"
	}
	return (checkSel(bin.X) && checkNil(bin.Y)) || (checkSel(bin.Y) && checkNil(bin.X))
}

// analyzeIfBlock 分析 if err != nil { ... } 块，提取错误处理方式和哨兵错误。
func analyzeIfBlock(ifStmt *ast.IfStmt, pass *CheckPass) errorPathInfo {
	ep := errorPathInfo{}

	// 检查是否有 return 语句
	hasReturn := false
	hasOnlyLog := true
	var returnSentinel string
	var hasRecordNotFound bool
	var notFoundSentinel string

	ast.Inspect(ifStmt.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.ReturnStmt:
			hasReturn = true
			hasOnlyLog = false
			if s := extractSentinelFromReturn(node); s != "" && returnSentinel == "" {
				returnSentinel = s
			}
		case *ast.IfStmt:
			// 内部 if — 检测 errors.Is(err, gorm.ErrRecordNotFound)
			if isRecordNotFoundCheck(node.Cond) {
				hasRecordNotFound = true
				// 从内部 if 的 return 提取 not_found sentinel
				ast.Inspect(node.Body, func(inner ast.Node) bool {
					if ret, ok := inner.(*ast.ReturnStmt); ok {
						if s := extractSentinelFromReturn(ret); s != "" {
							notFoundSentinel = s
						}
						return false
					}
					return true
				})
				// 外层 if 的 returnSentinel 应从内部 if 之后的 return 提取
				return true
			}
		}
		return true
	})

	if !hasReturn && hasOnlyLog {
		ep.Handling = "logged_and_ignored"
	} else if hasReturn {
		ep.Handling = "returned"
	} else {
		ep.Handling = "logged_and_ignored"
	}

	// 若有 RecordNotFound 分支，外层 sentinel 取非 not_found 的 return
	if hasRecordNotFound {
		// 重新提取：跳过 RecordNotFound 内部的 return，取外层的
		outerSentinel := extractOuterSentinel(ifStmt.Body, pass)
		if outerSentinel != "" {
			returnSentinel = outerSentinel
		}
	}

	ep.Sentinel = returnSentinel
	ep.HasRecordNotFound = hasRecordNotFound
	ep.NotFoundSentinel = notFoundSentinel

	return ep
}

// extractSentinelFromReturn 从 return 语句提取哨兵错误名。
//
// 支持的模式：
//  1. return ..., fmt.Errorf("...: %w: %w", err, pkg.ErrXxx) → "ErrXxx"
//  2. return ..., pkg.ErrXxx → "ErrXxx"
//  3. return ..., err → "" (透传)
func extractSentinelFromReturn(ret *ast.ReturnStmt) string {
	if len(ret.Results) == 0 {
		return ""
	}
	last := ret.Results[len(ret.Results)-1]

	// 模式 1: fmt.Errorf(...)
	if call, ok := last.(*ast.CallExpr); ok {
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if sel.Sel.Name == "Errorf" && len(call.Args) > 0 {
				// 取最后一个参数
				lastArg := call.Args[len(call.Args)-1]
				return extractErrName(lastArg)
			}
		}
	}

	// 模式 2: pkg.ErrXxx
	return extractErrName(last)
}

// extractErrName 从表达式中提取 Err* 名称。
//
// 支持 pkg.ErrXxx（SelectorExpr）和 ErrXxx（Ident）。
func extractErrName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		if strings.HasPrefix(e.Sel.Name, "Err") {
			return e.Sel.Name
		}
	case *ast.Ident:
		if strings.HasPrefix(e.Name, "Err") {
			return e.Name
		}
	}
	return ""
}

// isRecordNotFoundCheck 检查条件是否为 errors.Is(err, gorm.ErrRecordNotFound)。
func isRecordNotFoundCheck(cond ast.Expr) bool {
	call, ok := cond.(*ast.CallExpr)
	if !ok || len(call.Args) != 2 {
		return false
	}
	// 检查函数名是 errors.Is
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Is" {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok || ident.Name != "errors" {
		return false
	}
	// 检查第二个参数包含 "RecordNotFound" 或 "ErrRecordNotFound"
	switch arg := call.Args[1].(type) {
	case *ast.SelectorExpr:
		return strings.Contains(arg.Sel.Name, "RecordNotFound")
	case *ast.Ident:
		return strings.Contains(arg.Name, "RecordNotFound")
	}
	return false
}

// extractOuterSentinel 从 if 块中提取非 RecordNotFound 分支的 return sentinel。
//
// 跳过嵌套的 if errors.Is(err, gorm.ErrRecordNotFound) 内部的 return，
// 只取外层直接的 return 语句中的 sentinel。
func extractOuterSentinel(body *ast.BlockStmt, pass *CheckPass) string {
	for _, stmt := range body.List {
		// 跳过内部 if（RecordNotFound 分支）
		if _, ok := stmt.(*ast.IfStmt); ok {
			continue
		}
		if ret, ok := stmt.(*ast.ReturnStmt); ok {
			if s := extractSentinelFromReturn(ret); s != "" {
				return s
			}
		}
	}
	return ""
}

// ── Mock 依赖收集 ──────────────────────────────────────────────────────────

// collectMockDeps 分析包中所有 struct 的字段类型和包内接口定义，收集需要 mock 的接口。
//
// 两种来源:
//  1. struct 字段为 interface 类型 → 该 struct 的测试需要此 interface 的 mock
//  2. 包内定义的 interface → 可能作为依赖注入契约，也需要 mock
func collectMockDeps(pass *CheckPass) []MockPlan {
	var mocks []MockPlan
	seen := make(map[string]bool)

	for _, file := range pass.Files {
		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range genDecl.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}

				// 来源 1: struct 字段中的 interface 依赖
				if st, ok := ts.Type.(*ast.StructType); ok && st.Fields != nil {
					for _, field := range st.Fields.List {
						for _, name := range field.Names {
							obj := pass.TypesInfo.ObjectOf(name)
							if obj == nil {
								continue
							}
							if m := interfaceToMock(obj.Type(), seen); m != nil {
								mocks = append(mocks, *m)
							}
						}
					}
				}

				// 来源 2: 包内定义的 interface
				if _, ok := ts.Type.(*ast.InterfaceType); ok {
					obj := pass.TypesInfo.ObjectOf(ts.Name)
					if obj != nil {
						if m := interfaceToMock(obj.Type(), seen); m != nil {
							mocks = append(mocks, *m)
						}
					}
				}
			}
		}
	}
	return mocks
}

// interfaceToMock 将 types.Type 转换为 MockPlan（如果是非空 interface）。
//
// 支持指针解引用，通过 seen 去重。返回 nil 表示该类型不是可 mock 的 interface。
func interfaceToMock(t types.Type, seen map[string]bool) *MockPlan {
	// 解引用指针
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	iface, ok := t.Underlying().(*types.Interface)
	if !ok || iface.NumMethods() == 0 {
		return nil
	}
	named, ok := t.(*types.Named)
	if !ok {
		return nil
	}
	ifaceName := named.Obj().Name()
	if seen[ifaceName] {
		return nil
	}
	seen[ifaceName] = true

	mock := &MockPlan{
		MockName:  "mock" + ifaceName,
		Interface: ifaceName,
	}
	for i := 0; i < iface.NumMethods(); i++ {
		m := iface.Method(i)
		mock.Methods = append(mock.Methods, MockMethod{
			Name:      m.Name(),
			Signature: m.Type().String(),
		})
	}
	return mock
}
