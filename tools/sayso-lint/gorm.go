package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"strings"
)

// gormReadMethods 列举 GORM 的只读查询方法。
//
// 被 checkGormTOCTOU 使用，与 gormWriteMethods 配合判断函数内是否同时存在
// 读和写操作（TOCTOU 竞态风险）。
var gormReadMethods = map[string]bool{
	"First": true, "Find": true, "Count": true, "Pluck": true,
	"Scan": true, "Take": true, "Last": true,
}

// gormWriteMethods 列举 GORM 的写操作方法。
//
// 被 checkGormTOCTOU 使用。注意 Save 虽然也是写方法（在此表中），
// 但 checkGormSave 会单独禁止其使用。
var gormWriteMethods = map[string]bool{
	"Create": true, "Save": true, "Update": true, "Updates": true, "Delete": true,
}

// checkGormSave 检测 *gorm.DB.Save() 调用。
//
// 规则：GORM 的 Save() 在记录不存在时会自动 INSERT（而非报错），容易造成意外数据写入。
// 应使用 Create()（明确插入）或 Updates()（明确更新）替代。
//
// 类型感知（go/packages 能力）：
//   - 通过 types.Info.Selections 确认 .Save() 的接收器是 *gorm.DB
//   - 如果是 os.File.Save() 或其他类型的 Save()，不标记
//
// 豁免：测试文件。
//
// 修复方式：新增用 Create()，更新用 Updates()。
func checkGormSave(pass *CheckPass) []Finding {
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
			if !ok || sel.Sel.Name != "Save" {
				return true
			}
			if !isGormMethod(pass.TypesInfo, sel) {
				return true
			}
			findings = append(findings, Finding{
				Category: "gorm-safety",
				Rule:     "gorm-save",
				Severity: "error",
				File:     path,
				Line:     lineOf(pass.Fset, call.Pos()),
				Message:  funcName(decl) + ": 使用 gorm.Save()，应改为 Create()/Updates()",
			})
			return true
		})
	})
	return findings
}

// checkGormNoWhere 检测 *gorm.DB 的 Update/Delete 操作没有 WHERE 条件。
//
// 规则：没有 WHERE 条件的 Update/Delete 会影响全表数据，是高风险操作。
// GORM 默认会阻止这种操作（gorm.ErrMissingWhereClause），但如果使用了
// AllowGlobalUpdate 或 Unscoped 则不会阻止。
//
// 检测方式：
//   1. 找到所有 .Update()/.Updates()/.Delete() 调用
//   2. 通过 types.Info 确认是 *gorm.DB 的方法
//   3. 检查链式调用中是否有 .Where()
//   4. 对于分步查询 (query := db.Where(...); query.Delete(...))，
//      检查函数体中是否有 receiver变量.Where 模式
//
// 豁免：测试文件。
//
// 修复方式：补充 WHERE 条件。
func checkGormNoWhere(pass *CheckPass) []Finding {
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
			if !ok {
				return true
			}
			method := sel.Sel.Name
			if method != "Update" && method != "Updates" && method != "Delete" {
				return true
			}
			if !isGormMethod(pass.TypesInfo, sel) {
				return true
			}

			chainStr := nodeStr(pass.Fset, call)
			if strings.Contains(chainStr, "Where") {
				return true
			}
			if ident, ok := sel.X.(*ast.Ident); ok {
				if strings.Contains(bodyStr, ident.Name+".Where") {
					return true
				}
			}

			findings = append(findings, Finding{
				Category: "gorm-safety",
				Rule:     "gorm-no-where",
				Severity: "error",
				File:     path,
				Line:     lineOf(pass.Fset, call.Pos()),
				Message:  funcName(decl) + ": " + method + "() 无 WHERE 条件",
			})
			return true
		})
	})
	return findings
}

