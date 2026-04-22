// timeaudit.go — sayso-lint 时区一致性审计子命令。
//
// 用法:
//
//	sayso-lint time-audit <module-name>
//
// 扫描模块中所有非测试 Go 文件，提取 time.Now() / time.Since / time.Date 等时间操作点，
// 分类统计 UTC 使用一致性，输出 JSON 到 stdout。
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"
)

// ── 输出数据结构 ────────────────────────────────────────────────────────────

// TimeAuditReport 时区审计的完整输出。
type TimeAuditReport struct {
	Module  string         `json:"module"`
	Stats   TimeAuditStats `json:"stats"`
	Entries []TimeEntry    `json:"entries"`
}

// TimeAuditStats 统计概览。
type TimeAuditStats struct {
	TimeNowUTC      int `json:"time_now_utc"`       // time.Now().UTC()
	TimeNowBare     int `json:"time_now_bare"`      // 裸 time.Now()
	TimeNowIn       int `json:"time_now_in"`        // time.Now().In(...)
	TimeSinceUntil  int `json:"time_since_until"`   // time.Since / time.Until
	TimeCompare     int `json:"time_compare"`       // .Before / .After / .Equal / .Sub
	TimeCalc        int `json:"time_calc"`          // .Add / .AddDate
	TimeDate        int `json:"time_date"`          // time.Date(...)
	TimeParse       int `json:"time_parse"`         // time.Parse / time.ParseInLocation
	TimeLocation    int `json:"time_location"`      // time.LoadLocation / time.FixedZone
	UnixTimestamp   int `json:"unix_timestamp"`     // .Unix / .UnixMilli / .UnixNano
	TotalTimePoints int `json:"total_time_points"`  // 所有时间操作点总数
}

// TimeEntry 一处时间操作的详细记录。
type TimeEntry struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Function string `json:"function"`
	Kind     string `json:"kind"`     // time_now_utc / time_now_bare / time_now_in / time_since / time_until / time_compare / time_calc / time_date / time_parse / time_location / unix_timestamp
	Expr     string `json:"expr"`     // 匹配到的表达式片段
	Context  string `json:"context"`  // 所在行的完整内容（去除前后空白）
}

// ── 入口 ────────────────────────────────────────────────────────────────────

func runTimeAudit(module string) {
	codeRoot := filepath.Join("internal", module)

	if _, err := os.Stat(codeRoot); err != nil {
		fmt.Fprintf(os.Stderr, "错误: 目录 %s 不存在\n", codeRoot)
		os.Exit(2)
	}

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

	// 读取所有源文件内容用于提取上下文行
	fileContents := make(map[string][]string) // path → lines

	report := TimeAuditReport{Module: module}

	for _, pass := range passes {
		for fi, file := range pass.Files {
			path := pass.FilePaths[fi]

			// 懒加载文件行内容
			if _, ok := fileContents[path]; !ok {
				data, err := os.ReadFile(path)
				if err == nil {
					fileContents[path] = strings.Split(string(data), "\n")
				}
			}

			ast.Inspect(file, func(n ast.Node) bool {
				fn := enclosingFuncName(file, n, pass.Fset)
				switch node := n.(type) {
				case *ast.CallExpr:
					report.extractCallExpr(node, path, fn, pass, file, fileContents)
				case *ast.SelectorExpr:
					// 捕获 .Before / .After / .Equal / .Sub / .Add / .AddDate（非调用形式的选择器不处理）
				}
				return true
			})
		}
	}

	// 计算统计
	for _, e := range report.Entries {
		switch e.Kind {
		case "time_now_utc":
			report.Stats.TimeNowUTC++
		case "time_now_bare":
			report.Stats.TimeNowBare++
		case "time_now_in":
			report.Stats.TimeNowIn++
		case "time_since":
			report.Stats.TimeSinceUntil++
		case "time_until":
			report.Stats.TimeSinceUntil++
		case "time_compare":
			report.Stats.TimeCompare++
		case "time_calc":
			report.Stats.TimeCalc++
		case "time_date":
			report.Stats.TimeDate++
		case "time_parse", "time_parse_in_location":
			report.Stats.TimeParse++
		case "time_load_location", "time_fixed_zone":
			report.Stats.TimeLocation++
		case "unix_timestamp":
			report.Stats.UnixTimestamp++
		}
	}
	report.Stats.TotalTimePoints = len(report.Entries)

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(report)

	// 摘要 → stderr
	fmt.Fprintf(os.Stderr, "\n═══ %s 时区一致性审计 ═══\n", module)
	fmt.Fprintf(os.Stderr, "✅ time.Now().UTC():            %d\n", report.Stats.TimeNowUTC)
	fmt.Fprintf(os.Stderr, "❌ 裸 time.Now():               %d\n", report.Stats.TimeNowBare)
	fmt.Fprintf(os.Stderr, "⚠️  time.Now().In(...):          %d\n", report.Stats.TimeNowIn)
	fmt.Fprintf(os.Stderr, "   time.Since/Until:            %d\n", report.Stats.TimeSinceUntil)
	fmt.Fprintf(os.Stderr, "   时间比较(Before/After/Sub):  %d\n", report.Stats.TimeCompare)
	fmt.Fprintf(os.Stderr, "   时间计算(Add/AddDate):       %d\n", report.Stats.TimeCalc)
	fmt.Fprintf(os.Stderr, "   time.Date:                   %d\n", report.Stats.TimeDate)
	fmt.Fprintf(os.Stderr, "   time.Parse*:                 %d\n", report.Stats.TimeParse)
	fmt.Fprintf(os.Stderr, "   time.LoadLocation/FixedZone: %d\n", report.Stats.TimeLocation)
	fmt.Fprintf(os.Stderr, "   Unix 时间戳:                 %d\n", report.Stats.UnixTimestamp)
	fmt.Fprintf(os.Stderr, "   总计:                        %d\n", report.Stats.TotalTimePoints)
}

