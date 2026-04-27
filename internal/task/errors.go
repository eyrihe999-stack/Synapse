// errors.go task 模块错误码 + 哨兵错误。
//
// 错误码格式 HHHSSCCCC:HHH=HTTP 状态码,SS=模块号 28(task),CCCC=业务码。
// 沿用仓库约定:业务错误统一 HTTP 200 + body 业务码返回,只 ErrTaskInternal 返 500。
package task

import "errors"

// ─── 400:请求 / 业务校验 ─────────────────────────────────────────────────────

const (
	CodeTaskInvalidRequest     = 400280010
	CodeTaskTitleInvalid       = 400280020
	CodeTaskDescriptionInvalid = 400280021
	CodeTaskOutputKindInvalid  = 400280022
	CodeTaskStatusInvalid      = 400280030
	CodeTaskStateTransition    = 400280031 // 状态不允许的跃迁,比如 closed 又 submit

	CodeSubmissionEmpty        = 400280040
	CodeSubmissionTooLarge     = 400280041 // > MaxSubmissionByteSize
	CodeSubmissionContentKind  = 400280042 // content_kind 和 task.output_spec_kind 不匹配

	CodeReviewerDuplicate = 400280050 // 同一 reviewer 对同一 submission 重复决策
	CodeDecisionInvalid   = 400280051

	CodeAssigneeNotInChannel   = 400280060
	CodeReviewerNotInChannel   = 400280061
	CodeRequiredApprovalsRange = 400280062 // required_approvals 超出合理范围
)

// ─── 403:权限 ─────────────────────────────────────────────────────────────

const (
	CodeForbidden = 403280010
)

// ─── 404:资源 ─────────────────────────────────────────────────────────────

const (
	CodeTaskNotFound       = 404280010
	CodeSubmissionNotFound = 404280020
)

// ─── 409:冲突 ─────────────────────────────────────────────────────────────

const (
	CodeTaskAlreadyClaimed = 409280010 // open→in_progress 时已被别人 claim
)

// ─── 500 ──────────────────────────────────────────────────────────────────

const (
	CodeTaskInternal = 500280010
)

// ─── 哨兵错误 ─────────────────────────────────────────────────────────────

var (
	ErrTaskInternal = errors.New("task: internal error")

	ErrTaskTitleInvalid       = errors.New("task: title invalid")
	ErrTaskDescriptionInvalid = errors.New("task: description invalid")
	ErrTaskOutputKindInvalid  = errors.New("task: output kind invalid")
	ErrTaskStatusInvalid      = errors.New("task: status invalid")
	ErrTaskStateTransition    = errors.New("task: state transition not allowed")
	ErrTaskRoleInvalid        = errors.New("task: role invalid (assignee | reviewer | either)")
	ErrTaskLightweightHasContent = errors.New("task: lightweight task does not accept content (use inline_summary)")
	ErrTaskNotFound           = errors.New("task: task not found")
	ErrTaskAlreadyClaimed     = errors.New("task: task already claimed")

	ErrSubmissionEmpty       = errors.New("task: submission empty")
	ErrSubmissionTooLarge    = errors.New("task: submission too large")
	ErrSubmissionContentKind = errors.New("task: submission content kind mismatch")
	ErrSubmissionNotFound    = errors.New("task: submission not found")

	ErrReviewerDuplicate     = errors.New("task: reviewer already decided this submission")
	ErrDecisionInvalid       = errors.New("task: decision invalid")
	ErrAssigneeNotInChannel  = errors.New("task: assignee not in channel")
	ErrReviewerNotInChannel  = errors.New("task: reviewer not in channel")
	ErrRequiredApprovalsRange = errors.New("task: required_approvals out of range")

	ErrForbidden = errors.New("task: forbidden")
)
