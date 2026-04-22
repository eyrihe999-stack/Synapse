package main

import (
	"go/ast"
	"go/token"
	"go/types"
)

// Finding 表示一条 lint 检查发现。
//
// 字段说明：
//   - Category: 问题分类（如 "error-handling"、"gorm-safety"），对应 categoryOrder
//   - Rule:     具体规则标识（如 "err-swallow"、"gorm-save"），用于过滤和统计
//   - Severity: 严重程度（error/warning/info），error 表示必须修复
//   - File:     问题所在文件的相对路径
//   - Line:     问题所在行号（0 表示文件级问题，如缺少必须文件）
//   - Message:  人类可读的问题描述，通常包含函数名和修复建议
type Finding struct {
	Category string `json:"category"`
	Rule     string `json:"rule"`
	Severity string `json:"severity"` // error, warning, info
	File     string `json:"file"`
	Line     int    `json:"line"`
	Message  string `json:"message"`
}

// Report 表示一次完整审计的输出报告。
//
// 字段说明：
//   - Module:   被审计的模块名（如 "order"、"auth"）
//   - Total:    发现的问题总数
//   - Summary:  按 category 分组的问题计数
//   - Findings: 所有发现的详细列表
//
// 序列化为 JSON 输出到 stdout，供 CI 管道或其他工具消费。
type Report struct {
	Module   string         `json:"module"`
	Total    int            `json:"total"`
	Summary  map[string]int `json:"summary"` // category → count
	Findings []Finding      `json:"findings"`
}

// CheckPass 封装单个包的完整分析上下文，作为所有 check 函数的输入。
//
// 字段说明：
//   - Fset:      文件位置集，用于将 token.Pos 转换为行号和文件名
//   - Pkg:       包的类型信息（包名、导入路径等）
//   - TypesInfo: 类型推断结果，包含标识符定义/使用、类型表达式求值结果等
//   - Files:     包中所有 AST 文件（已去重，已排除生成文件）
//   - FilePaths: 与 Files 一一对应的相对路径列表
//   - Module:    当前审计的模块名（如 "order"）
//   - CodeRoot:  模块根目录路径（如 "internal/order"）
type CheckPass struct {
	Fset      *token.FileSet
	Pkg       *types.Package
	TypesInfo *types.Info
	Files     []*ast.File
	FilePaths []string // 与 Files 一一对应的相对路径
	Module    string   // 模块名
	CodeRoot  string   // internal/{module}
}

// CheckFunc 是单包检查函数的统一签名。
//
// 每个 check 函数接收一个 CheckPass（单包上下文），返回该包中发现的所有问题。
// 所有 check 函数注册在 lint.go 的 allChecks 切片中，由 main.go 依次调用。
// 跨包检查（如 checkUnusedExports）不走此签名，而是在 runCrossPackageChecks 中单独处理。
type CheckFunc func(pass *CheckPass) []Finding
