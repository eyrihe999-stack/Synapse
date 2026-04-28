// errors.go pm 模块错误码与哨兵错误。
//
// 错误码格式 HHHSSCCCC:
//   - HHH:HTTP 状态码
//   - SS:模块号 29 = pm(已用 01/11/13/15/17/19/21/23/25/27)
//   - CCCC:业务码
//
// 业务错误统一以 HTTP 200 + body 业务码返回(对齐 channel / user 风格);
// 仅 ErrPMInternal 返 500。
package pm

import "errors"

// ─── 400 段:请求 / 业务校验 ─────────────────────────────────────────────────

const (
	// CodePMInvalidRequest 请求参数无效(泛用)
	CodePMInvalidRequest = 400290010

	// Project(从 channel 模块迁移过来,错误码改用 pm 模块号 29)
	CodeProjectNameInvalid    = 400290020
	CodeProjectNameDuplicated = 409290021
	CodeProjectArchived       = 400290022

	// Initiative
	CodeInitiativeNameInvalid    = 400290030
	CodeInitiativeNameDuplicated = 409290031
	CodeInitiativeStatusInvalid  = 400290032
	CodeInitiativeArchived       = 400290033
	CodeInitiativeNotEmpty       = 400290034 // 含未归档 workstream 不能删
	CodeInitiativeSystem         = 400290035 // 系统 initiative(is_system=true)不允许改名/删/archive

	// Version
	CodeVersionNameInvalid    = 400290040
	CodeVersionNameDuplicated = 409290041
	CodeVersionStatusInvalid  = 400290042
	CodeVersionSystem         = 400290043 // 系统 version(Backlog)不允许改名/删

	// Workstream
	CodeWorkstreamNameInvalid       = 400290050
	CodeWorkstreamStatusInvalid     = 400290051
	CodeWorkstreamInitiativeInvalid = 400290052 // initiative 不存在或不属于同 project
	CodeWorkstreamVersionInvalid    = 400290053 // version 不存在或不属于同 project

	// ProjectKBRef
	CodeProjectKBRefInvalid    = 400290060 // source_id / doc_id 二选一约束不满足
	CodeProjectKBRefDuplicated = 409290061 // 同 (project_id, source_id, doc_id) 已存在
)

// ─── 403 段:权限 ─────────────────────────────────────────────────────────────

const (
	// CodeForbidden 调用者无权限执行该操作
	CodeForbidden = 403290010
)

// ─── 404 段:资源 ─────────────────────────────────────────────────────────────

const (
	CodeProjectNotFound      = 404290010
	CodeInitiativeNotFound   = 404290020
	CodeVersionNotFound      = 404290030
	CodeWorkstreamNotFound   = 404290040
	CodeProjectKBRefNotFound = 404290050
)

// ─── 500 段 ─────────────────────────────────────────────────────────────────

const (
	// CodePMInternal 服务内部错误
	CodePMInternal = 500290010
)

// ─── 哨兵错误 ───────────────────────────────────────────────────────────────

var (
	ErrPMInternal = errors.New("pm: internal error")

	ErrProjectNotFound    = errors.New("pm: project not found")
	ErrProjectNameInvalid = errors.New("pm: project name invalid")
	ErrProjectNameDup     = errors.New("pm: project name duplicated")
	ErrProjectArchived    = errors.New("pm: project archived")

	ErrInitiativeNotFound      = errors.New("pm: initiative not found")
	ErrInitiativeNameInvalid   = errors.New("pm: initiative name invalid")
	ErrInitiativeNameDup       = errors.New("pm: initiative name duplicated")
	ErrInitiativeStatusInvalid = errors.New("pm: initiative status invalid")
	ErrInitiativeArchived      = errors.New("pm: initiative archived")
	ErrInitiativeNotEmpty      = errors.New("pm: initiative has active workstreams")
	ErrInitiativeSystem        = errors.New("pm: system initiative cannot be modified or removed")

	ErrVersionNotFound      = errors.New("pm: version not found")
	ErrVersionNameInvalid   = errors.New("pm: version name invalid")
	ErrVersionNameDup       = errors.New("pm: version name duplicated")
	ErrVersionStatusInvalid = errors.New("pm: version status invalid")
	ErrVersionSystem        = errors.New("pm: system version cannot be modified or removed")

	ErrWorkstreamNotFound          = errors.New("pm: workstream not found")
	ErrWorkstreamNameInvalid       = errors.New("pm: workstream name invalid")
	ErrWorkstreamStatusInvalid     = errors.New("pm: workstream status invalid")
	ErrWorkstreamInitiativeInvalid = errors.New("pm: workstream initiative invalid")
	ErrWorkstreamVersionInvalid    = errors.New("pm: workstream version invalid")

	ErrProjectKBRefInvalid    = errors.New("pm: project kb ref invalid")
	ErrProjectKBRefNotFound   = errors.New("pm: project kb ref not found")
	ErrProjectKBRefDuplicated = errors.New("pm: project kb ref duplicated")

	ErrForbidden = errors.New("pm: forbidden")
)
