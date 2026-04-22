package main

import (
	"go/ast"
	"go/token"
	"strings"
	"unicode"
)

// suspectPrefixes 列举常见 API key / token 的字符串前缀模式。
//
// 被 checkHardcodedSecret 的方式 B（值模式匹配）使用。
// 每个前缀对应一类已知的密钥格式，通过前缀匹配可以高精度识别真实密钥。
// 匹配时还要求前缀后至少有 4 个字符（排除太短的误匹配）。
var suspectPrefixes = []string{
	"sk-",        // OpenAI secret key
	"sk_live_",   // Stripe live secret key
	"sk_test_",   // Stripe test secret key
	"pk_live_",   // Stripe live publishable key
	"pk_test_",   // Stripe test publishable key
	"AKIA",       // AWS access key
	"ghp_",       // GitHub personal access token
	"gho_",       // GitHub OAuth access token
	"Bearer ",    // Auth bearer token（硬编码）
	"Basic ",     // Auth basic token（硬编码）
	"eyJhbGci",   // JWT token（base64 encoded header）
	"xox",        // Slack token
	"LTAI",       // Aliyun AccessKey
}

// secretVarNames 列举可能包含敏感信息的变量/常量名关键词（小写形式）。
//
// 被 checkHardcodedSecret 的方式 A（变量名匹配）使用。
// 检查时将变量名小写化后与此列表做子串匹配。
// 如变量名含这些关键词且值为非空非占位符字符串字面量 → 报告为硬编码密钥。
var secretVarNames = []string{
	"password", "passwd", "secret", "apikey", "api_key",
	"accesskey", "access_key", "privatekey", "private_key",
	"secretkey", "secret_key", "credential",
}

// checkHardcodedSecret 检测代码中硬编码的密钥、令牌和敏感凭证。
//
// 规则：源代码中不应包含硬编码的 API key、密码、token 等敏感信息。这些信息应从
// 环境变量或配置管理系统中读取。硬编码的密钥一旦提交到代码仓库，即使后续删除
// 也会永久保留在 git 历史中。
//
// 检测方式（双重检测）：
//
// 方式 A — 变量名匹配：
//   1. 遍历所有 const/var 声明
//   2. 如果变量名包含 password/secret/apikey 等敏感词
//   3. 且值为非空字符串字面量 → 标记
//
// 方式 B — 值模式匹配：
//   1. 遍历所有字符串字面量
//   2. 检查是否匹配已知的 API key 前缀（sk-、AKIA、ghp_ 等）
//   3. 或者是否为高熵字符串（≥ 24 字符，包含大小写和数字，可能是随机生成的密钥）
//
// 豁免：测试文件、空字符串、占位符值（"xxx"、"changeme"、"placeholder" 等）。
//
// 修复方式：将敏感值移至环境变量或 config server，代码中用 os.Getenv() 读取。
func checkHardcodedSecret(pass *CheckPass) []Finding {
	var findings []Finding

	for i, file := range pass.Files {
		path := pass.FilePaths[i]
		if isTestFile(path) {
			continue
		}

		// 方式 A：检查 const/var 声明的变量名
		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || (genDecl.Tok != token.CONST && genDecl.Tok != token.VAR) {
				continue
			}
			for _, spec := range genDecl.Specs {
				valSpec, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, name := range valSpec.Names {
					nameLower := strings.ToLower(name.Name)
					for _, keyword := range secretVarNames {
						if strings.Contains(nameLower, keyword) {
							for _, val := range valSpec.Values {
								lit, ok := val.(*ast.BasicLit)
								if !ok || lit.Kind != token.STRING {
									continue
								}
								cleaned := strings.Trim(lit.Value, `"'` + "`")
								if cleaned == "" || isPlaceholder(cleaned) {
									continue
								}
								findings = append(findings, Finding{
									Category: "security",
									Rule:     "hardcoded-secret",
									Severity: "error",
									File:     path,
									Line:     lineOf(pass.Fset, lit.Pos()),
									Message:  "疑似硬编码敏感信息: " + name.Name + " = " + truncate(cleaned, 20),
								})
							}
							break
						}
					}
				}
			}
		}

		// 方式 B：检查字符串字面量的值模式
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			val := strings.Trim(lit.Value, `"'` + "`")
			if len(val) < 8 || isPlaceholder(val) {
				return true
			}

			for _, prefix := range suspectPrefixes {
				if strings.HasPrefix(val, prefix) && len(val) > len(prefix)+4 {
					findings = append(findings, Finding{
						Category: "security",
						Rule:     "hardcoded-secret",
						Severity: "error",
						File:     path,
						Line:     lineOf(pass.Fset, lit.Pos()),
						Message:  "疑似硬编码 token/key: " + truncate(val, 20),
					})
					return true
				}
			}
			return true
		})
	}
	return findings
}

