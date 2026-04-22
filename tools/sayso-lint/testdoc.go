// testdoc.go — sayso-lint 测试文档数据提取子命令。
//
// 用法:
//
//	sayso-lint test-doc <module-name>
//
// 功能:
//  1. 统计测试文件信息（文件数、函数数、子测试数、行数）
//  2. 提取完整场景矩阵（已覆盖 + 缺失）
//  3. 分析 sentinel 覆盖度
//  4. 分析调用链覆盖度
//  5. 提取 mock 清单
//
// 输出格式: TestDocData JSON → stdout，摘要 → stderr。
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ── 输出数据结构 ────────────────────────────────────────────────────────────

// TestDocData 测试文档生成所需的完整数据。
type TestDocData struct {
	Module    string            `json:"module"`
	Stats     TestDocStats      `json:"stats"`
	Files     []TestDocFile     `json:"files"`
	Scenarios []TestDocScenario `json:"scenarios"`
	Sentinels []SentinelCov     `json:"sentinel_coverage"`
	CallChain []CallChainCov    `json:"call_chain_coverage"`
	Mocks     []MockInfo        `json:"mocks"`
}

// TestDocStats 测试统计概览。
type TestDocStats struct {
	FileCount     int `json:"file_count"`
	TestFunctions int `json:"test_functions"`
	SubTests      int `json:"sub_tests"`
	TotalLines    int `json:"total_lines"`
	TotalScenarios   int `json:"total_scenarios"`
	CoveredScenarios int `json:"covered_scenarios"`
	MissingScenarios int `json:"missing_scenarios"`
}

// TestDocFile 单个测试文件的统计信息。
type TestDocFile struct {
	Path          string   `json:"path"`
	Layer         string   `json:"layer"`
	TestFunctions int      `json:"test_functions"`
	SubTests      int      `json:"sub_tests"`
	Lines         int      `json:"lines"`
	Targets       []string `json:"targets"`
}