// checkGormNotFound 检测 service 层使用 First/Take/Last 后未区分 ErrRecordNotFound。
//
// 规则：GORM 的 First/Take/Last 在记录不存在时返回 gorm.ErrRecordNotFound。
// 如果 service 层不区分这个错误，调用方无法区分"记录不存在"和"数据库故障"，
// 导致 handler 层无法返回正确的业务错误码。
//
// 检测方式：
//   1. 找到 service 层所有 .First()/.Take()/.Last() 调用
//   2. 通过 types.Info 确认是 *gorm.DB 的方法
//   3. 检查函数体中是否有 "ErrRecordNotFound" 字符串（说明有区分处理）
//
// 豁免：repository 层（由 service 调用方负责）、handler 层、测试文件。
//
// 修复方式：用 errors.Is(err, gorm.ErrRecordNotFound) 区分处理。
func checkGormNotFound(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if !isServicePkg(path) || decl.Body == nil {
			return
		}

		bodyStr := nodeStr(pass.Fset, decl.Body)
		hasNotFoundCheck := strings.Contains(bodyStr, "ErrRecordNotFound")

		ast.Inspect(decl.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			method := sel.Sel.Name
			if method != "First" && method != "Take" && method != "Last" {
				return true
			}
			if !isGormMethod(pass.TypesInfo, sel) {
				return true
			}
			if hasNotFoundCheck {
				return true
			}

			findings = append(findings, Finding{
				Category: "gorm-safety",
				Rule:     "gorm-notfound",
				Severity: "warning",
				File:     path,
				Line:     lineOf(pass.Fset, call.Pos()),
				Message:  funcName(decl) + ": " + method + "() 未区分 ErrRecordNotFound",
			})
			return true
		})
	})
	return findings
}

// checkGormTOCTOU 检测 GORM 读写操作的 TOCTOU（Time of Check to Time of Use）竞态风险。
//
// 规则：同一个函数内如果既有 GORM 读操作（First/Find/Count 等）又有写操作
// （Create/Update/Delete 等），但不在 Transaction 内执行，则存在竞态风险——
// 读到的数据可能在写之前被其他请求修改。
//
// 检测方式：
//   1. 遍历函数体，收集所有 GORM 读和写操作的行号
//   2. 通过 types.Info 确认每个操作都是 *gorm.DB 的方法
//   3. 如果同时有读和写：
//      - 没有 Transaction → warning（应加事务）
//      - 有 Transaction 但读没有 clause.Locking → info（可能需要 FOR UPDATE）
//
// 豁免：测试文件。
//
// 修复方式：将读写操作放入 db.Transaction() 回调中，读操作加 clause.Locking。
func checkGormTOCTOU(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil {
			return
		}

		var reads, writes []int
		hasTx := false
		hasLocking := false

		ast.Inspect(decl.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			method := sel.Sel.Name

			if method == "Transaction" {
				hasTx = true
			}

			if gormReadMethods[method] && isGormMethod(pass.TypesInfo, sel) {
				reads = append(reads, lineOf(pass.Fset, call.Pos()))
				chainStr := nodeStr(pass.Fset, call)
				if strings.Contains(chainStr, "Locking") {
					hasLocking = true
				}
			}
			if gormWriteMethods[method] && isGormMethod(pass.TypesInfo, sel) {
				writes = append(writes, lineOf(pass.Fset, call.Pos()))
			}
			return true
		})

		if len(reads) == 0 || len(writes) == 0 {
			return
		}

		if !hasTx {
			findings = append(findings, Finding{
				Category: "gorm-safety",
				Rule:     "gorm-toctou",
				Severity: "warning",
				File:     path,
				Line:     reads[0],
				Message:  fmt.Sprintf("%s: GORM 读(%d处)+写(%d处)不在 Transaction 内", funcName(decl), len(reads), len(writes)),
			})
		} else if !hasLocking {
			findings = append(findings, Finding{
				Category: "gorm-safety",
				Rule:     "gorm-toctou",
				Severity: "info",
				File:     path,
				Line:     reads[0],
				Message:  fmt.Sprintf("%s: 事务内读(%d处)无 FOR UPDATE/FOR SHARE", funcName(decl), len(reads)),
			})
		}
	})
	return findings
}

// checkGormMultiTable 检测多表操作不在事务内。
//
// 规则：如果一个函数操作了 2 个以上不同的数据库表（通过 .Table() 或 .Model() 判断），
// 但不在 Transaction 内，则跨表操作可能出现不一致。
//
// 检测方式：
//   1. 收集函数中所有 .Table("xxx") 和 .Model(&xxx{}) 的参数
//   2. 如果不同的表名/模型 >= 2 且函数体中没有 "Transaction" → 标记
//
// 豁免：测试文件。
//
// 修复方式：将跨表操作放入 db.Transaction() 回调中。
func checkGormMultiTable(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil {
			return
		}
		bodyStr := nodeStr(pass.Fset, decl.Body)
		tables := make(map[string]bool)

		ast.Inspect(decl.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if (sel.Sel.Name == "Table" || sel.Sel.Name == "Model") && len(call.Args) > 0 {
				tables[nodeStr(pass.Fset, call.Args[0])] = true
			}
			return true
		})

		if len(tables) >= 2 && !strings.Contains(bodyStr, "Transaction") {
			findings = append(findings, Finding{
				Category: "gorm-safety",
				Rule:     "gorm-multi-table",
				Severity: "warning",
				File:     path,
				Line:     lineOf(pass.Fset, decl.Pos()),
				Message:  fmt.Sprintf("%s: 操作 %d 个表但不在 Transaction 内", funcName(decl), len(tables)),
			})
		}
	})
	return findings
}