// isPlaceholder 判断字符串值是否为占位符/示例值（非真实密钥）。
//
// 通过检查小写化后的字符串是否包含常见占位符关键词来排除误报：
//   - 开发占位符：xxx、changeme、placeholder、dummy、fake
//   - 待办标记：todo、fixme、replace
//   - 模板语法：<、${、{{（如 "${SECRET_KEY}"、"<your-api-key>"）
//   - 测试值：test、your-、example
//
// 匹配任意一个即认为是占位符，不报告为硬编码密钥。
func isPlaceholder(s string) bool {
	lower := strings.ToLower(s)
	placeholders := []string{
		"xxx", "changeme", "placeholder", "your-", "example",
		"todo", "fixme", "replace", "dummy", "test", "fake",
		"<", "${", "{{",
	}
	for _, p := range placeholders {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// checkJSONTagMissing 检测 handler 层的请求/响应结构体字段缺少 json tag。
//
// 规则：handler/ 包中的导出结构体（通常是 API 请求/响应体）的每个导出字段都必须有
// `json:"xxx"` tag，否则序列化后的字段名依赖 Go 的默认规则（首字母大写），
// 导致 API 契约不可控，且前端无法稳定使用。
//
// 检测方式：
//   1. 在 handler/ 包中查找所有导出的 struct 类型声明
//   2. 遍历每个导出字段
//   3. 检查 struct tag 中是否包含 `json:` 前缀
//
// 豁免：
//   - 测试文件
//   - 非 handler 包（model 等包的 struct 可能有不同约定）
//   - 嵌入字段（匿名字段，如 `BaseResponse`）
//   - 非导出字段
//
// 修复方式：为每个导出字段添加 `json:"fieldName"` tag。
func checkJSONTagMissing(pass *CheckPass) []Finding {
	var findings []Finding

	for i, file := range pass.Files {
		path := pass.FilePaths[i]
		if isTestFile(path) || !isHandlerPkg(path) {
			continue
		}

		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.TYPE {
				continue
			}
			for _, spec := range genDecl.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || !ts.Name.IsExported() {
					continue
				}
				structType, ok := ts.Type.(*ast.StructType)
				if !ok || structType.Fields == nil {
					continue
				}

				for _, field := range structType.Fields.List {
					// 跳过嵌入字段（匿名字段）
					if len(field.Names) == 0 {
						continue
					}
					for _, name := range field.Names {
						if !name.IsExported() {
							continue
						}
						hasJSONTag := false
						if field.Tag != nil {
							tagVal := strings.Trim(field.Tag.Value, "`")
							if strings.Contains(tagVal, "json:") {
								hasJSONTag = true
							}
						}
						if !hasJSONTag {
							findings = append(findings, Finding{
								Category: "security",
								Rule:     "json-tag-missing",
								Severity: "warning",
								File:     path,
								Line:     lineOf(pass.Fset, field.Pos()),
								Message:  ts.Name.Name + "." + name.Name + ": 导出字段缺少 json tag",
							})
						}
					}
				}
			}
		}
	}
	return findings
}

