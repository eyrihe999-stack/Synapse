// hub.go agent 双向 tool calling 总线 —— LocalHub。
//
// 心智模型:**双向 tool calling**。每条 WS 连接两侧对等,都能当 tool provider 也能
// 当 caller。Synapse 通过 RegisterTool 暴露给 agent 的能力;CallTool 调 agent 的 tool;
// Notify 发无回执事件。wire 层对齐 JSON-RPC 2.0,method 就是 tool 名。
//
// 职责:
//   - 按 agent_id 维护 *conn 注册表(单连接策略,后来者被拒)
//   - 把 Notify / CallTool 路由到对应 conn
//   - 把 agent 入站 request/notification 按 method 前缀匹配到业务注册的 ToolHandler
//   - 连接断开即从注册表移除 + 通知监听者;不做 resume / grace
//
// 业务(internal/agents/、internal/knowledge/ 等)直接依赖 *LocalHub。当前唯一实现,
// 未来真要做分布式版再抽接口,避免投机抽象。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/gorilla/websocket"

	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/transport"
)

// LocalHub agent 双向 tool calling 总线,本地内存实现。并发安全,所有方法可从任意 goroutine 调用。
//
// 生命周期:
//   1. NewLocalHub 构造
//   2. 业务通过 RegisterTool 注册自己提供给 agent 的 tool
//   3. WS handler 调 Attach 把新连接接进来
//   4. 业务通过 CallTool / Notify 主动调 agent 的 tool 或发事件
//   5. Shutdown 关所有连接 + 拒新请求
type LocalHub struct {
	log logger.LoggerInterface

	connsMu sync.RWMutex
	conns   map[transport.AgentID]*conn

	// tools method prefix → ToolHandler。RegisterTool 注册,dispatchInbound 按最长前缀匹配。
	toolsMu  sync.RWMutex
	tools    map[string]transport.ToolHandler
	prefixes []string // 按长度降序缓存

	// 生命周期回调
	cbMu         sync.RWMutex
	onConnectFns []func(meta *transport.AgentMeta)
	onDisconnFns []func(agentID transport.AgentID, reason string)

	closeMu sync.Mutex
	closed  bool
}

// NewLocalHub 构造一个本地内存 Hub。log 不可为 nil。
func NewLocalHub(log logger.LoggerInterface) *LocalHub {
	return &LocalHub{
		log:   log,
		conns: make(map[transport.AgentID]*conn),
		tools: make(map[string]transport.ToolHandler),
	}
}

// Attach WS handler 完成 handshake 鉴权后调本方法,把新连接交给 Hub 托管。
//
// 错误:
//   - ErrDuplicateAgentID:同 agent_id 已有活跃连接(V1 策略:后来者被拒)
//   - ErrHubClosed       :Hub 已 Shutdown
//
// 成功后 Hub 接管 ws 的生命周期,调用方不再使用 ws。
func (h *LocalHub) Attach(ctx context.Context, ws *websocket.Conn, meta *transport.AgentMeta) error {
	h.connsMu.Lock()
	// 在 connsMu 内再检 closed:Shutdown 和 Attach 交错时
	// 保证要么 Attach 抢先进 map(Shutdown 会扫到并关掉),要么 Attach 被拒。
	if h.isClosed() {
		h.connsMu.Unlock()
		return transport.ErrHubClosed
	}
	if _, exists := h.conns[meta.AgentID]; exists {
		h.connsMu.Unlock()
		h.log.WarnCtx(ctx, "transport: duplicate agent connection rejected", map[string]any{
			"agent_id": string(meta.AgentID),
		})
		return transport.ErrDuplicateAgentID
	}
	c := newConn(ws, meta, h, h.log)
	h.conns[meta.AgentID] = c
	h.connsMu.Unlock()

	c.start()

	h.fireOnConnect(meta)
	h.log.InfoCtx(ctx, "transport: agent connected", map[string]any{
		"agent_id":  string(meta.AgentID),
		"auth_mode": meta.AuthMode,
		"org_id":    meta.OrgID,
	})
	return nil
}

// ─── Hub 接口实现 ────────────────────────────────────────────────────────────

// Notify server → agent 单向 notification(JSON-RPC notification),不等回执。
// 典型场景:推送通知、广播状态变更。
//
// 错误:
//   - ErrAgentOffline :目标不在线
//   - ErrBackpressure :outbound 队列满,对端消费跟不上
//   - ErrHubClosed    :Hub 已 Shutdown
//
// 成功返回 ≠ 对端已收到,仅表示已进本端发送队列。
func (h *LocalHub) Notify(ctx context.Context, to transport.AgentID, method string, params any, meta *transport.Meta) error {
	if h.isClosed() {
		return transport.ErrHubClosed
	}
	c, ok := h.lookupConn(to)
	if !ok {
		return transport.ErrAgentOffline
	}
	return c.notify(method, params, meta)
}