// checkSQLConcat 检测 GORM 的 Where/Exec/Raw 方法中使用字符串拼接构建 SQL。
//
// 规则：SQL 查询必须使用参数化占位符（`?`），禁止通过 `+` 拼接变量到 SQL 字符串中，
// 防止 SQL 注入攻击。
//
// 检测方式：
//   1. 找到 .Where()/.Exec()/.Raw()/.Having()/.Order() 调用
//   2. 检查第一个参数是否为 BinaryExpr（+ 拼接）
//   3. 排除纯常量拼接：如果拼接的所有部分都是字面量/标识符/选择器（如 const 拼接），跳过
//   4. 含 CallExpr/IndexExpr 等动态表达式 → 标记为注入风险
//
// 豁免：测试文件、纯常量拼接（如 `"INSERT INTO " + analytics.TableName + ...`）。
//
// 修复方式：改为参数化查询 `db.Where("field = ?", value)`。
func checkSQLConcat(pass *CheckPass) []Finding {
	sqlMethods := map[string]bool{
		"Where": true, "Exec": true, "Raw": true, "Having": true, "Order": true,
	}
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil {
			return
		}
		ast.Inspect(decl.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !sqlMethods[sel.Sel.Name] {
				return true
			}

			firstArg := call.Args[0]
			bin, ok := firstArg.(*ast.BinaryExpr)
			if !ok || bin.Op != token.ADD {
				return true
			}
			// 排除纯常量拼接
			if isConstConcat(bin) {
				return true
			}

			findings = append(findings, Finding{
				Category: "gorm-safety",
				Rule:     "sql-concat",
				Severity: "error",
				File:     path,
				Line:     lineOf(pass.Fset, call.Pos()),
				Message:  funcName(decl) + ": " + sel.Sel.Name + "() 使用字符串拼接构建 SQL",
			})
			return true
		})
	})
	return findings
}

// checkGormManualTxRollback 检测手动事务（db.Begin()）缺少 defer tx.Rollback()。
//
// 规则：使用 db.Begin() 开始的手动事务，必须有 `defer tx.Rollback()` 兜底。
// 如果在 Commit() 之前发生 panic 或提前 return，没有 defer Rollback() 会导致
// 事务锁不释放、连接池耗尽。
//
// 安全的替代方案：使用 db.Transaction(func(tx *gorm.DB) error { ... })，
// 框架会自动处理 rollback。本检查只针对手动 Begin() 的场景。
//
// 检测方式：
//   1. 找到 .Begin() 调用，通过 types.Info 确认是 *gorm.DB 的方法
//   2. 检查函数体中是否有 defer xxx.Rollback() 调用
//
// 豁免：测试文件。
//
// 修复方式：在 Begin() 后紧跟 `defer tx.Rollback()`，或改用 db.Transaction()。
func checkGormManualTxRollback(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil {
			return
		}

		// 检查是否有 defer Rollback()
		bodyStr := nodeStr(pass.Fset, decl.Body)
		hasBegin := false
		var beginLine int

		ast.Inspect(decl.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Begin" {
				return true
			}
			if !isGormMethod(pass.TypesInfo, sel) {
				return true
			}
			hasBegin = true
			beginLine = lineOf(pass.Fset, call.Pos())
			return true
		})

		if hasBegin && !strings.Contains(bodyStr, "Rollback") {
			findings = append(findings, Finding{
				Category: "gorm-safety",
				Rule:     "gorm-manual-tx",
				Severity: "error",
				File:     path,
				Line:     beginLine,
				Message:  funcName(decl) + ": db.Begin() 后缺少 defer tx.Rollback()（或改用 db.Transaction()）",
			})
		}
	})
	return findings
}

