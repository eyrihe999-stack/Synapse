// errors.go source 模块错误码与哨兵错误变量定义。
//
// 错误码格式 HHHSSCCCC:
//   - HHH:HTTP 状态码 (400/403/404/409/500)
//   - SS:模块号 21 = source
//   - CCCC:业务码
package source

import "errors"

// ─── 400 段:请求/业务校验错误 ────────────────────────────────────────────────

const (
	// CodeSourceInvalidRequest 请求参数无效
	CodeSourceInvalidRequest = 400210010
	// CodeSourceInvalidVisibility visibility 取值非法(必须为 org/group/private)
	CodeSourceInvalidVisibility = 400210011
	// CodeSourceInvalidKind kind 取值非法
	CodeSourceInvalidKind = 400210012
	// CodeSourceInvalidName name 为空 / 超长 / 非法
	CodeSourceInvalidName = 400210013
	// CodeSourceNameExists 同一 owner 下该 name 已存在
	CodeSourceNameExists = 409210010
	// CodeSourceHasDocuments 删除 source 时该 source 下仍有 doc 引用
	CodeSourceHasDocuments = 409210020

	// CodeSourceGitLabIntegrationMissing external_account_id 在 user_integrations 下找不到 active 凭据
	CodeSourceGitLabIntegrationMissing = 400210020
	// CodeSourceGitLabRepoNotFound GitLab API 返 404,owner 凭据看不到该 project
	CodeSourceGitLabRepoNotFound = 400210021
	// CodeSourceGitLabAuthFailed PAT/OAuth expired/revoked,GitLab API 返 401
	CodeSourceGitLabAuthFailed = 401210010
	// CodeSourceGitLabUpstream GitLab API 5xx / 网络错(暂时性)
	CodeSourceGitLabUpstream = 502210010
)

// ─── 403 段:权限 ─────────────────────────────────────────────────────────────

const (
	// CodeSourceForbidden 调用方无权执行该操作(改 source visibility 等 owner-only 动作)
	CodeSourceForbidden = 403210010
)

// ─── 404 段:资源不存在 ──────────────────────────────────────────────────────

const (
	// CodeSourceNotFound source 不存在(或不属于当前 org)
	CodeSourceNotFound = 404210020
)

// ─── 500 段:内部错误 ────────────────────────────────────────────────────────

const (
	// CodeSourceInternal 内部错误
	CodeSourceInternal = 500210000
)

// ─── Sentinel Errors ────────────────────────────────────────────────────────

var (
	// ErrSourceInvalidRequest 请求参数无效
	ErrSourceInvalidRequest = errors.New("source: invalid request")
	// ErrSourceInvalidVisibility visibility 取值非法
	ErrSourceInvalidVisibility = errors.New("source: invalid visibility")
	// ErrSourceInvalidKind kind 取值非法
	ErrSourceInvalidKind = errors.New("source: invalid kind")
	// ErrSourceInvalidName name 为空 / 超长 / 非法(CreateCustomSource 校验)
	ErrSourceInvalidName = errors.New("source: invalid name")
	// ErrSourceNameExists 同一 owner 在同一 org 下已经存在同名 source
	ErrSourceNameExists = errors.New("source: name exists for this owner")
	// ErrSourceHasDocuments 删除 source 时该 source 下仍有 doc 引用,必须先把 doc 清掉
	ErrSourceHasDocuments = errors.New("source: still has documents")

	// ErrSourceForbidden 无权执行
	ErrSourceForbidden = errors.New("source: forbidden")

	// ErrSourceNotFound source 不存在
	ErrSourceNotFound = errors.New("source: not found")

	// ErrSourceInternal 内部基础设施错误
	ErrSourceInternal = errors.New("source: internal error")

	// ─── GitLab 集成专属(给 service / runner 抛,handler 翻成对应 Code* 返前端) ───

	// ErrSourceGitLabIntegrationMissing 调用方传的 external_account_id 在 user_integrations 下找不到 active 凭据
	ErrSourceGitLabIntegrationMissing = errors.New("source: gitlab integration missing")
	// ErrSourceGitLabRepoNotFound 凭据有效但看不到该 project(404 或 403)
	ErrSourceGitLabRepoNotFound = errors.New("source: gitlab repo not found")
	// ErrSourceGitLabAuthFailed PAT/OAuth 失效(GitLab 返 401);触发 last_sync_status=auth_failed
	ErrSourceGitLabAuthFailed = errors.New("source: gitlab auth failed")
	// ErrSourceGitLabUpstream GitLab API 5xx / 网络错;runner 标 last_sync_status=failed,允许重试
	ErrSourceGitLabUpstream = errors.New("source: gitlab upstream error")
)
