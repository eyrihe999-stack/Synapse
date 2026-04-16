// errors.go agent 模块错误码与哨兵错误变量定义。
//
// 错误码格式 HHHSSCCCC:
//   - HHH:HTTP 状态码 (400/401/403/404/409/429/500/503/504)
//   - SS:模块号 12 = agent
//   - CCCC:业务码
//
// 业务错误(400/403/404/409 等)统一以 HTTP 200 + body 业务码返回。
// 限流错误 429 是唯一保留的非 200 业务错误(网关层需要硬拒绝)。
// 上游/超时错误也走 200,只有 ErrAgentInternal 使用 500。
package agent

import "errors"

// ─── 400 段:请求/业务校验 ────────────────────────────────────────────────────

// 400 12 001x Agent 参数
const (
	// CodeAgentInvalidRequest 请求参数无效
	CodeAgentInvalidRequest = 400120010
	// CodeAgentSlugInvalid agent slug 格式非法
	CodeAgentSlugInvalid = 400120011
	// CodeAgentEndpointInvalid endpoint 非 HTTPS 或不合法
	CodeAgentEndpointInvalid = 400120012
	// CodeAgentProtocolUnsupported 协议不支持(第一版仅 jsonrpc)
	CodeAgentProtocolUnsupported = 400120013
	// CodeAgentTimeoutOutOfRange timeout 超出允许区间
	CodeAgentTimeoutOutOfRange = 400120014
	// CodeAgentRateLimitOutOfRange rate_limit 超出允许区间
	CodeAgentRateLimitOutOfRange = 400120015
	// CodeAgentConcurrentOutOfRange max_concurrent 超出允许区间
	CodeAgentConcurrentOutOfRange = 400120016
	// CodeAgentDisplayNameInvalid display_name 非法
	CodeAgentDisplayNameInvalid = 400120017
)

// 400 12 002x Method 参数
const (
	// CodeMethodEmpty agent 至少需要 1 个 method
	CodeMethodEmpty = 400120020
	// CodeMethodNameInvalid method_name 格式非法
	CodeMethodNameInvalid = 400120021
	// CodeMethodTransportUnsupported transport 不支持(ws 等)
	CodeMethodTransportUnsupported = 400120022
	// CodeMethodLastCannotDelete 最后 1 个 method 不可删除
	CodeMethodLastCannotDelete = 400120023
	// CodeMethodVisibilityInvalid visibility 枚举非法
	CodeMethodVisibilityInvalid = 400120024
)

// 400 12 003x Invoke 参数
const (
	// CodeInvokeMethodMissing JSON-RPC body 缺少 method 字段
	CodeInvokeMethodMissing = 400120030
	// CodeInvokeJSONRPCInvalid JSON-RPC body 格式非法
	CodeInvokeJSONRPCInvalid = 400120031
	// CodeInvokeMethodNotDeclared 调用的 method 未在 agent 中声明
	CodeInvokeMethodNotDeclared = 400120032
)

// 400 12 004x Publish 参数
const (
	// CodePublishAlreadyExists 同 agent+org 已存在 active publish
	CodePublishAlreadyExists = 400120040
	// CodePublishNotPending publish 非 pending,不能 approve/reject
	CodePublishNotPending = 400120041
)

// ─── 401 段:鉴权 ─────────────────────────────────────────────────────────────

const (
	// CodeGatewayAgentAuthFailed 上游 agent HMAC 验证失败(agent 侧拒绝)
	CodeGatewayAgentAuthFailed = 401120010
)

// ─── 403 段:权限 ─────────────────────────────────────────────────────────────

const (
	// CodeAgentPermissionDenied 无权执行
	CodeAgentPermissionDenied = 403120010
	// CodeMethodPrivateInvoke private method 仅作者可调用
	CodeMethodPrivateInvoke = 403120011
	// CodeAgentNotAuthor 仅 agent 作者可执行该操作
	CodeAgentNotAuthor = 403120012
)

// ─── 404 段:资源不存在 ──────────────────────────────────────────────────────

