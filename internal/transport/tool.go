// tool.go 业务层注册 tool 所需的公开类型。
//
// 心智模型:**双向 tool calling**。每条 WS 连接两侧对等,都能当 tool provider 也能当
// caller。业务通过 LocalHub.RegisterTool 挂 ToolHandler,收到入站 request/notification
// 时被调;handler 入参是 ToolInvocation(method + params + meta)。
//
// 这两个类型独立一个文件而不塞在 envelope.go,因为它们是"业务面向"的类型(业务代码
// 会 import 它们写 handler),envelope.go 是"wire 面向"(JSON 序列化结构)。分开便于
// 未来 envelope.go 按协议演进时不碰业务 API。
package transport

import (
	"context"
	"encoding/json"
)

// ToolInvocation 传给 ToolHandler 的入站调用封装 —— 无论 request 还是 notification。
// 业务通过 Unmarshal 取出 Params;Meta 中的 trace_id / user_id 会由 transport 注入 ctx,
// handler 直接用 log.*Ctx 即可串联,不用手动从 Meta 里抠。
type ToolInvocation struct {
	From   AgentID
	Method string
	Params json.RawMessage
	Meta   *Meta
}

// Unmarshal 把 Params 解进 v。v 为 nil 表示 handler 不关心参数。
// params 为空(对端没发 params 字段)+ v 非 nil 时返 nil,v 保持零值。
func (r *ToolInvocation) Unmarshal(v any) error {
	if v == nil || len(r.Params) == 0 {
		return nil
	}
	return json.Unmarshal(r.Params, v)
}

// ToolHandler 业务提供给 Hub 的 tool 实现函数 —— 注册给 LocalHub.RegisterTool 使用。
//
// 返回值:
//   - result:tool 成功时的返回值(会被 json.Marshal);notification 场景下被丢弃
//   - err   :失败时的错误。*RPCError 原样透传给对端;普通 error 包成 CodeTransportInternal
//
// ctx 中已注入 trace_id(= ToolInvocation.Meta.TraceID)和 user_id(on-behalf-of),
// 日志用 *Ctx 即可自动串联。
type ToolHandler func(ctx context.Context, inv *ToolInvocation) (result any, err error)