// TestDocScenario 测试场景矩阵中的一条记录。
type TestDocScenario struct {
	Layer    string `json:"layer"`
	Target   string `json:"target"`
	File     string `json:"file"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Sentinel string `json:"sentinel,omitempty"`
	Covered  bool   `json:"covered"`
}

// SentinelCov sentinel 覆盖分析。
type SentinelCov struct {
	Name     string `json:"name"`
	Covered  bool   `json:"covered"`
	TestFile string `json:"test_file,omitempty"`
}

// CallChainCov 调用链覆盖分析。
type CallChainCov struct {
	Function    string `json:"function"`
	Call        string `json:"call"`
	ErrorTested bool   `json:"error_tested"`
	TestName    string `json:"test_name,omitempty"`
}

// MockInfo mock 清单。
type MockInfo struct {
	Name      string `json:"name"`
	Interface string `json:"interface"`
	Methods   int    `json:"methods"`
	File      string `json:"file"`
}

// ── 子命令入口 ──────────────────────────────────────────────────────────────

func runTestDoc(module string) {
	codeRoot := filepath.Join("internal", module)

	if _, err := os.Stat(codeRoot); err != nil {
		fmt.Fprintf(os.Stderr, "错误: 目录 %s 不存在\n", codeRoot)
		os.Exit(2)
	}

	// 1. 统计测试文件信息
	files := collectTestFileStats(codeRoot)

	// 2. 加载源码生成场景 spec
	srcPasses := loadPasses(codeRoot, false)
	plan := buildTestPlan(module, nil, srcPasses)

	// 3. 解析已有测试
	testInfo := loadExistingTests(codeRoot)

	// 4. 构建完整场景矩阵（覆盖 + 缺失）
	scenarios, stats := buildScenarioMatrix(plan, testInfo)

	// 5. 分析 sentinel 覆盖度
	sentinelCov := buildSentinelCoverage(codeRoot, testInfo)

	// 6. 分析调用链覆盖度
	callCov := buildCallChainCoverage(plan, testInfo)

	// 7. 提取 mock 清单
	mocks := extractMockInfo(plan, codeRoot)

	// 合并文件统计
	stats.FileCount = len(files)
	for _, f := range files {
		stats.TestFunctions += f.TestFunctions
		stats.SubTests += f.SubTests
		stats.TotalLines += f.Lines
	}

	data := TestDocData{
		Module:    module,
		Stats:     stats,
		Files:     files,
		Scenarios: scenarios,
		Sentinels: sentinelCov,
		CallChain: callCov,
		Mocks:     mocks,
	}

	// JSON → stdout
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(data)

	// 摘要 → stderr
	fmt.Fprintf(os.Stderr, "\n═══ %s 测试文档数据提取 ═══\n", module)
	fmt.Fprintf(os.Stderr, "测试文件:   %d\n", stats.FileCount)
	fmt.Fprintf(os.Stderr, "Test 函数:  %d\n", stats.TestFunctions)
	fmt.Fprintf(os.Stderr, "子测试:     %d\n", stats.SubTests)
	fmt.Fprintf(os.Stderr, "总行数:     %d\n", stats.TotalLines)
	fmt.Fprintf(os.Stderr, "场景总数:   %d（已覆盖 %d，缺失 %d）\n",
		stats.TotalScenarios, stats.CoveredScenarios, stats.MissingScenarios)
	if stats.TotalScenarios > 0 {
		pct := float64(stats.CoveredScenarios) / float64(stats.TotalScenarios) * 100
		fmt.Fprintf(os.Stderr, "覆盖率:     %.0f%%\n", pct)
	}
	coveredSentinels := 0
	for _, s := range sentinelCov {
		if s.Covered {
			coveredSentinels++
		}
	}
	fmt.Fprintf(os.Stderr, "Sentinel:   %d/%d\n", coveredSentinels, len(sentinelCov))
}

// ── 测试文件统计 ──────────────────────────────────────────────────────────────

// collectTestFileStats 收集所有测试文件的统计信息。
func collectTestFileStats(codeRoot string) []TestDocFile {
	var files []TestDocFile

	_ = filepath.Walk(codeRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}

		lines := countLines(path)
		layer := detectLayerByPath(path)

		testFuncs := 0
		subTests := 0
		var targets []string

		// 用字符串扫描统计
		testFuncs, subTests, targets = scanTestFile(path)

		files = append(files, TestDocFile{
			Path:          path,
			Layer:         layer,
			TestFunctions: testFuncs,
			SubTests:      subTests,
			Lines:         lines,
			Targets:       targets,
		})

		return nil
	})

	return files
}

// scanTestFile 扫描测试文件提取统计信息。
func scanTestFile(path string) (testFuncs int, subTests int, targets []string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	targetSet := make(map[string]bool)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "func Test") && strings.Contains(trimmed, "(t *testing.T)") {
			testFuncs++
			// 从 TestApplyEntitlements 提取 ApplyEntitlements
			name := trimmed[len("func "):]
			if idx := strings.Index(name, "("); idx > 0 {
				name = name[:idx]
			}
			name = strings.TrimPrefix(name, "Test")
			if idx := strings.Index(name, "_"); idx > 0 {
				name = name[:idx]
			}
			if name != "" && !targetSet[name] {
				targetSet[name] = true
				targets = append(targets, name)
			}
		}

		if strings.Contains(trimmed, "t.Run(") || strings.Contains(trimmed, "t.Run (") {
			subTests++
		}
	}
	return
}

// countLines 统计文件行数。
func countLines(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	count := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		count++
	}
	return count
}

// detectLayer 从文件路径推断层级。
func detectLayerByPath(path string) string {
	if strings.Contains(path, "/handler/") {
		return "handler"
	}
	if strings.Contains(path, "/service/") {
		return "service"
	}
	if strings.Contains(path, "/repository/") {
		return "repository"
	}
	if strings.Contains(path, "/model/") {
		return "model"
	}
	return "root"
}

// ── 场景矩阵构建 ──────────────────────────────────────────────────────────────

// buildScenarioMatrix 构建完整场景矩阵，包含已覆盖和缺失的场景。
func buildScenarioMatrix(plan TestPlan, testInfo map[string]*existingTest) ([]TestDocScenario, TestDocStats) {
	var scenarios []TestDocScenario
	var stats TestDocStats

	for _, pkg := range plan.Packages {
		et := testInfo[pkg.Path]
		allRuns := make(map[string]bool)
		if et != nil {
			for _, runs := range et.TestFuncs {
				for _, r := range runs {
					allRuns[r] = true
				}
			}
		}

		for _, file := range pkg.Files {
			for _, fn := range file.Functions {
				for _, scenario := range fn.Scenarios {
					stats.TotalScenarios++
					covered := isScenarioCovered(scenario, allRuns)
					if covered {
						stats.CoveredScenarios++
					} else {
						stats.MissingScenarios++
					}

					scenarios = append(scenarios, TestDocScenario{
						Layer:    pkg.Layer,
						Target:   fn.Target,
						File:     filepath.Base(file.TestPath),
						Name:     scenario.Name,
						Type:     scenario.Type,
						Sentinel: scenario.Sentinel,
						Covered:  covered,
					})
				}
			}
		}
	}

	return scenarios, stats
}

// ── Sentinel 覆盖分析 ─────────────────────────────────────────────────────────

// buildSentinelCoverage 分析 sentinel 覆盖度。
// 从模块的 errors.go/const.go 中提取所有 sentinel，与测试中的 errors.Is 断言交叉对比。
func buildSentinelCoverage(codeRoot string, testInfo map[string]*existingTest) []SentinelCov {
	// 收集模块定义的所有 sentinel
	allSentinels := collectSentinels(codeRoot)

	// 收集测试中断言的 sentinel 及对应文件
	testedSentinels := make(map[string]string) // sentinel → test file
	for pkgPath, et := range testInfo {
		for s := range et.Sentinels {
			if _, exists := testedSentinels[s]; !exists {
				// 取包路径的最后一部分作为简化路径
				testedSentinels[s] = filepath.Base(pkgPath)
			}
		}
	}

	var result []SentinelCov
	for _, s := range allSentinels {
		cov := SentinelCov{Name: s}
		if testFile, ok := testedSentinels[s]; ok {
			cov.Covered = true
			cov.TestFile = testFile
		}
		result = append(result, cov)
	}

	return result
}

// collectSentinels 从模块代码中提取所有 Err 开头的 sentinel 变量。
func collectSentinels(codeRoot string) []string {
	var sentinels []string
	seen := make(map[string]bool)

	_ = filepath.Walk(codeRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		f, readErr := os.Open(path)
		if readErr != nil {
			return nil
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			// 匹配 ErrXxx = errors.New(...) 或 var ErrXxx = ...
			if strings.Contains(line, "errors.New") || strings.Contains(line, "fmt.Errorf") {
				for _, prefix := range []string{"Err", "err"} {
					if idx := strings.Index(line, prefix); idx >= 0 {
						rest := line[idx:]
						end := strings.IndexAny(rest, " \t=")
						if end > 0 {
							name := rest[:end]
							if strings.HasPrefix(name, "Err") && !seen[name] {
								seen[name] = true
								sentinels = append(sentinels, name)
							}
						}
					}
				}
			}
		}
		return nil
	})

	return sentinels
}

// ── 调用链覆盖分析 ─────────────────────────────────────────────────────────────

// buildCallChainCoverage 分析调用链中每个 repo 调用点的 error 路径是否被测试覆盖。
func buildCallChainCoverage(plan TestPlan, testInfo map[string]*existingTest) []CallChainCov {
	var result []CallChainCov

	for _, pkg := range plan.Packages {
		et := testInfo[pkg.Path]
		allRuns := make(map[string]bool)
		if et != nil {
			for _, runs := range et.TestFuncs {
				for _, r := range runs {
					allRuns[r] = true
				}
			}
		}

		for _, file := range pkg.Files {
			for _, fn := range file.Functions {
				for _, call := range fn.Calls {
					if call.Kind != "repo" && call.Kind != "function" {
						continue
					}

					cov := CallChainCov{
						Function: fn.Target,
						Call:     call.Target,
					}

					// 查找对应的 error 场景是否被覆盖
					for _, scenario := range fn.Scenarios {
						if scenario.FailingCall == call.Target && scenario.Type == "error" {
							if isScenarioCovered(scenario, allRuns) {
								cov.ErrorTested = true
								cov.TestName = scenario.Name
							}
							break
						}
					}

					result = append(result, cov)
				}
			}
		}
	}

	return result
}

// ── Mock 清单提取 ─────────────────────────────────────────────────────────────

// extractMockInfo 从测试计划中提取 mock 清单。
func extractMockInfo(plan TestPlan, codeRoot string) []MockInfo {
	var mocks []MockInfo

	for _, pkg := range plan.Packages {
		for _, m := range pkg.Mocks {
			mocks = append(mocks, MockInfo{
				Name:      m.MockName,
				Interface: m.Interface,
				Methods:   len(m.Methods),
				File:      filepath.Join(pkg.Path, "mock_test.go"),
			})
		}
	}

	return mocks
}
