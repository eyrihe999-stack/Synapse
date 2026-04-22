// testcoverage.go — sayso-lint 测试覆盖度缺口分析子命令。
//
// 用法:
//
//	sayso-lint test <module-name>
//
// 功能:
//  1. 基于源码 AST 生成确定性测试场景 spec（同 retest 但不删除文件）
//  2. 解析现有 _test.go：提取 t.Run 名称、mock error 设置、errors.Is 断言
//  3. 交叉对比输出覆盖度缺口 JSON
//
// 输出格式: TestCoverage JSON → stdout，摘要 → stderr。
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"
)

// ── 输出数据结构 ────────────────────────────────────────────────────────────

// TestCoverage 测试覆盖度分析的完整输出。
type TestCoverage struct {
	Module   string          `json:"module"`
	Packages []PackageGap    `json:"packages"`
	Summary  CoverageSummary `json:"summary"`
}

// CoverageSummary 覆盖度汇总统计。
type CoverageSummary struct {
	TotalScenarios   int `json:"total_scenarios"`
	CoveredScenarios int `json:"covered_scenarios"`
	MissingScenarios int `json:"missing_scenarios"`
}

// PackageGap 单个包的覆盖度分析。
type PackageGap struct {
	Path  string        `json:"path"`
	Layer string        `json:"layer"`
	Files []FileGap     `json:"files"`
	Mocks []MockPlan    `json:"mocks,omitempty"`
}

// FileGap 单个文件的覆盖度分析。
type FileGap struct {
	SourcePath string        `json:"source_path"`
	TestPath   string        `json:"test_path"`
	Functions  []FunctionGap `json:"functions"`
}

// FunctionGap 单个函数的覆盖度分析。
type FunctionGap struct {
	TestName  string         `json:"test_name"`
	Target    string         `json:"target"`
	Calls     []CallInfo     `json:"calls,omitempty"`
	Gaps      []TestScenario `json:"gaps"`      // 缺失的场景
	Covered   []string       `json:"covered"`   // 已覆盖的场景名
}

// ── 已有测试分析结构 ──────────────────────────────────────────────────────

// existingTest 已有测试文件的分析结果。
type existingTest struct {
	TestFuncs map[string][]string   // TestFuncName → []t.Run names
	MockErrors map[string]bool      // mock target names that return error
	Sentinels  map[string]bool      // asserted sentinels (errors.Is)
}

// ── 子命令入口 ──────────────────────────────────────────────────────────────

func runTestCoverage(module string) {
	codeRoot := filepath.Join("internal", module)

	if _, err := os.Stat(codeRoot); err != nil {
		fmt.Fprintf(os.Stderr, "错误: 目录 %s 不存在\n", codeRoot)
		os.Exit(2)
	}

	// 1. 加载源码包（不含测试）生成场景 spec
	srcPasses := loadPasses(codeRoot, false)
	plan := buildTestPlan(module, nil, srcPasses)

	// 2. 加载测试包提取已有测试信息
	testInfo := loadExistingTests(codeRoot)

	// 3. 交叉对比生成覆盖度缺口
	coverage := buildCoverage(module, plan, testInfo)

	// JSON → stdout
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(coverage)

	// 摘要 → stderr
	fmt.Fprintf(os.Stderr, "\n═══ %s 测试覆盖度分析 ═══\n", module)
	fmt.Fprintf(os.Stderr, "总场景数:   %d\n", coverage.Summary.TotalScenarios)
	fmt.Fprintf(os.Stderr, "已覆盖:     %d\n", coverage.Summary.CoveredScenarios)
	fmt.Fprintf(os.Stderr, "缺失:       %d\n", coverage.Summary.MissingScenarios)
	if coverage.Summary.TotalScenarios > 0 {
		pct := float64(coverage.Summary.CoveredScenarios) / float64(coverage.Summary.TotalScenarios) * 100
		fmt.Fprintf(os.Stderr, "覆盖率:     %.0f%%\n", pct)
	}
}

// ── 包加载 ──────────────────────────────────────────────────────────────────

// loadPasses 加载指定目录的 Go 包并构建 CheckPass 列表。
func loadPasses(codeRoot string, includeTests bool) []*CheckPass {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:   ".",
		Tests: includeTests,
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
			Module:    filepath.Base(codeRoot),
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
			if !includeTests && strings.HasSuffix(relPath, "_test.go") {
				continue
			}
			if isGenerated(file) || strings.Contains(relPath, "/cmd/") {
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
	return passes
}

// ── 已有测试解析 ────────────────────────────────────────────────────────────

// loadExistingTests 解析模块下所有 _test.go 文件，提取测试函数和子测试信息。
func loadExistingTests(codeRoot string) map[string]*existingTest {
	result := make(map[string]*existingTest) // package path → test info
	fset := token.NewFileSet()

	_ = filepath.Walk(codeRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}

		pkgPath := filepath.Dir(path)
		if result[pkgPath] == nil {
			result[pkgPath] = &existingTest{
				TestFuncs:  make(map[string][]string),
				MockErrors: make(map[string]bool),
				Sentinels:  make(map[string]bool),
			}
		}

		file, parseErr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if parseErr != nil {
			return nil
		}

		extractTestInfo(file, result[pkgPath])
		return nil
	})

	return result
}

