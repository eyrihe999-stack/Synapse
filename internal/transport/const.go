// const.go transport 模块(agent WS 网关)常量定义。
//
// 本层职责仅到"一个 agent 的双向 RPC 通道"为止 —— 业务语义(kb.*、agents.*、
// capability registry 等)统一在 internal/agents/ 下实现,不在 transport 里埋。
//
// 未来计划:整个 transport 会被抽出去做独立 connection-manager 服务。保持
// 这里的常量"只描述传输",业务维度参数不进来,解耦时一刀切不带渣。
package transport

import "time"

// ─── 心跳 ─────────────────────────────────────────────────────────────────────

const (
	// PingInterval 服务端主动发 ping 的周期。
	PingInterval = 20 * time.Second

	// PongTimeout 收到对端最近一次消息(含 pong)后多久没任何动静判死,断开连接。
	// 选 60s 是 3 个 PingInterval 容忍一次丢 pong + 一次网络抖动。
	PongTimeout = 60 * time.Second

	// WriteDeadline 单次写 WS 帧的超时。写超时多半意味对端 TCP 卡死,直接断。
	WriteDeadline = 10 * time.Second
)

// ─── 并发 ────────────────────────────────────────────────────────────────────

const (
	// OutboundQueueSize 单连接 outbound channel 容量。满了 Send/Call 返 ErrBackpressure,
	// 上层自己决定是重试还是丢弃。1024 覆盖突发 burst,agent SDK 消费速度正常时远到不了。
	OutboundQueueSize = 1024
)

// ─── RPC ───────────────────────────────────────────────────────────────────

const (
	// CallHardCeiling 单次 server→agent RPC 的硬顶超时。
	// 业务通过 ctx 控实际超时,这里是"防 ctx 忘传死阻塞"的兜底;
	// 超过此值应当走 asyncjob,不应走 WS RPC。
	CallHardCeiling = 5 * time.Minute
)

// ─── WS handshake ────────────────────────────────────────────────────────────

const (
	// HeaderAgentID agent 握手时声明自己的 ID。
	HeaderAgentID = "X-Agent-ID"

	// HeaderAgentKey agent 握手时携带的 apikey(或未来 JWT)凭证。
	HeaderAgentKey = "X-Agent-Key"

	// AuthModeAPIKey handshake 成功后 AgentMeta.AuthMode 的取值。
	//
	// 当前唯一鉴权方式 —— 不管 agent 是代表服务(kind=system)还是代表某个 user
	// (kind=user,未实装),握手凭据都是 apikey。"是不是 user 的"由 agents 模块
	// 在 AgentMeta.OnBehalfOfUserID 上反映,不在 AuthMode 这个层面区分。
	//
	// 历史:早期设计打算给 user-scoped agent 走 JWT / OAuth,后评估 refresh-token
	// 复杂度 + 把 web session 生命周期耦合到 agent 上不划算,统一走 apikey 方案。
	AuthModeAPIKey = "apikey"
)