// checkBindingTagMissing 检测 handler 层 request struct 必填字段缺少 binding:"required" tag。
//
// 规则：handler/ 包中名称含 "Req"/"Request" 的结构体，导出字段如果有 `json` tag
// 但没有 `binding:"required"` tag，Gin 在绑定时不会校验该字段是否存在。
// 客户端不传该字段时会使用零值（空字符串、0），导致业务逻辑静默处理无效数据。
//
// 检测方式：
//   1. 在 handler/ 包中找到名称含 "Req"/"Request" 的导出 struct
//   2. 遍历每个有 json tag 的导出字段
//   3. 如果没有 binding tag → 标记为信息级（可能是可选字段，但需人工确认）
//
// 豁免：测试文件、非 handler 包、Response 结构体、嵌入字段。
//
// 修复方式：对必填字段添加 `binding:"required"`，可选字段保持现状。
func checkBindingTagMissing(pass *CheckPass) []Finding {
	var findings []Finding

	for i, file := range pass.Files {
		path := pass.FilePaths[i]
		if isTestFile(path) || !isHandlerPkg(path) {
			continue
		}

		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.TYPE {
				continue
			}
			for _, spec := range genDecl.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || !ts.Name.IsExported() {
					continue
				}
				name := ts.Name.Name
				// 只检查 Request 类结构体
				if !strings.Contains(name, "Req") && !strings.Contains(name, "Request") {
					continue
				}
				// 排除 Response 类
				if strings.Contains(name, "Resp") || strings.Contains(name, "Response") {
					continue
				}
				structType, ok := ts.Type.(*ast.StructType)
				if !ok || structType.Fields == nil {
					continue
				}

				for _, field := range structType.Fields.List {
					if len(field.Names) == 0 {
						continue
					}
					for _, fieldName := range field.Names {
						if !fieldName.IsExported() {
							continue
						}
						if field.Tag == nil {
							continue
						}
						tagVal := strings.Trim(field.Tag.Value, "`")
						hasJSON := strings.Contains(tagVal, "json:")
						hasBinding := strings.Contains(tagVal, "binding:")
						if hasJSON && !hasBinding {
							findings = append(findings, Finding{
								Category: "security",
								Rule:     "binding-tag-missing",
								Severity: "info",
								File:     path,
								Line:     lineOf(pass.Fset, field.Pos()),
								Message:  name + "." + fieldName.Name + ": Request struct 字段有 json tag 但无 binding tag（必填字段应加 binding:\"required\"）",
							})
						}
					}
				}
			}
		}
	}
	return findings
}

// checkEmptyStructTag 检测 struct tag 中可能为笔误的空值或无效格式。
//
// 规则：struct tag 中 `json:""` 是合法但容易误用的写法：
//   - `json:""` → 序列化时字段名为空字符串，几乎不是开发者本意
//   - `json:" "` → 字段名为空格，明显是笔误
//   - tag 中含未闭合引号 → 格式错误，Go 编译器不报错但运行时行为异常
//
// 检测方式：
//   1. 遍历所有 struct 字段的 tag
//   2. 检查 json tag 值是否为空或仅含空白
//
// 豁免：测试文件、`json:"-"`（合法的隐藏字段标记）。
//
// 修复方式：
//   - 隐藏字段用 `json:"-"`
//   - 正常字段用 `json:"fieldName"`
func checkEmptyStructTag(pass *CheckPass) []Finding {
	var findings []Finding

	for i, file := range pass.Files {
		path := pass.FilePaths[i]
		if isTestFile(path) {
			continue
		}

		ast.Inspect(file, func(n ast.Node) bool {
			field, ok := n.(*ast.Field)
			if !ok || field.Tag == nil {
				return true
			}
			tagVal := strings.Trim(field.Tag.Value, "`")

			// 检查 json:"" 模式
			if strings.Contains(tagVal, `json:""`) {
				fieldName := ""
				if len(field.Names) > 0 {
					fieldName = field.Names[0].Name
				}
				findings = append(findings, Finding{
					Category: "security",
					Rule:     "empty-struct-tag",
					Severity: "warning",
					File:     path,
					Line:     lineOf(pass.Fset, field.Pos()),
					Message:  fieldName + ": json tag 值为空（json:\"\"），可能是笔误。隐藏字段应用 json:\"-\"",
				})
			}
			return true
		})
	}
	return findings
}