// extractTestInfo 从测试文件 AST 提取测试信息。
//
// 支持两种测试模式：
//  1. t.Run("name", ...) 子测试 → 提取 run 名称
//  2. TestFuncName_Scenario 独立测试函数 → 从函数名提取场景
func extractTestInfo(file *ast.File, et *existingTest) {
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}

		if !strings.HasPrefix(fd.Name.Name, "Test") {
			continue
		}

		// 从函数名提取场景标识（TestApplyEntitlements_EmptyMappings → "empty_mappings"）
		funcName := fd.Name.Name
		var runs []string

		// 提取 t.Run 子测试名
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Run" || len(call.Args) < 2 {
				return true
			}
			if lit, ok := call.Args[0].(*ast.BasicLit); ok {
				name := strings.Trim(lit.Value, "\"")
				// 只取 " — " 或 " - " 之前的部分
				if idx := strings.Index(name, " — "); idx >= 0 {
					name = name[:idx]
				}
				if idx := strings.Index(name, " - "); idx >= 0 {
					name = name[:idx]
				}
				runs = append(runs, name)
			}
			return true
		})

		// 如果没有 t.Run，从函数名本身提取场景
		if len(runs) == 0 {
			// TestApplyEntitlements_EmptyMappings → "empty_mappings"
			// TestApplyEntitlements_PlanNotFound → "plan_not_found"
			parts := strings.SplitN(funcName[4:], "_", 2) // 去掉 "Test" 前缀
			if len(parts) == 2 {
				runs = append(runs, camelToSnake(parts[1]))
			} else {
				// TestApplyEntitlements（无后缀）→ 视为 success
				runs = append(runs, "success")
			}
		}

		et.TestFuncs[funcName] = runs

		// 提取 errors.Is 断言中的 sentinel 名
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Is" {
				return true
			}
			if s, ok := call.Args[1].(*ast.SelectorExpr); ok {
				if strings.HasPrefix(s.Sel.Name, "Err") {
					et.Sentinels[s.Sel.Name] = true
				}
			}
			return true
		})
	}
}

// ── 覆盖度分析 ──────────────────────────────────────────────────────────────

// buildCoverage 将场景 spec 与已有测试交叉对比，输出覆盖度缺口。
func buildCoverage(module string, plan TestPlan, testInfo map[string]*existingTest) TestCoverage {
	cov := TestCoverage{Module: module}

	for _, pkg := range plan.Packages {
		et := testInfo[pkg.Path]
		pkgGap := PackageGap{
			Path:  pkg.Path,
			Layer: pkg.Layer,
			Mocks: pkg.Mocks,
		}

		// 收集该包下所有已有 t.Run 名称（扁平化）
		allRuns := make(map[string]bool)
		if et != nil {
			for _, runs := range et.TestFuncs {
				for _, r := range runs {
					allRuns[r] = true
				}
			}
		}

		for _, file := range pkg.Files {
			fileGap := FileGap{
				SourcePath: file.SourcePath,
				TestPath:   file.TestPath,
			}

			for _, fn := range file.Functions {
				funcGap := FunctionGap{
					TestName: fn.TestName,
					Target:   fn.Target,
					Calls:    fn.Calls,
				}

				for _, scenario := range fn.Scenarios {
					cov.Summary.TotalScenarios++

					// 匹配策略：子测试名包含场景名的关键部分
					if isScenarioCovered(scenario, allRuns) {
						cov.Summary.CoveredScenarios++
						funcGap.Covered = append(funcGap.Covered, scenario.Name)
					} else {
						cov.Summary.MissingScenarios++
						funcGap.Gaps = append(funcGap.Gaps, scenario)
					}
				}

				// 只输出有缺口的函数
				if len(funcGap.Gaps) > 0 {
					fileGap.Functions = append(fileGap.Functions, funcGap)
				}
			}

			if len(fileGap.Functions) > 0 {
				pkgGap.Files = append(pkgGap.Files, fileGap)
			}
		}

		if len(pkgGap.Files) > 0 {
			cov.Packages = append(cov.Packages, pkgGap)
		}
	}

	return cov
}

// isScenarioCovered 检查场景是否被某个已有 t.Run 覆盖。
//
// 匹配规则（宽松）：
//   - success 场景：有 t.Run 名包含 "success" 或该 Test 函数有任何 t.Run
//   - error 场景：有 t.Run 名包含 failing_call 的关键词（去掉 repo/txRepo 前缀）
//   - not_found 场景：有 t.Run 名包含 "not_found" 或 "not found"
//   - idempotent 场景：有 t.Run 名包含 "idempotent" 或 "skip"
func isScenarioCovered(scenario TestScenario, allRuns map[string]bool) bool {
	// 提取 failing_call 的方法名关键词
	callKeyword := ""
	if scenario.FailingCall != "" {
		callKeyword = scenario.FailingCall
		if idx := strings.LastIndex(callKeyword, "."); idx >= 0 {
			callKeyword = callKeyword[idx+1:]
		}
		callKeyword = strings.ToLower(camelToSnake(callKeyword))
	}

	for run := range allRuns {
		runLower := strings.ToLower(run)
		runSnake := strings.ReplaceAll(runLower, " ", "_")

		switch scenario.Type {
		case "success":
			if strings.Contains(runLower, "success") || strings.Contains(runLower, "normal") {
				return true
			}
		case "error":
			if callKeyword != "" && strings.Contains(runSnake, callKeyword) &&
				(strings.Contains(runSnake, "error") || strings.Contains(runSnake, "fail")) {
				return true
			}
		case "not_found":
			if callKeyword != "" && strings.Contains(runSnake, callKeyword) &&
				(strings.Contains(runSnake, "not_found") || strings.Contains(runSnake, "not found")) {
				return true
			}
		case "idempotent":
			if strings.Contains(runSnake, "idempotent") || strings.Contains(runSnake, "skip") {
				return true
			}
		}
	}
	return false
}