// ── 提取逻辑 ────────────────────────────────────────────────────────────────

// extractCallExpr 分析一个 CallExpr 节点，提取时间操作。
func (r *TimeAuditReport) extractCallExpr(call *ast.CallExpr, path, fn string, pass *CheckPass, file *ast.File, fileContents map[string][]string) {
	switch {
	// ── time.Now() 系列 ──────────────────────────────────────────────────
	case isTimeNowCall(call):
		line := lineOf(pass.Fset, call.Pos())
		ctx := getContextLine(fileContents, path, line)
		if isWrappedByUTC(findEnclosingBlock(file, call, pass.Fset), call, pass.Fset) {
			r.Entries = append(r.Entries, TimeEntry{
				File: path, Line: line, Function: fn,
				Kind: "time_now_utc", Expr: "time.Now().UTC()", Context: ctx,
			})
		} else if isWrappedByIn(file, call, pass.Fset) {
			r.Entries = append(r.Entries, TimeEntry{
				File: path, Line: line, Function: fn,
				Kind: "time_now_in", Expr: "time.Now().In(...)", Context: ctx,
			})
		} else {
			r.Entries = append(r.Entries, TimeEntry{
				File: path, Line: line, Function: fn,
				Kind: "time_now_bare", Expr: "time.Now()", Context: ctx,
			})
		}

	// ── time.Since / time.Until ──────────────────────────────────────────
	case isTimePkgCall(call, "Since"):
		line := lineOf(pass.Fset, call.Pos())
		r.Entries = append(r.Entries, TimeEntry{
			File: path, Line: line, Function: fn,
			Kind: "time_since", Expr: "time.Since(...)", Context: getContextLine(fileContents, path, line),
		})
	case isTimePkgCall(call, "Until"):
		line := lineOf(pass.Fset, call.Pos())
		r.Entries = append(r.Entries, TimeEntry{
			File: path, Line: line, Function: fn,
			Kind: "time_until", Expr: "time.Until(...)", Context: getContextLine(fileContents, path, line),
		})

	// ── time.Date ────────────────────────────────────────────────────────
	case isTimePkgCall(call, "Date"):
		line := lineOf(pass.Fset, call.Pos())
		r.Entries = append(r.Entries, TimeEntry{
			File: path, Line: line, Function: fn,
			Kind: "time_date", Expr: "time.Date(...)", Context: getContextLine(fileContents, path, line),
		})

	// ── time.Parse / time.ParseInLocation ────────────────────────────────
	case isTimePkgCall(call, "Parse"):
		line := lineOf(pass.Fset, call.Pos())
		r.Entries = append(r.Entries, TimeEntry{
			File: path, Line: line, Function: fn,
			Kind: "time_parse", Expr: "time.Parse(...)", Context: getContextLine(fileContents, path, line),
		})
	case isTimePkgCall(call, "ParseInLocation"):
		line := lineOf(pass.Fset, call.Pos())
		r.Entries = append(r.Entries, TimeEntry{
			File: path, Line: line, Function: fn,
			Kind: "time_parse_in_location", Expr: "time.ParseInLocation(...)", Context: getContextLine(fileContents, path, line),
		})

	// ── time.LoadLocation / time.FixedZone ──────────────────────────────
	case isTimePkgCall(call, "LoadLocation"):
		line := lineOf(pass.Fset, call.Pos())
		r.Entries = append(r.Entries, TimeEntry{
			File: path, Line: line, Function: fn,
			Kind: "time_load_location", Expr: "time.LoadLocation(...)", Context: getContextLine(fileContents, path, line),
		})
	case isTimePkgCall(call, "FixedZone"):
		line := lineOf(pass.Fset, call.Pos())
		r.Entries = append(r.Entries, TimeEntry{
			File: path, Line: line, Function: fn,
			Kind: "time_fixed_zone", Expr: "time.FixedZone(...)", Context: getContextLine(fileContents, path, line),
		})

	// ── 方法调用: .Before / .After / .Equal / .Sub / .Add / .AddDate / .Unix* / .Format ──
	default:
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		name := sel.Sel.Name
		line := lineOf(pass.Fset, call.Pos())
		ctx := getContextLine(fileContents, path, line)
		switch name {
		case "Before", "After", "Equal", "Sub":
			if isTimeTypedExpr(sel.X, pass) {
				r.Entries = append(r.Entries, TimeEntry{
					File: path, Line: line, Function: fn,
					Kind: "time_compare", Expr: "." + name + "(...)", Context: ctx,
				})
			}
		case "Add", "AddDate":
			if isTimeTypedExpr(sel.X, pass) {
				r.Entries = append(r.Entries, TimeEntry{
					File: path, Line: line, Function: fn,
					Kind: "time_calc", Expr: "." + name + "(...)", Context: ctx,
				})
			}
		case "Unix", "UnixMilli", "UnixNano":
			if isTimeTypedExpr(sel.X, pass) {
				r.Entries = append(r.Entries, TimeEntry{
					File: path, Line: line, Function: fn,
					Kind: "unix_timestamp", Expr: "." + name + "()", Context: ctx,
				})
			}
		}
	}
}

