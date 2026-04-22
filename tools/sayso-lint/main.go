// sayso-lint 基于 go/packages 类型系统的全量代码审计工具。
//
// 用法:
//
//	go run ./tools/sayso-lint [flags] <module-name>
//	go run ./tools/sayso-lint order
//	go run ./tools/sayso-lint --severity=error order
//
// 输出 JSON 报告到 stdout，人类摘要到 stderr。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"
)

func main() {
	severity := flag.String("severity", "", "最低严重级别过滤 (error, warning, info)")
	testsOnly := flag.Bool("tests-only", false, "只加载测试文件并运行测试相关检查")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "用法: sayso-lint [flags] <module-name>\n      sayso-lint doc <module-name>\n      sayso-lint test-doc <module-name>\n      sayso-lint retest <module-name>\n      sayso-lint test <module-name>\n      sayso-lint time-audit <module-name>\n\nFlags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		flag.Usage()
		os.Exit(2)
	}

	// 子命令: doc — 提取模块结构化文档
	if args[0] == "doc" {
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "用法: sayso-lint doc <module-name>\n")
			os.Exit(2)
		}
		runDocExtract(args[1])
		return
	}

	// 子命令: retest — 删除旧测试并输出测试计划
	if args[0] == "retest" {
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "用法: sayso-lint retest <module-name>\n")
			os.Exit(2)
		}
		runTestPlan(args[1])
		return
	}

	// 子命令: test-doc — 提取测试文档数据
	if args[0] == "test-doc" {
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "用法: sayso-lint test-doc <module-name>\n")
			os.Exit(2)
		}
		runTestDoc(args[1])
		return
	}

	// 子命令: time-audit — 时区一致性审计
	if args[0] == "time-audit" {
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "用法: sayso-lint time-audit <module-name>\n")
			os.Exit(2)
		}
		runTimeAudit(args[1])
		return
	}

	// 子命令: test — 测试覆盖度缺口分析（不删除文件）
	if args[0] == "test" {
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "用法: sayso-lint test <module-name>\n")
			os.Exit(2)
		}
		runTestCoverage(args[1])
		return
	}

	module := args[0]

	if *severity != "" {
		if _, ok := severityLevel[*severity]; !ok {
			fmt.Fprintf(os.Stderr, "错误: 无效的 severity 级别 %q（可选: error, warning, info）\n", *severity)
			os.Exit(2)
		}
	}

	codeRoot := filepath.Join("internal", module)

	if _, err := os.Stat(codeRoot); err != nil {
		fmt.Fprintf(os.Stderr, "错误: 目录 %s 不存在\n", codeRoot)
		os.Exit(2)
	}

	// 加载包：完整 AST + 类型信息
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:   ".",
		Tests: *testsOnly,
	}

	pkgs, err := packages.Load(cfg, "./"+codeRoot+"/...")
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 加载包失败: %v\n", err)
		os.Exit(1)
	}

	// 构建 CheckPass 列表
	cwd, _ := os.Getwd()
	var passes []*CheckPass
	seen := make(map[string]bool) // 去重

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

			// 跳过模块外的文件和已处理的文件
			if !strings.HasPrefix(relPath, codeRoot) {
				continue
			}
			if seen[relPath] {
				continue
			}

			// --tests-only 时只保留测试文件，否则只保留非测试文件
			isTest := strings.HasSuffix(relPath, "_test.go")
			if *testsOnly && !isTest {
				continue
			}
			if !*testsOnly && isTest {
				continue
			}

			// 跳过生成文件
			if isGenerated(file) {
				continue
			}

			// 跳过 cmd/ 目录下的测试工具程序（非生产代码）
			if strings.Contains(relPath, "/cmd/") {
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

	// 运行检查
	var allFindings []Finding
	if *testsOnly {
		// 测试模式：只跑测试相关检查
		for _, pass := range passes {
			allFindings = append(allFindings, checkTestComment(pass)...)
		}
	} else {
		// 默认模式：跑所有生产代码检查
		for _, pass := range passes {
			for _, check := range allChecks {
				allFindings = append(allFindings, check(pass)...)
			}
		}
		allFindings = append(allFindings, runCrossPackageChecks(passes, codeRoot)...)
	}

	// 应用 //sayso-lint:ignore 抑制
	ignoreMap := buildIgnoreMap(passes)
	allFindings, suppressed := filterIgnored(allFindings, ignoreMap)

	// 按 severity 级别过滤
	if *severity != "" {
		allFindings = filterBySeverity(allFindings, *severity)
	}


	// 生成报告
	report := Report{
		Module:   module,
		Total:    len(allFindings),
		Summary:  make(map[string]int),
		Findings: allFindings,
	}
	for _, f := range allFindings {
		report.Summary[f.Category]++
	}

	// JSON → stdout
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(report)

	// 摘要 → stderr
	fmt.Fprintf(os.Stderr, "\n═══ %s 审计完成 ═══\n", module)
	for _, cat := range categoryOrder {
		count := report.Summary[cat.key]
		status := "✅"
		if count > 0 {
			status = "❌"
		}
		fmt.Fprintf(os.Stderr, "%s %s: %d\n", status, cat.label, count)
	}
	fmt.Fprintf(os.Stderr, "\n总计: %d 个问题\n", report.Total)
	if suppressed > 0 {
		fmt.Fprintf(os.Stderr, "（%d 条被 //sayso-lint:ignore 抑制）\n", suppressed)
	}

	if report.Total > 0 {
		os.Exit(1)
	}
}

var categoryOrder = []struct {
	key   string
	label string
}{
	{"error-handling", "错误处理"},
	{"logging", "日志"},
	{"gorm-safety", "数据库安全"},
	{"concurrency", "并发与资源"},
	{"gin-safety", "Gin 框架安全"},
	{"security", "安全"},
	{"annotation", "注释"},
	{"structure", "目录与命名"},
	{"unused", "未使用代码"},
}

// isGenerated 判断文件是否为代码生成器产出的文件。
//
// 遍历文件中所有注释，如果任一注释包含 "Code generated" 字符串则认为是生成文件。
// 遵循 Go 官方约定（go generate 工具在文件头部插入此标记）。
// 生成文件跳过所有 lint 检查，因为修改会在下次生成时被覆盖。
func isGenerated(file *ast.File) bool {
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			if strings.Contains(c.Text, "Code generated") {
				return true
			}
		}
	}
	return false
}
