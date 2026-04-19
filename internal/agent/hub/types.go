// Package hub 维护"本地 agent 通过 WebSocket 反向接入 Synapse"的连接注册表与消息路由。
//
// 单向通道语义:
//   - WS 仅用于 Synapse → Agent(inbound 调用)
//   - Agent 对外调其它 agent 仍走 HTTP(使用 owner 的 OAuth token)
//
// 多路复用:
//   - 一条 WS 可并发承载多个 JSON-RPC 请求
//   - Synapse 给每个出站请求分配一个内部 id(internalID),响应时按 internalID 匹配回调 chan
//   - 外部调用方原始的 JSON-RPC id 在发往 agent 前被改写为 internalID,agent 响应回来再换回
//
// 不做的事(MVP):
//   - 断线重连由 agent 侧负责(指数退避)
//   - notifications / 服务端推送:协议上支持透传但无活跃使用
//   - 跨 Synapse 实例路由(single-node 内存,重启连接全断)
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// 协议常量。
const (
	// writeDeadline 单次 WS 写超时。正常 write 远低于此值;超过说明客户端接收端阻塞。
	writeDeadline = 10 * time.Second
	// initialToolsTimeout 握手后第一次 tools/list 超时。agent 启动慢时要给点余量。
	initialToolsTimeout = 15 * time.Second
	// defaultInvokeTimeout Invoke 默认超时。调用方可以 ctx 覆盖。
	defaultInvokeTimeout = 60 * time.Second
	// pingInterval Synapse 主动 ping 的周期,用来检测死连接。
	pingInterval = 30 * time.Second
)

// ErrAgentOffline agent 当前没有活 WS 连接(调用方应 fallback 到 HTTP endpoint 或返错)。
var ErrAgentOffline = errors.New("hub: agent offline")

// ErrInvokeTimeout 指定时限内未收到响应。
var ErrInvokeTimeout = errors.New("hub: invoke timeout")

// ErrInvalidPayload agent 返回了无法解析的 JSON-RPC 响应。
var ErrInvalidPayload = errors.New("hub: invalid payload from agent")

// OnlineInfo 供 catalog 模块拿 agent 在线快照。
type OnlineInfo struct {
	AgentID     uint64
	OwnerUID    uint64
	ConnectedAt time.Time
	LastSeen    time.Time
	Tools       []json.RawMessage // agent 握手时返的 tools/list.tools 数组(原样 raw json)
}

// agentConn 单条 agent WS 连接的运行时状态。不对外,由 hub 内部管理。
type agentConn struct {
	agentID  uint64
	ownerUID uint64
	conn     *websocket.Conn

	// sendMu 序列化 WS 写 —— coder/websocket 文档明确一次只能一个 goroutine 写。
	sendMu sync.Mutex

	// pending internalID(string) → chan<- []byte。Invoke 注册,read loop 投递。
	pending sync.Map

	// tools 握手时缓存的 tools/list 结果。discovery 读用,要 RWMutex 保护;
	// agent 重连才刷新,写频率低。
	toolsMu sync.RWMutex
	tools   []json.RawMessage

	connectedAt time.Time
	lastSeenUnix atomic.Int64 // unix nano,read loop 每读一帧就更新

	// ctx 控制整条连接的生命期。close / err 时 cancel,pending chan 全部被叫醒。
	ctx    context.Context
	cancel context.CancelFunc
}