// ── 辅助函数 ────────────────────────────────────────────────────────────────

// isTimeNowCall 判断 call 是否为 time.Now()。
func isTimeNowCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Now" {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == "time"
}

// isTimePkgCall 判断 call 是否为 time.<funcName>(...)。
func isTimePkgCall(call *ast.CallExpr, funcName string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != funcName {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == "time"
}

// isTimeTypedExpr 通过类型信息判断 expr 的类型是否为 time.Time 或 *time.Time。
func isTimeTypedExpr(expr ast.Expr, pass *CheckPass) bool {
	if pass.TypesInfo == nil {
		return false
	}
	t := pass.TypesInfo.TypeOf(expr)
	if t == nil {
		return false
	}
	s := t.String()
	return s == "time.Time" || s == "*time.Time"
}

// isWrappedByIn 检查 time.Now() 是否被 .In(...) 方法调用包裹。
func isWrappedByIn(file *ast.File, target *ast.CallExpr, fset *token.FileSet) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "In" {
			return true
		}
		if innerCall, ok := sel.X.(*ast.CallExpr); ok && innerCall == target {
			found = true
			return false
		}
		return true
	})
	return found
}

// findEnclosingBlock 找到包含 target 节点的最近 BlockStmt（函数体）。
func findEnclosingBlock(file *ast.File, target ast.Node, fset *token.FileSet) *ast.BlockStmt {
	var block *ast.BlockStmt
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if fd, ok := n.(*ast.FuncDecl); ok && fd.Body != nil {
			if fd.Body.Pos() <= target.Pos() && target.End() <= fd.Body.End() {
				block = fd.Body
			}
		}
		if fl, ok := n.(*ast.FuncLit); ok && fl.Body != nil {
			if fl.Body.Pos() <= target.Pos() && target.End() <= fl.Body.End() {
				block = fl.Body
			}
		}
		return true
	})
	return block
}

// enclosingFuncName 返回包含 node 的最内层函数名。
func enclosingFuncName(file *ast.File, target ast.Node, fset *token.FileSet) string {
	if target == nil {
		return ""
	}
	name := "(top-level)"
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if fd, ok := n.(*ast.FuncDecl); ok && fd.Body != nil {
			if fd.Body.Pos() <= target.Pos() && target.End() <= fd.Body.End() {
				name = funcName(fd)
			}
		}
		return true
	})
	return name
}

// getContextLine 获取指定文件指定行的内容（去除前后空白）。
func getContextLine(fileContents map[string][]string, path string, line int) string {
	lines, ok := fileContents[path]
	if !ok || line < 1 || line > len(lines) {
		return ""
	}
	return strings.TrimSpace(lines[line-1])
}