// CallTool server → agent RPC,等 response(tool call)。
//
// 超时控制:调用方 ctx + Hub 内部 CallHardCeiling(5min)硬顶,取先到者。
// out 为接收 result 的指针;nil 则丢弃 result。
//
// 错误(附加于 Notify):
//   - *RPCError:对端 tool 返了业务错(errors.As 解出)
//   - ctx 超时 / 取消
func (h *LocalHub) CallTool(ctx context.Context, to transport.AgentID, method string, params any, out any, meta *transport.Meta) error {
	if h.isClosed() {
		return transport.ErrHubClosed
	}
	c, ok := h.lookupConn(to)
	if !ok {
		return transport.ErrAgentOffline
	}
	return c.callTool(ctx, method, params, meta, out)
}

// RegisterTool 注册 Synapse 提供给 agent 的 tool。
//
// methodPrefix 语义:
//   - 精确匹配优先,其次最长前缀匹配(类似 HTTP mux)
//   - 例:注册 "kb." 则 kb.search / kb.upsert 都走它;另外再注册 "kb.search" 会把它分流出去
//   - 同 prefix 重复注册 → panic(启动期就该暴露,不容忍悄悄覆盖)
//
// ToolHandler 处理 inbound request 和 notification 两种 —— notification 时 result 被丢弃,
// err 只进日志。一个 handler 兜两种流,业务不用写两份。
func (h *LocalHub) RegisterTool(methodPrefix string, handler transport.ToolHandler) {
	if methodPrefix == "" || handler == nil {
		panic("transport: RegisterTool requires non-empty prefix and handler")
	}
	h.toolsMu.Lock()
	defer h.toolsMu.Unlock()
	if _, dup := h.tools[methodPrefix]; dup {
		panic(fmt.Sprintf("transport: duplicate tool handler for prefix %q", methodPrefix))
	}
	h.tools[methodPrefix] = handler
	h.prefixes = append(h.prefixes, methodPrefix)
	sort.Slice(h.prefixes, func(i, j int) bool {
		return len(h.prefixes[i]) > len(h.prefixes[j])
	})
}

// OnConnect 订阅连接建立事件。回调在 Hub 内部 goroutine 执行,实现方不要阻塞太久
// (建议只更内存 registry)。同一事件允许多个订阅者,按注册顺序执行。
func (h *LocalHub) OnConnect(fn func(meta *transport.AgentMeta)) {
	if fn == nil {
		return
	}
	h.cbMu.Lock()
	h.onConnectFns = append(h.onConnectFns, fn)
	h.cbMu.Unlock()
}

// OnDisconnect 订阅连接结束事件,语义同 OnConnect。
func (h *LocalHub) OnDisconnect(fn func(agentID transport.AgentID, reason string)) {
	if fn == nil {
		return
	}
	h.cbMu.Lock()
	h.onDisconnFns = append(h.onDisconnFns, fn)
	h.cbMu.Unlock()
}

// IsOnline agent 当前是否有活跃连接。断开即 false —— 不做 resume / grace,
// 与 Notify / CallTool 的可达性语义严格一致。
func (h *LocalHub) IsOnline(agentID transport.AgentID) bool {
	_, ok := h.lookupConn(agentID)
	return ok
}

// Disconnect 主动断掉指定 agent 的当前连接。找不到 agent 返 false。
// 用途:管理操作(disable / rotate key / delete)需要立即让 agent 停止使用旧身份。
// 幂等:agent 不在线返 false,重复调用 true → false。
func (h *LocalHub) Disconnect(agentID transport.AgentID, reason string) bool {
	c, ok := h.lookupConn(agentID)
	if !ok {
		return false
	}
	c.close(reason)
	return true
}

// ListOnline 当前所有 online agent 的 id。用途:监控 / debug / capability registry 初始化兜底。
func (h *LocalHub) ListOnline() []transport.AgentID {
	h.connsMu.RLock()
	defer h.connsMu.RUnlock()
	out := make([]transport.AgentID, 0, len(h.conns))
	for id := range h.conns {
		out = append(out, id)
	}
	return out
}

// Shutdown 关所有连接,拒后续 Send/Call/Attach。幂等。
func (h *LocalHub) Shutdown(ctx context.Context) error {
	h.closeMu.Lock()
	if h.closed {
		h.closeMu.Unlock()
		return nil
	}
	h.closed = true
	h.closeMu.Unlock()

	h.connsMu.Lock()
	conns := h.conns
	h.conns = make(map[transport.AgentID]*conn)
	h.connsMu.Unlock()

	for id, c := range conns {
		c.close("hub shutdown")
		h.log.InfoCtx(ctx, "transport: agent disconnected by shutdown", map[string]any{
			"agent_id": string(id),
		})
	}
	return nil
}

// ─── connOwner 接口 ───────────────────────────────────────────────────────────

