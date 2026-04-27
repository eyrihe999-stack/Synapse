// errors.go transport 模块错误码与哨兵错误定义。
//
// 错误码格式 HHHSSCCCC:
//   - HHH:HTTP 状态码 (400/401/404/409/500)
//   - SS :模块号 23 = transport
//   - CCCC:业务码
//
// Hub API 返 sentinel error,上层(handler 或 agents 模块)用 errors.Is 判断
// 语义再决定对上游的表达方式(业务 200 + body code,还是 HTTP 真 4xx/5xx)。
package transport

import "errors"

// ─── 400 段:请求/握手校验 ────────────────────────────────────────────────────

const (
	// CodeTransportInvalidHandshake handshake 缺 agent_id / key header
	CodeTransportInvalidHandshake = 400230010
	// CodeTransportInvalidEnvelope 收到的帧解不出 Envelope 或 type 非法
	CodeTransportInvalidEnvelope = 400230011
	// CodeTransportMethodNotFound 无 handler 订阅该 method
	CodeTransportMethodNotFound = 404230020
)

// ─── 401 段:鉴权 ────────────────────────────────────────────────────────────

const (
	// CodeTransportAuthFailed apikey / jwt 校验不通过
	CodeTransportAuthFailed = 401230010
)

// ─── 404 / 409 段:状态 ──────────────────────────────────────────────────────

const (
	// CodeTransportAgentOffline 向没在线的 agent 发消息 / 调 RPC
	CodeTransportAgentOffline = 404230030
	// CodeTransportDuplicateAgentID 同 agent_id 已有活跃连接(单连接策略下新连被拒)
	CodeTransportDuplicateAgentID = 409230010
)

// ─── 500 / 503 段:内部 / 过载 ────────────────────────────────────────────────

const (
	// CodeTransportInternal 内部错误(写失败 / 序列化失败等)
	CodeTransportInternal = 500230000
	// CodeTransportBackpressure outbound 队列满,消费跟不上
	CodeTransportBackpressure = 503230010
)

// ─── Sentinel Errors ────────────────────────────────────────────────────────

var (
	// ─ 握手 / 协议 ─

	// ErrInvalidHandshake handshake header 缺失或格式错
	ErrInvalidHandshake = errors.New("transport: invalid handshake")

	// ErrAuthFailed apikey / jwt 校验不通过。Authenticator 实现应返此错或包装它。
	ErrAuthFailed = errors.New("transport: auth failed")

	// ErrInvalidEnvelope 帧格式错 / 必填字段缺失 / 未知 type
	ErrInvalidEnvelope = errors.New("transport: invalid envelope")

	// ErrMethodNotFound 收到的 request method 没有 handler 订阅
	ErrMethodNotFound = errors.New("transport: method not found")

	// ─ 连接状态 ─

	// ErrAgentOffline Send/Call 的目标 agent 当前不在线(含宽限期外)
	ErrAgentOffline = errors.New("transport: agent offline")

	// ErrDuplicateAgentID 同 agent_id 已有活跃连接(单连接策略)。
	// V1 选"拒后来者":新 handshake 返此错;旧连不受影响。
	ErrDuplicateAgentID = errors.New("transport: duplicate agent id")

	// ─ 过载 / 背压 ─

	// ErrBackpressure outbound 队列满。Send 非阻塞返此错,业务自定策略(重试 / 丢)。
	ErrBackpressure = errors.New("transport: backpressure")

	// ─ 其它 ─

	// ErrHubClosed Hub 已 Shutdown。所有后续 Send/Call 直接返此错。
	ErrHubClosed = errors.New("transport: hub closed")

	// ErrTransportInternal 兜底内部错误(序列化 / IO 等)。
	ErrTransportInternal = errors.New("transport: internal error")
)
