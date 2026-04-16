// errors.go agent 模块错误码与哨兵错误变量定义。
//
// 错误码格式 HHHSSCCCC:
//   - HHH:HTTP 状态码
//   - SS:模块号 12 = agent
//   - CCCC:业务码
package agent

import "errors"

// ─── 400 段:请求/业务校验 ────────────────────────────────────────────────────

const (
	// CodeAgentInvalidRequest 通用请求参数非法。
	CodeAgentInvalidRequest = 400120010
	// CodeAgentSlugInvalid agent slug 格式不合法。
	CodeAgentSlugInvalid = 400120011
	// CodeAgentEndpointInvalid agent endpoint URL 不合法。
	CodeAgentEndpointInvalid = 400120012
	// CodeAgentTypeUnsupported agent 类型不被支持。
	CodeAgentTypeUnsupported = 400120013
	// CodeAgentContextModeInvalid agent context mode 不合法。
	CodeAgentContextModeInvalid = 400120014
	// CodeAgentMaxRoundsOutOfRange agent 最大上下文轮数超出范围。
	CodeAgentMaxRoundsOutOfRange = 400120015
	// CodeAgentTimeoutOutOfRange agent 超时时间超出范围。
	CodeAgentTimeoutOutOfRange = 400120016
	// CodeAgentDisplayNameInvalid agent 显示名不合法。
	CodeAgentDisplayNameInvalid = 400120017
	// CodePublishAlreadyExists 该 agent 在此 org 已有活跃发布。
	CodePublishAlreadyExists = 400120040
	// CodePublishNotPending 发布记录不处于 pending 状态,无法审核。
	CodePublishNotPending = 400120041
	// CodeChatRequestInvalid 对话请求参数不合法。
	CodeChatRequestInvalid = 400120050
	// CodeSessionNotBelongToUser session 不属于当前用户。
	CodeSessionNotBelongToUser = 400120051
)

// ─── 403 段:权限 ─────────────────────────────────────────────────────────────

const (
	// CodeAgentPermissionDenied 用户无权执行此操作。
	CodeAgentPermissionDenied = 403120010
	// CodeAgentNotAuthor 用户不是该 agent 的作者。
	CodeAgentNotAuthor = 403120012
)

// ─── 404 段:资源不存在 ──────────────────────────────────────────────────────

const (
	// CodeAgentNotFound agent 不存在。
	CodeAgentNotFound = 404120020
	// CodePublishNotFound 发布记录不存在。
	CodePublishNotFound = 404120022
	// CodeAgentNotPublishedInOrg agent 未在该 org 发布。
	CodeAgentNotPublishedInOrg = 404120023
	// CodeSessionNotFound session 不存在。
	CodeSessionNotFound = 404120030
)

// ─── 409 段:冲突 ─────────────────────────────────────────────────────────────

const (
	// CodeAgentSlugTaken agent slug 已被占用。
	CodeAgentSlugTaken = 409120010
)

// ─── 429 段:限流 ─────────────────────────────────────────────────────────────

const (
	// CodeChatRateLimited 对话请求触发限流。
	CodeChatRateLimited = 429120050
)

// ─── 503/504 段:上游不可用 ──────────────────────────────────────────────────

const (
	// CodeChatUpstreamTimeout 上游 agent 响应超时。
	CodeChatUpstreamTimeout = 504120010
	// CodeChatUpstreamUnreachable 上游 agent 不可达。
	CodeChatUpstreamUnreachable = 503120020
)

// ─── 500 段:内部错误 ────────────────────────────────────────────────────────

const (
	// CodeAgentInternal agent 模块内部错误。
	CodeAgentInternal = 500120000
	// CodeAgentCryptoFailed 加解密操作失败。
	CodeAgentCryptoFailed = 500120001
)

// ─── Sentinel Errors ────────────────────────────────────────────────────────

var (
	// ErrAgentInvalidRequest 请求参数不合法时返回。
	ErrAgentInvalidRequest = errors.New("agent: invalid request")
	// ErrAgentSlugInvalid agent slug 格式不符合规则时返回。
	ErrAgentSlugInvalid = errors.New("agent: slug invalid")
	// ErrAgentEndpointInvalid agent endpoint URL 不合法时返回。
	ErrAgentEndpointInvalid = errors.New("agent: endpoint invalid")
	// ErrAgentTypeUnsupported agent 类型不被系统支持时返回。
	ErrAgentTypeUnsupported = errors.New("agent: type unsupported")
	// ErrAgentContextModeInvalid context mode 值不合法时返回。
	ErrAgentContextModeInvalid = errors.New("agent: context mode invalid")
	// ErrAgentMaxRoundsOutOfRange 最大上下文轮数超出允许范围时返回。
	ErrAgentMaxRoundsOutOfRange = errors.New("agent: max context rounds out of range")
	// ErrAgentTimeoutOutOfRange 超时时间超出允许范围时返回。
	ErrAgentTimeoutOutOfRange = errors.New("agent: timeout out of range")
	// ErrAgentDisplayNameInvalid 显示名格式不合法时返回。
	ErrAgentDisplayNameInvalid = errors.New("agent: display name invalid")
	// ErrPublishAlreadyExists 该 agent 在目标 org 已有活跃发布记录时返回。
	ErrPublishAlreadyExists = errors.New("agent: publish already exists")
	// ErrPublishNotPending 发布记录不处于 pending 状态时尝试审核返回。
	ErrPublishNotPending = errors.New("agent: publish not pending")
	// ErrChatRequestInvalid 对话请求参数校验失败时返回。
	ErrChatRequestInvalid = errors.New("agent: chat request invalid")
	// ErrSessionNotBelongToUser session 不属于当前请求用户时返回。
	ErrSessionNotBelongToUser = errors.New("agent: session does not belong to user")

	// ErrAgentPermissionDenied 用户无权执行该操作时返回。
	ErrAgentPermissionDenied = errors.New("agent: permission denied")
	// ErrAgentNotAuthor 用户不是该 agent 的作者时返回。
	ErrAgentNotAuthor = errors.New("agent: not author")

	// ErrAgentNotFound agent 记录不存在时返回。
	ErrAgentNotFound = errors.New("agent: agent not found")
	// ErrPublishNotFound 发布记录不存在时返回。
	ErrPublishNotFound = errors.New("agent: publish not found")
	// ErrAgentNotPublishedInOrg agent 未在指定 org 发布时返回。
	ErrAgentNotPublishedInOrg = errors.New("agent: not published in org")
	// ErrSessionNotFound session 记录不存在时返回。
	ErrSessionNotFound = errors.New("agent: session not found")

	// ErrAgentSlugTaken agent slug 已被同一作者的其他 agent 占用时返回。
	ErrAgentSlugTaken = errors.New("agent: slug taken")

	// ErrChatRateLimited 对话请求触发限流时返回。
	ErrChatRateLimited = errors.New("agent: chat rate limited")

	// ErrChatUpstreamTimeout 上游 agent 在超时时间内未响应时返回。
	ErrChatUpstreamTimeout = errors.New("agent: upstream timeout")
	// ErrChatUpstreamUnreachable 上游 agent 连接不可达时返回。
	ErrChatUpstreamUnreachable = errors.New("agent: upstream unreachable")

	// ErrAgentInternal agent 模块内部未预期错误时返回。
	ErrAgentInternal = errors.New("agent: internal error")
	// ErrAgentCryptoFailed 加解密操作失败时返回。
	ErrAgentCryptoFailed = errors.New("agent: crypto failed")
)