// checkRowsErr 检测 GORM db.Rows() 迭代后未检查 rows.Err()。
//
// 规则：GORM 的 db.Rows() 返回 *sql.Rows。在 for rows.Next() 循环结束后，
// 必须检查 rows.Err() 以确认迭代是否正常完成。如果迭代因连接断开、超时等原因
// 中断，rows.Next() 会返回 false 但不会主动报错，导致返回不完整的数据。
//
// 检测方式：
//   1. 找到 .Rows() 调用，确认是 *gorm.DB 的方法
//   2. 检查函数体中是否有 ".Err()" 调用
//   3. 没有则标记为违规
//
// 豁免：测试文件。
//
// 修复方式：在 rows.Next() 循环后添加：
//
//	if err := rows.Err(); err != nil {
//	    return nil, err
//	}
func checkRowsErr(pass *CheckPass) []Finding {
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if decl.Body == nil {
			return
		}

		bodyStr := nodeStr(pass.Fset, decl.Body)
		hasRows := false
		var rowsLine int

		ast.Inspect(decl.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Rows" {
				return true
			}
			if isGormMethod(pass.TypesInfo, sel) {
				hasRows = true
				rowsLine = lineOf(pass.Fset, call.Pos())
			}
			return true
		})

		if hasRows && !strings.Contains(bodyStr, ".Err()") {
			findings = append(findings, Finding{
				Category: "gorm-safety",
				Rule:     "rows-err",
				Severity: "warning",
				File:     path,
				Line:     rowsLine,
				Message:  funcName(decl) + ": db.Rows() 迭代后未检查 rows.Err()，可能返回不完整数据",
			})
		}
	})
	return findings
}

// checkGormSelectStar 检测对大表的 GORM 查询未指定 Select 列。
//
// 规则：Find/First/Take 等查询如果不指定 Select()，GORM 默认 SELECT *，
// 会拉取所有列（包括大 text/blob 字段）。在列表查询场景下浪费带宽和内存，
// 且在分页查询中放大问题（N 行 × 全列）。
//
// 检测方式：
//   1. 找到 .Find() 调用（列表查询最常见的全表扫描场景）
//   2. 通过 types.Info 确认是 *gorm.DB 的方法
//   3. 检查链式调用中是否有 .Select()
//   4. 没有 Select → 标记
//
// 豁免：
//   - 测试文件
//   - First/Take/Last（单行查询，SELECT * 可接受）
//   - 链式调用中已有 Select
//   - repository 层（repository 内部可能有多处调用，由 service 层决定是否 Select）
//
// 修复方式：添加 `.Select("id", "name", "status")` 指定需要的列。
func checkGormSelectStar(pass *CheckPass) []Finding {
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
			if !ok || sel.Sel.Name != "Find" {
				return true
			}
			if !isGormMethod(pass.TypesInfo, sel) {
				return true
			}

			// 检查链式调用中是否有 Select
			chainStr := nodeStr(pass.Fset, call)
			if strings.Contains(chainStr, "Select") {
				return true
			}
			// 检查函数体中是否有 receiver.Select 模式
			if ident, ok := sel.X.(*ast.Ident); ok {
				bodyStr := nodeStr(pass.Fset, decl.Body)
				if strings.Contains(bodyStr, ident.Name+".Select") {
					return true
				}
			}

			findings = append(findings, Finding{
				Category: "gorm-safety",
				Rule:     "gorm-select-star",
				Severity: "info",
				File:     path,
				Line:     lineOf(pass.Fset, call.Pos()),
				Message:  funcName(decl) + ": Find() 无 Select()，默认 SELECT *（大表场景应指定列）",
			})
			return true
		})
	})
	return findings
}

// isConstConcat 递归判断 BinaryExpr（+ 拼接）是否完全由编译期常量组成。
//
// 安全的常量拼接示例：
//   - "INSERT INTO " + analytics.TableName + " (...)"  → 全是字面量和包级常量
//   - ("prefix_" + constVar)                            → ParenExpr 包裹也安全
//
// 不安全的动态拼接示例：
//   - "WHERE id = " + strconv.Itoa(id)   → 含 CallExpr（函数调用）
//   - "name = " + params["name"]          → 含 IndexExpr（map/slice 取值）
//
// 递归规则：
//   - BasicLit（字符串/数字字面量）→ 安全
//   - Ident（标识符，通常是 const）→ 安全
//   - SelectorExpr（如 pkg.Const）→ 安全
//   - BinaryExpr（+ 拼接）→ 左右子树均安全则安全
//   - ParenExpr → 内部表达式安全则安全
//   - 其他（CallExpr、IndexExpr 等）→ 不安全
func isConstConcat(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return true
	case *ast.Ident:
		return true
	case *ast.SelectorExpr:
		return true
	case *ast.BinaryExpr:
		return e.Op == token.ADD && isConstConcat(e.X) && isConstConcat(e.Y)
	case *ast.ParenExpr:
		return isConstConcat(e.X)
	default:
		return false
	}
}