const (
	// CodeAgentNotFound agent 不存在
	CodeAgentNotFound = 404120020
	// CodeMethodNotFound method 不存在
	CodeMethodNotFound = 404120021
	// CodePublishNotFound publish 不存在
	CodePublishNotFound = 404120022
	// CodeAgentNotPublishedInOrg agent 未发布到该 org
	CodeAgentNotPublishedInOrg = 404120023
	// CodeInvocationNotFound invocation 不存在(取消时)
	CodeInvocationNotFound = 404120024
)

// ─── 409 段:冲突 ─────────────────────────────────────────────────────────────

const (
	// CodeAgentSlugTaken 作者内 agent slug 已占用
	CodeAgentSlugTaken = 409120010
	// CodeMethodNameTaken 同 agent 内 method_name 已占用
	CodeMethodNameTaken = 409120011
)

// ─── 429 段:限流(唯一保留非 200 的业务错) ─────────────────────────────────

const (
	// CodeUserRateLimited 用户全局限流
	CodeUserRateLimited = 429120010
	// CodeOrgRateLimited org 全局限流
	CodeOrgRateLimited = 429120020
	// CodeAgentRateLimited agent 全局限流
	CodeAgentRateLimited = 429120030
	// CodeUserAgentRateLimited 用户+agent 组合限流
	CodeUserAgentRateLimited = 429120040
)

// ─── 503 / 504 段:上游不可用 ────────────────────────────────────────────────

const (
	// CodeGatewayAgentTimeout 转发上游超时
	CodeGatewayAgentTimeout = 504120010
	// CodeGatewayAgentUnreachable 转发上游不可达(网络错误)
	CodeGatewayAgentUnreachable = 503120020
	// CodeAgentUnhealthy agent 健康状态 unhealthy,调用快速失败
	CodeAgentUnhealthy = 503120021
	// CodeInvocationCanceled invocation 被客户端取消
	CodeInvocationCanceled = 503120030
)

// ─── 500 段:内部错误 ────────────────────────────────────────────────────────

const (
	// CodeAgentInternal 内部错误
	CodeAgentInternal = 500120000
	// CodeAgentCryptoFailed 加解密/签名失败
	CodeAgentCryptoFailed = 500120001
	// CodeAuditWriteFailed 审计写入失败(仅打日志,不直接返给客户端)
	CodeAuditWriteFailed = 500120002
)

// ─── Sentinel Errors ────────────────────────────────────────────────────────

