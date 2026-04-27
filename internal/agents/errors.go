// errors.go agents 模块错误码与哨兵错误。
//
// 错误码格式 HHHSSCCCC:HHH=HTTP 状态,SS=模块号 25,CCCC=业务码。
package agents

import "errors"

// ─── 400 段 ──────────────────────────────────────────────────────────────────

const (
	// CodeAgentInvalidRequest create/update 入参不合法
	CodeAgentInvalidRequest = 400250010
	// CodeAgentDisplayNameInvalid display_name 为空 / 过长
	CodeAgentDisplayNameInvalid = 400250011
)

// ─── 401 段 ──────────────────────────────────────────────────────────────────

const (
	// CodeAgentAuthFailed handshake apikey 校验失败(agent 连接时)
	CodeAgentAuthFailed = 401250010
	// CodeAgentDisabled handshake 时发现 agent.enabled = false
	CodeAgentDisabled = 401250011
)

// ─── 403 段 ──────────────────────────────────────────────────────────────────

const (
	// CodeAgentForbidden 非 owner/admin/creator 想执行 write 操作
	CodeAgentForbidden = 403250010
)

// ─── 404 段 ──────────────────────────────────────────────────────────────────

const (
	// CodeAgentNotFound 按 ID 或 agent_id 查不到
	CodeAgentNotFound = 404250020
)

// ─── 500 段 ──────────────────────────────────────────────────────────────────

const (
	// CodeAgentInternal 内部错误(DB / 密钥生成失败等)
	CodeAgentInternal = 500250000
)

// ─── Sentinel Errors ────────────────────────────────────────────────────────

var (
	// 400 段

	// ErrAgentInvalidRequest create/update 入参不合法
	ErrAgentInvalidRequest = errors.New("agents: invalid request")
	// ErrAgentDisplayNameInvalid display_name 为空或过长
	ErrAgentDisplayNameInvalid = errors.New("agents: invalid display name")

	// 401 段

	// ErrAgentAuthFailed handshake apikey 校验失败
	ErrAgentAuthFailed = errors.New("agents: auth failed")
	// ErrAgentDisabled agent 存在但被禁用
	ErrAgentDisabled = errors.New("agents: disabled")

	// 403 段

	// ErrAgentForbidden 当前 user 无权管理此 agent
	ErrAgentForbidden = errors.New("agents: forbidden")

	// 404 段

	// ErrAgentNotFound agent_id / ID 不存在
	ErrAgentNotFound = errors.New("agents: not found")

	// 500 段

	// ErrAgentInternal 内部错误
	ErrAgentInternal = errors.New("agents: internal error")
)