// checkUnexportedReturn 检测导出函数返回未导出类型。
//
// 规则：导出函数返回未导出的具体类型（小写字母开头的 struct/interface），
// 外部包无法直接声明该类型的变量，导致 API 不完整。这通常是重构遗留或设计疏忽。
//
// 检测方式：
//   1. 遍历所有导出函数的返回值列表
//   2. 检查返回类型是否为未导出的标识符（小写字母开头且不是内置类型）
//   3. 排除 error、interface{}、context.Context 等内置/常用类型
//
// 豁免：测试文件、返回 error/interface{}/any 的情况、指针类型指向的底层类型。
//
// 修复方式：将返回类型导出，或改为返回接口类型。
func checkUnexportedReturn(pass *CheckPass) []Finding {
	builtinTypes := map[string]bool{
		"error": true, "bool": true, "string": true, "int": true, "int8": true,
		"int16": true, "int32": true, "int64": true, "uint": true, "uint8": true,
		"uint16": true, "uint32": true, "uint64": true, "float32": true, "float64": true,
		"complex64": true, "complex128": true, "byte": true, "rune": true, "any": true,
	}
	var findings []Finding

	eachNonTestFunc(pass, func(file *ast.File, path string, decl *ast.FuncDecl) {
		if !decl.Name.IsExported() || decl.Type.Results == nil {
			return
		}

		for _, result := range decl.Type.Results.List {
			typeName := ""
			switch t := result.Type.(type) {
			case *ast.Ident:
				typeName = t.Name
			case *ast.StarExpr:
				if ident, ok := t.X.(*ast.Ident); ok {
					typeName = ident.Name
				}
			}
			if typeName == "" || builtinTypes[typeName] {
				continue
			}
			// 检查是否为未导出类型（首字母小写）
			if len(typeName) > 0 && unicode.IsLower(rune(typeName[0])) {
				findings = append(findings, Finding{
					Category: "structure",
					Rule:     "unexported-return",
					Severity: "info",
					File:     path,
					Line:     lineOf(pass.Fset, decl.Pos()),
					Message:  funcName(decl) + ": 导出函数返回未导出类型 " + typeName,
				})
			}
		}
	})
	return findings
}

// isHighEntropy 检查字符串是否具有高熵特征（可能是随机生成的密钥）。
//
// 判断标准（三个条件同时满足）：
//   - 长度 >= 24 字符（短字符串不太可能是密钥）
//   - 同时包含大写字母（A-Z）
//   - 同时包含小写字母（a-z）
//   - 同时包含数字（0-9）
//
// 满足以上条件的字符串具有混合字符集特征，高度疑似随机生成的 API key 或 token。
// 纯大写（如 AWS region 名）、纯数字（如端口号）或纯小写（如域名）不会触发。
//
// 注意：当前未在 checkHardcodedSecret 中启用，保留为可选的增强检测手段。
func isHighEntropy(s string) bool {
	if len(s) < 24 {
		return false
	}
	hasUpper, hasLower, hasDigit := false, false, false
	for _, r := range s {
		switch {
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsLower(r):
			hasLower = true
		case unicode.IsDigit(r):
			hasDigit = true
		}
	}
	return hasUpper && hasLower && hasDigit
}