var (
	// ─ 400 段 ─

	// ErrAgentInvalidRequest 请求参数无效
	ErrAgentInvalidRequest = errors.New("agent: invalid request")
	// ErrAgentSlugInvalid agent slug 非法
	ErrAgentSlugInvalid = errors.New("agent: slug invalid")
	// ErrAgentEndpointInvalid endpoint 非 HTTPS 或不合法
	ErrAgentEndpointInvalid = errors.New("agent: endpoint invalid")
	// ErrAgentProtocolUnsupported 协议不支持
	ErrAgentProtocolUnsupported = errors.New("agent: protocol unsupported")
	// ErrAgentTimeoutOutOfRange timeout 越界
	ErrAgentTimeoutOutOfRange = errors.New("agent: timeout out of range")
	// ErrAgentRateLimitOutOfRange rate_limit 越界
	ErrAgentRateLimitOutOfRange = errors.New("agent: rate limit out of range")
	// ErrAgentConcurrentOutOfRange max_concurrent 越界
	ErrAgentConcurrentOutOfRange = errors.New("agent: concurrent out of range")
	// ErrAgentDisplayNameInvalid display_name 非法
	ErrAgentDisplayNameInvalid = errors.New("agent: display name invalid")

	// ErrMethodEmpty agent 至少需要 1 个 method
	ErrMethodEmpty = errors.New("agent: method empty")
	// ErrMethodNameInvalid method_name 非法
	ErrMethodNameInvalid = errors.New("agent: method name invalid")
	// ErrMethodTransportUnsupported transport 不支持
	ErrMethodTransportUnsupported = errors.New("agent: method transport unsupported")
	// ErrMethodLastCannotDelete 最后 1 个 method 不可删除
	ErrMethodLastCannotDelete = errors.New("agent: last method cannot delete")
	// ErrMethodVisibilityInvalid visibility 枚举非法
	ErrMethodVisibilityInvalid = errors.New("agent: method visibility invalid")

	// ErrInvokeMethodMissing JSON-RPC body 缺少 method
	ErrInvokeMethodMissing = errors.New("agent: invoke method missing")
	// ErrInvokeJSONRPCInvalid JSON-RPC body 格式非法
	ErrInvokeJSONRPCInvalid = errors.New("agent: invoke jsonrpc invalid")
	// ErrInvokeMethodNotDeclared 调用未声明的 method
	ErrInvokeMethodNotDeclared = errors.New("agent: invoke method not declared")

	// ErrPublishAlreadyExists 已有 active publish
	ErrPublishAlreadyExists = errors.New("agent: publish already exists")
	// ErrPublishNotPending publish 非 pending
	ErrPublishNotPending = errors.New("agent: publish not pending")

	// ─ 401 段 ─

	// ErrGatewayAgentAuthFailed 上游 agent HMAC 验证失败
	ErrGatewayAgentAuthFailed = errors.New("agent: gateway agent auth failed")

	// ─ 403 段 ─

	// ErrAgentPermissionDenied 无权执行
	ErrAgentPermissionDenied = errors.New("agent: permission denied")
	// ErrMethodPrivateInvoke private method 仅作者可调用
	ErrMethodPrivateInvoke = errors.New("agent: method private, author only")
	// ErrAgentNotAuthor 非 agent 作者
	ErrAgentNotAuthor = errors.New("agent: not author")

	// ─ 404 段 ─

	// ErrAgentNotFound agent 不存在
	ErrAgentNotFound = errors.New("agent: agent not found")
	// ErrMethodNotFound method 不存在
	ErrMethodNotFound = errors.New("agent: method not found")
	// ErrPublishNotFound publish 不存在
	ErrPublishNotFound = errors.New("agent: publish not found")
	// ErrAgentNotPublishedInOrg 未发布到该 org
	ErrAgentNotPublishedInOrg = errors.New("agent: not published in org")
	// ErrInvocationNotFound invocation 不存在
	ErrInvocationNotFound = errors.New("agent: invocation not found")

	// ─ 409 段 ─

	// ErrAgentSlugTaken slug 已占用
	ErrAgentSlugTaken = errors.New("agent: slug taken")
	// ErrMethodNameTaken method_name 已占用
	ErrMethodNameTaken = errors.New("agent: method name taken")

	// ─ 429 段 ─

	// ErrUserRateLimited 用户全局限流
	ErrUserRateLimited = errors.New("agent: user rate limited")
	// ErrOrgRateLimited org 全局限流
	ErrOrgRateLimited = errors.New("agent: org rate limited")
	// ErrAgentRateLimited agent 全局限流
	ErrAgentRateLimited = errors.New("agent: agent rate limited")
	// ErrUserAgentRateLimited 用户+agent 组合限流
	ErrUserAgentRateLimited = errors.New("agent: user-agent rate limited")

	// ─ 503/504 段 ─

	// ErrGatewayAgentTimeout 转发超时
	ErrGatewayAgentTimeout = errors.New("agent: gateway timeout")
	// ErrGatewayAgentUnreachable 上游不可达
	ErrGatewayAgentUnreachable = errors.New("agent: gateway unreachable")
	// ErrAgentUnhealthy agent 健康状态 unhealthy
	ErrAgentUnhealthy = errors.New("agent: agent unhealthy")
	// ErrInvocationCanceled 被客户端取消
	ErrInvocationCanceled = errors.New("agent: invocation canceled")

	// ─ 500 段 ─

	// ErrAgentInternal 内部错误
	ErrAgentInternal = errors.New("agent: internal error")
	// ErrAgentCryptoFailed 加解密/签名失败
	ErrAgentCryptoFailed = errors.New("agent: crypto failed")
	// ErrAuditWriteFailed 审计写入失败
	ErrAuditWriteFailed = errors.New("agent: audit write failed")
)
