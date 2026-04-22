package main

import (
	"fmt"
	"go/ast"
	"go/types"
	"os"
	"path/filepath"
	"strings"
)

// checkUnusedExports 检测模块中导出但在整个项目中无调用者的符号。
//
// 规则：每个导出的函数/方法应在项目中至少有一个调用点（定义处除外）。
// 未使用的导出符号是死代码，应删除以降低维护成本。
//
// 检测方式（项目级搜索）：
//  1. 收集模块中所有导出的函数和方法
//  2. 读取 internal/ + cmd/ + pkg/ 下所有非测试 .go 文件的内容
//  3. 对每个符号，用 word-boundary 匹配搜索：
//     - 定义文件内出现 > 1 次 → 有同文件使用（如 handler method 在 RegisterRoutes 中引用）
//     - 其他文件中出现 → 有跨文件使用
//  4. 两者都无 → 标记为未使用
//
// 接口实现豁免（类型感知）：
//   - 通过 buildIfaceMethodMap 收集模块及其直接依赖中所有 interface 的方法
//   - 如果导出方法的接收器类型实现了某个 interface，且该方法属于该 interface → 跳过
//   - 避免因接口间接调用导致的误报
//
// Word-boundary 匹配规则：
//   - 符号前后字符不是字母/数字/下划线时视为完整单词
//   - 例如 "GetMyAnalytics" 匹配 "h.GetMyAnalytics" 但不匹配 "GetMyAnalyticsV2"
//
// 豁免：测试文件、符号名 ≤ 2 字符（避免误匹配）。
//
// 修复方式：确认无使用后删除。
func checkUnusedExports(passes []*CheckPass, codeRoot string) []Finding {
	type export struct {
		name     string
		file     string
		line     int
		kind     string
		recvType types.Type // 方法的接收器类型（func 为 nil）
	}
	var exports []export

	for _, pass := range passes {
		eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
			if !decl.Name.IsExported() {
				return
			}
			kind := "func"
			var recvType types.Type
			if decl.Recv != nil {
				kind = "method"
				// 通过类型系统获取接收器类型（比 AST 更可靠）
				if obj := pass.TypesInfo.Defs[decl.Name]; obj != nil {
					if fn, ok := obj.(*types.Func); ok {
						if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
							recvType = sig.Recv().Type()
						}
					}
				}
			}
			exports = append(exports, export{
				name:     decl.Name.Name,
				file:     path,
				line:     lineOf(pass.Fset, decl.Pos()),
				kind:     kind,
				recvType: recvType,
			})
		})
	}

	// 构建接口方法映射，用于跳过接口实现方法的误报
	ifaceMap := buildIfaceMethodMap(passes)
	projectFiles := readAllGoFiles()

	var findings []Finding
	for _, exp := range exports {
		// 跳过接口实现方法：如果接收器类型实现了某个包含该方法的 interface，
		// 说明该方法可能通过 interface 间接调用，不应标记为未使用
		if exp.kind == "method" && exp.recvType != nil {
			if ifaces, ok := ifaceMap[exp.name]; ok {
				baseType := exp.recvType
				if ptr, ok := exp.recvType.(*types.Pointer); ok {
					baseType = ptr.Elem()
				}
				isImpl := false
				for _, iface := range ifaces {
					if types.Implements(exp.recvType, iface) || types.Implements(types.NewPointer(baseType), iface) {
						isImpl = true
						break
					}
				}
				if isImpl {
					continue
				}
			}
		}

		used := false
		for _, pf := range projectFiles {
			if pf.path == exp.file {
				if countWord(pf.content, exp.name) > 1 {
					used = true
					break
				}
				continue
			}
			if containsWord(pf.content, exp.name) {
				used = true
				break
			}
		}
		if !used {
			findings = append(findings, Finding{
				Category: "unused",
				Rule:     "unused-export",
				Severity: "warning",
				File:     exp.file,
				Line:     exp.line,
				Message:  exp.kind + " " + exp.name + " 无调用者",
			})
		}
	}
	return findings
}

// ── 接口实现检测 ──────────────────────────────────────────────────────────────

// ifaceMethodIndex 将方法名映射到包含该方法的 interface 列表。
//
// 用于快速判断某个导出方法是否可能是某个 interface 的实现：
// 先按方法名查找候选 interface，再用 types.Implements 精确验证。
type ifaceMethodIndex map[string][]*types.Interface

