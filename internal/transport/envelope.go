// envelope.go transport 模块 wire format 定义。
//
// 单一 Envelope 结构覆盖所有帧类型(request / response / event / ping / pong),
// 兼容 JSON-RPC 2.0 的核心字段(ID / Method / Params / Result / Error),
// 外加 Meta 做 trace / on-behalf-of 透传。
//
// 不做 protobuf:
//   - 流量 ≪ 千 QPS/agent,JSON overhead 可忽略
//   - 调试:tcpdump / 开发者工具直接可读
//   - agent SDK 零依赖起步门槛
//
// 当前版本不做 seq/ack/replay:连接断了就是断了,agent 重连是全新会话,
// 可靠投递责任在调用方(asyncjob / 业务层自行重试)。
package transport

import "encoding/json"

// EnvelopeType Envelope.Type 取值枚举。
type EnvelopeType string

const (
	// TypeRequest 发起 RPC。必填 ID + Method,期望对端回 Response(同 ID)。
	TypeRequest EnvelopeType = "request"

	// TypeResponse 对 Request 的应答。必填 ID,必含 Result 或 Error 之一。
	TypeResponse EnvelopeType = "response"

	// TypeEvent 单向通知。必填 Method,不期望回。典型场景:agent 主动推 progress。
	TypeEvent EnvelopeType = "event"

	// TypePing / TypePong 心跳。
	// Ping 由 Hub 定期发起,Agent 收到必须立即回 Pong(相同 ID)。
	// 应用层心跳而非 WS frame 层:部分代理会吞 WS ping,应用层跨代理更可靠。
	TypePing EnvelopeType = "ping"
	TypePong EnvelopeType = "pong"
)

// Envelope 单帧消息。所有字段 omitempty,按 Type 语义决定哪些必填。
//
// 合法组合参考:
//
//	request : ID + Method + Params(可选)+ Meta(可选)
//	response: ID + Result 或 Error
//	event   : Method + Params + Meta(可选);无 ID
//	ping    : ID(用于和 pong 对齐)
//	pong    : ID(= 对应 ping 的 ID)
type Envelope struct {
	// ID RPC 相关;Request 生成,Response/Pong 回填。Event 可省。
	ID string `json:"id,omitempty"`

	// Type 帧类型。必填。
	Type EnvelopeType `json:"type"`

	// Method request/event 必填;其它类型可省。
	Method string `json:"method,omitempty"`

	// Params request/event 的入参。使用 json.RawMessage 避免多次 marshal。
	Params json.RawMessage `json:"params,omitempty"`

	// Result response 成功时的结果。
	Result json.RawMessage `json:"result,omitempty"`

	// Error response 失败时的错误体。
	Error *RPCError `json:"error,omitempty"`

	// Meta 跨消息上下文,用于 trace / on-behalf-of 透传。所有 type 都可携带。
	Meta *Meta `json:"meta,omitempty"`
}

// RPCError JSON-RPC 风格错误对象。实现 error 接口,可被 errors.As / errors.Is 检出。
// Code 沿用 Synapse 的 HHHSSCCCC 业务码 —— 跨服务日志统一。
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`

	// Data 可选附加结构化信息,由业务层约定。
	Data json.RawMessage `json:"data,omitempty"`
}

// Error 实现 error 接口,便于业务层直接 return &RPCError{...}。
func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Meta 跨消息携带的上下文元数据。
//
// 字段取名与 Synapse HTTP 侧的 trace_id / user_id / org_id 对齐,SLS 视图可串联。
// on-behalf-of 模式下由 Hub 或业务层在出站消息上自动填充,避免每个 handler 手填。
type Meta struct {
	TraceID string `json:"trace_id,omitempty"`
	UserID  uint64 `json:"user_id,omitempty"`
	OrgID   uint64 `json:"org_id,omitempty"`
}