// dispatchInbound 由 conn.readLoop 在收到 request/notification 时调。
// 按 method 前缀查 ToolHandler;没 tool 且 type=request → 回 MethodNotFound;event → 仅 log。
func (h *LocalHub) dispatchInbound(c *conn, env *transport.Envelope) {
	handler := h.lookupTool(env.Method)
	if handler == nil {
		if env.Type == transport.TypeRequest {
			c.submit(&transport.Envelope{
				ID:   env.ID,
				Type: transport.TypeResponse,
				Error: &transport.RPCError{
					Code:    transport.CodeTransportMethodNotFound,
					Message: "tool not found: " + env.Method,
				},
			})
			return
		}
		h.log.DebugCtx(c.baseCtx(), "transport: notification dropped, no handler", map[string]any{
			"agent_id": string(c.meta.AgentID), "method": env.Method,
		})
		return
	}
	// 每条 inbound 开 goroutine 处理,避免慢 handler 堵塞 readLoop。
	go h.runHandler(c, env, handler)
}

// removeConn 由 conn.close 在结束时调。
func (h *LocalHub) removeConn(agentID transport.AgentID, c *conn, reason string) {
	h.connsMu.Lock()
	if cur, ok := h.conns[agentID]; ok && cur == c {
		delete(h.conns, agentID)
	}
	h.connsMu.Unlock()
	h.fireOnDisconnect(agentID, reason)
	h.log.InfoCtx(c.baseCtx(), "transport: agent disconnected", map[string]any{
		"agent_id": string(agentID), "reason": reason,
	})
}

// ─── 内部 ────────────────────────────────────────────────────────────────────

func (h *LocalHub) lookupConn(agentID transport.AgentID) (*conn, bool) {
	h.connsMu.RLock()
	c, ok := h.conns[agentID]
	h.connsMu.RUnlock()
	return c, ok
}

func (h *LocalHub) lookupTool(method string) transport.ToolHandler {
	h.toolsMu.RLock()
	defer h.toolsMu.RUnlock()
	for _, p := range h.prefixes {
		if method == p || strings.HasPrefix(method, p) {
			return h.tools[p]
		}
	}
	return nil
}

func (h *LocalHub) runHandler(c *conn, env *transport.Envelope, handler transport.ToolHandler) {
	ctx := c.baseCtx()
	if env.Meta != nil {
		if env.Meta.TraceID != "" {
			ctx = logger.WithRequestID(ctx, env.Meta.TraceID)
		}
		if env.Meta.UserID != 0 {
			ctx = logger.WithUserID(ctx, env.Meta.UserID)
		}
	}
	inv := &transport.ToolInvocation{
		From:   c.meta.AgentID,
		Method: env.Method,
		Params: env.Params,
		Meta:   env.Meta,
	}

	result, err := safeHandler(ctx, handler, inv, h.log)

	if env.Type == transport.TypeEvent {
		if err != nil {
			h.log.WarnCtx(ctx, "transport: notification handler error", map[string]any{
				"agent_id": string(c.meta.AgentID), "method": env.Method, "err": err.Error(),
			})
		}
		return
	}

	resp := &transport.Envelope{
		ID:   env.ID,
		Type: transport.TypeResponse,
	}
	if err != nil {
		var rpcErr *transport.RPCError
		if errors.As(err, &rpcErr) {
			resp.Error = rpcErr
		} else {
			resp.Error = &transport.RPCError{
				Code:    transport.CodeTransportInternal,
				Message: err.Error(),
			}
		}
	} else if result != nil {
		raw, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			resp.Error = &transport.RPCError{
				Code:    transport.CodeTransportInternal,
				Message: "marshal result: " + marshalErr.Error(),
			}
		} else {
			resp.Result = raw
		}
	}
	if sendErr := c.submit(resp); sendErr != nil {
		h.log.WarnCtx(ctx, "transport: response submit failed", map[string]any{
			"agent_id": string(c.meta.AgentID), "method": env.Method, "err": sendErr.Error(),
		})
	}
}

// safeHandler recover handler panic → error,避免单个 tool bug 拖垮 Conn。
func safeHandler(ctx context.Context, h transport.ToolHandler, inv *transport.ToolInvocation, log logger.LoggerInterface) (result any, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("tool panic: %v", rec)
			log.ErrorCtx(ctx, "transport: tool panic", nil, map[string]any{
				"method": inv.Method, "panic": rec,
			})
		}
	}()
	return h(ctx, inv)
}

func (h *LocalHub) fireOnConnect(meta *transport.AgentMeta) {
	h.cbMu.RLock()
	fns := append([]func(*transport.AgentMeta){}, h.onConnectFns...)
	h.cbMu.RUnlock()
	for _, fn := range fns {
		fn(meta)
	}
}

func (h *LocalHub) fireOnDisconnect(agentID transport.AgentID, reason string) {
	h.cbMu.RLock()
	fns := append([]func(transport.AgentID, string){}, h.onDisconnFns...)
	h.cbMu.RUnlock()
	for _, fn := range fns {
		fn(agentID, reason)
	}
}

func (h *LocalHub) isClosed() bool {
	h.closeMu.Lock()
	defer h.closeMu.Unlock()
	return h.closed
}
