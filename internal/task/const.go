// Package task channel 内结构化任务模块。
//
// 对应设计见 docs/collaboration-design.md §3.5。
//
// 模型:一个 channel 内可并存多个 task;task 由人或系统 agent 创建;assignee 指
// 向 principal(通常是 user,允许一个 user 下任一在线个人 agent 认领);reviewer
// 白名单显式指定,达到 required_approvals 个 approve 即 close。
//
// 第一版只支持 markdown / text 两种产物格式,走 OSS 存储。
package task

import "time"

// Task 状态机。
//
//	draft          → open(创建者显式 publish)
//	open           → in_progress(被 claim)
//	in_progress    → submitted(assignee 提交 submission)
//	submitted      → approved | revision_requested | rejected
//	revision_requested → in_progress(assignee 重新做;旧 submission 保留)
//	cancelled     —— 终态,可由任一状态(未 approved / rejected)进入
const (
	StatusDraft              = "draft"
	StatusOpen               = "open"
	StatusInProgress         = "in_progress"
	StatusSubmitted          = "submitted"
	StatusApproved           = "approved"
	StatusRevisionRequested  = "revision_requested"
	StatusRejected           = "rejected"
	StatusCancelled          = "cancelled"
)

// IsTerminalStatus 是否终态(不能再推进)。
func IsTerminalStatus(s string) bool {
	switch s {
	case StatusApproved, StatusRejected, StatusCancelled:
		return true
	default:
		return false
	}
}

// 产物格式枚举。
const (
	OutputKindMarkdown = "markdown"
	OutputKindText     = "text"
	// OutputKindNone 仅出现在 task_submissions.content_kind:轻量任务
	// (tasks.is_lightweight=true)的提交不带文件,落 'none'。tasks 表的
	// output_spec_kind 不允许此值(创建任务时不靠 kind 表达,靠 is_lightweight 字段)。
	OutputKindNone = "none"
)

// IsValidOutputKind 校验 output_spec_kind 字段合法性。
func IsValidOutputKind(k string) bool {
	return k == OutputKindMarkdown || k == OutputKindText
}

// ExtensionForOutputKind 返回给定格式的文件后缀。用于 OSS key 后缀。
func ExtensionForOutputKind(k string) string {
	switch k {
	case OutputKindMarkdown:
		return "md"
	case OutputKindText:
		return "txt"
	default:
		return "bin"
	}
}

// Review decision 枚举。
const (
	DecisionApproved         = "approved"
	DecisionRequestChanges   = "request_changes"
	DecisionRejected         = "rejected"
)

// IsValidDecision 校验 review 决策合法性。
func IsValidDecision(d string) bool {
	switch d {
	case DecisionApproved, DecisionRequestChanges, DecisionRejected:
		return true
	default:
		return false
	}
}

// 字段长度约束。
const (
	TitleMaxLen       = 256
	DescriptionMaxLen = 8 * 1024 // 8KB markdown
	CommentMaxLen     = 4 * 1024
)

// 产物大小上限:第一版硬编码 1MB。Config 后续再提取。
const MaxSubmissionByteSize = 1 * 1024 * 1024

// MaxInlineSummaryLen 轻量任务用 inline_summary 当产物,长度上限对齐 DB 列。
const MaxInlineSummaryLen = 512

// 列表分页。
const (
	ListDefaultLimit = 50
	ListMaxLimit     = 100
)

// 心跳 / 时间相关(当前未使用,预留给后续"我在做"状态/claim 自动释放等场景)。
const DefaultClaimTTL = 24 * time.Hour