// buildIfaceMethodMap 收集当前模块及其直接依赖中所有 interface 的方法。
//
// 遍历范围：
//   - 当前模块自身定义的 interface（pass.Pkg.Scope()）
//   - 直接依赖包的导出 interface（pass.Pkg.Imports() 一层）
//
// 不递归遍历间接依赖（性能考虑），但覆盖了最常见的场景：
// 实现标准库 interface（io.Reader 等）和项目内 interface。
//
// 返回值：方法名 → 包含该方法的 interface 列表。
func buildIfaceMethodMap(passes []*CheckPass) ifaceMethodIndex {
	m := make(ifaceMethodIndex)
	seen := make(map[*types.Package]bool)

	collectPkg := func(pkg *types.Package) {
		if pkg == nil || seen[pkg] {
			return
		}
		seen[pkg] = true
		scope := pkg.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			tn, ok := obj.(*types.TypeName)
			if !ok {
				continue
			}
			iface, ok := tn.Type().Underlying().(*types.Interface)
			if !ok {
				continue
			}
			for i := 0; i < iface.NumMethods(); i++ {
				mName := iface.Method(i).Name()
				m[mName] = append(m[mName], iface)
			}
		}
	}

	for _, pass := range passes {
		collectPkg(pass.Pkg)
		for _, imp := range pass.Pkg.Imports() {
			collectPkg(imp)
		}
	}
	return m
}

// ── 辅助：项目文件读取和 word-boundary 搜索 ────────────────────────────────

// projFile 表示一个项目源文件的路径和完整内容。
//
// 用于 checkUnusedExports 和 checkCircularDep 的项目级文件搜索，
// 将文件一次性读入内存以加速多次搜索。
type projFile struct {
	path    string
	content string
}

var (
	projectFilesLoaded bool
	projectFilesCache  []projFile
)

// readAllGoFiles 读取 internal/ + cmd/ + pkg/ 三个顶层目录下的所有非测试 .go 文件。
//
// 结果在进程生命周期内缓存，多次调用只执行一次实际 I/O。
// 被 checkUnusedExports（符号引用搜索）和 checkCircularDep（import 路径搜索）共享。
func readAllGoFiles() []projFile {
	if projectFilesLoaded {
		return projectFilesCache
	}
	projectFilesLoaded = true

	for _, dir := range []string{"internal", "cmd", "pkg"} {
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				fmt.Fprintf(os.Stderr, "警告: 读取文件失败 %s: %v\n", path, readErr)
				return nil
			}
			projectFilesCache = append(projectFilesCache, projFile{path, string(data)})
			return nil
		})
	}
	return projectFilesCache
}

// containsWord 检查 content 中是否包含 word 作为完整标识符（至少出现一次）。
//
// Word-boundary 匹配算法：
//  1. 用 strings.Index 找到 word 的首次出现位置
//  2. 检查匹配位置的前一个字符和后一个字符是否为标识符字符（isIdent）
//  3. 前后都不是标识符字符 → 完整单词匹配（如 "h.GetUser" 中的 "GetUser"）
//  4. 前或后是标识符字符 → 部分匹配（如 "GetUserV2" 中搜索 "GetUser"）→ 跳过
//  5. 未找到完整匹配 → 返回 false
//
// 示例：containsWord("h.GetMyAnalytics()", "GetMyAnalytics") → true
//
//	containsWord("GetMyAnalyticsV2", "GetMyAnalytics") → false
func containsWord(content, word string) bool {
	idx := 0
	for idx <= len(content)-len(word) {
		i := strings.Index(content[idx:], word)
		if i == -1 {
			return false
		}
		pos := idx + i
		if (pos == 0 || !isIdent(content[pos-1])) && (pos+len(word) >= len(content) || !isIdent(content[pos+len(word)])) {
			return true
		}
		idx = pos + 1
	}
	return false
}

// countWord 统计 word 在 content 中作为完整标识符出现的次数。
//
// 算法与 containsWord 相同（word-boundary 匹配），但不是短路返回，
// 而是遍历所有匹配位置并计数。用于判断符号在定义文件中是否有除定义外的其他引用：
//   - countWord(定义文件内容, 符号名) > 1 → 同文件内有引用（如 RegisterRoutes 中引用 handler 方法）
//   - countWord(...) == 1 → 只有定义本身，无同文件引用
func countWord(content, word string) int {
	count := 0
	idx := 0
	for idx <= len(content)-len(word) {
		i := strings.Index(content[idx:], word)
		if i == -1 {
			break
		}
		pos := idx + i
		if (pos == 0 || !isIdent(content[pos-1])) && (pos+len(word) >= len(content) || !isIdent(content[pos+len(word)])) {
			count++
		}
		idx = pos + 1
	}
	return count
}

// isIdent 判断字节是否为 Go 标识符的合法组成字符。
//
// 合法字符：a-z、A-Z、0-9、_（下划线）。
// 用于 containsWord/countWord 的 word-boundary 判断：
// 匹配位置的前后字符如果是 isIdent，说明是更长标识符的一部分，不算完整匹配。
func isIdent(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}
