// conn.go 单条 agent WS 连接的内部实现。
//
// 每个 Conn 启动时 spawn 三个 goroutine:
//   - readLoop :从 WS 读帧 → dispatch(response 唤醒 pending;request/event 走 handler;ping 回 pong)
//   - writeLoop:从 outbound chan 读帧 → 写 WS
//   - pingLoop :定期 ping + 检查 pong 超时,触发 close
//
// 三个 loop 中任意一个 return 都走统一 close 路径:幂等、关 ws、清 pending、通知 owner。
// 断开即终结,不做 resume —— 对端重连视为新 agent,状态另起。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/transport"
)

// connOwner Conn 对其拥有者(LocalHub)的反向接口。
// 取最小方法集避免 conn.go 反向 import hub.go。
type connOwner interface {
	// dispatchInbound 转发 agent 入站 request/event 给业务 handler。
	// 实现应 spawn goroutine 非阻塞返回,避免堵塞 readLoop。
	dispatchInbound(c *conn, env *transport.Envelope)

	// removeConn 通知拥有者该连接已结束,从注册表里彻底清除。
	removeConn(agentID transport.AgentID, c *conn, reason string)
}

// conn 单条 agent 连接的状态机。
type conn struct {
	ws    *websocket.Conn
	meta  *transport.AgentMeta
	owner connOwner
	log   logger.LoggerInterface

	// outbound 向 agent 写的帧队列。满 → ErrBackpressure。
	outbound chan []byte

	// pending 等待 response 的 Call 请求 ID → 响应 chan。
	// chan 容量 1:readLoop 解到 response 非阻塞 send;Call goroutine 按 ctx 等。
	pendingMu sync.Mutex
	pending   map[string]chan *transport.Envelope

	// 心跳追踪:最后一次从对端收到任何帧的时刻(unix nano)。
	lastRecvAt atomic.Int64

	// 生命周期。
	closeOnce sync.Once
	closed    atomic.Bool
	closeCh   chan struct{}
}

// newConn 构造 conn。调用方随后必须调 start() 启动 3 个 loop。
func newConn(
	ws *websocket.Conn,
	meta *transport.AgentMeta,
	owner connOwner,
	log logger.LoggerInterface,
) *conn {
	c := &conn{
		ws:       ws,
		meta:     meta,
		owner:    owner,
		log:      log,
		outbound: make(chan []byte, transport.OutboundQueueSize),
		pending:  make(map[string]chan *transport.Envelope),
		closeCh:  make(chan struct{}),
	}
	c.lastRecvAt.Store(time.Now().UnixNano())
	// WS 层读超时给足 PongTimeout 余量;实际死判在 pingLoop 里做。
	_ = ws.SetReadDeadline(time.Now().Add(transport.PongTimeout + 5*time.Second))
	ws.SetPongHandler(func(string) error {
		c.lastRecvAt.Store(time.Now().UnixNano())
		_ = ws.SetReadDeadline(time.Now().Add(transport.PongTimeout + 5*time.Second))
		return nil
	})
	return c
}

func (c *conn) start() {
	go c.readLoop()
	go c.writeLoop()
	go c.pingLoop()
}

// ─── Hub 侧调用的方法 ─────────────────────────────────────────────────────────

// notify server→agent 单向 notification(对应 Hub.Notify)。
func (c *conn) notify(method string, params any, meta *transport.Meta) error {
	env := &transport.Envelope{
		Type:   transport.TypeEvent,
		Method: method,
		Meta:   meta,
	}
	if err := fillParams(env, params); err != nil {
		return err
	}
	return c.submit(env)
}

// callTool server→agent tool call,等 response。遵守 ctx + CallHardCeiling。
// out 为 *T 接收 result(nil = 丢弃);对端返业务错则返 *RPCError。
func (c *conn) callTool(ctx context.Context, method string, params any, meta *transport.Meta, out any) error {
	id := uuid.NewString()
	env := &transport.Envelope{
		ID:     id,
		Type:   transport.TypeRequest,
		Method: method,
		Meta:   meta,
	}
	if err := fillParams(env, params); err != nil {
		return err
	}

	respCh := make(chan *transport.Envelope, 1)
	c.pendingMu.Lock()
	if c.pending == nil {
		// conn 已关
		c.pendingMu.Unlock()
		return transport.ErrAgentOffline
	}
	c.pending[id] = respCh
	c.pendingMu.Unlock()

	// 硬顶兜底:防业务忘传 ctx 造成永久阻塞。
	ctx, cancel := context.WithTimeout(ctx, transport.CallHardCeiling)
	defer cancel()

	if err := c.submit(env); err != nil {
		c.removePending(id)
		return err
	}

	select {
	case resp := <-respCh:
		if resp == nil {
			// close 时 pending chan 被 close,读出 nil → agent 下线
			return transport.ErrAgentOffline
		}
		if resp.Error != nil {
			return resp.Error
		}
		if out != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, out); err != nil {
				return fmt.Errorf("unmarshal result: %w: %w", err, transport.ErrTransportInternal)
			}
		}
		return nil
	case <-ctx.Done():
		c.removePending(id)
		return ctx.Err()
	case <-c.closeCh:
		c.removePending(id)
		return transport.ErrAgentOffline
	}
}

// close 触发 Conn 终结。幂等。reason 用于日志与通知 owner。
func (c *conn) close(reason string) {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		close(c.closeCh)
		_ = c.ws.Close()

		// 清 pending,让等待中的 Call 立刻返 offline。
		c.pendingMu.Lock()
		for _, ch := range c.pending {
			close(ch)
		}
		c.pending = nil
		c.pendingMu.Unlock()

		c.owner.removeConn(c.meta.AgentID, c, reason)
	})
}

// ─── 三个 loop ───────────────────────────────────────────────────────────────

func (c *conn) readLoop() {
	defer c.close("read loop exit")
	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			// 主动 close 场景下 ws 已关,ReadMessage 必返 net.ErrClosed,不算"真错",不打 WARN 减噪。
			if !isNormalClose(err) && !c.closed.Load() {
				c.log.WarnCtx(c.baseCtx(), "transport: read error", map[string]any{
					"agent_id": string(c.meta.AgentID), "err": err.Error(),
				})
			}
			return
		}
		c.lastRecvAt.Store(time.Now().UnixNano())

		var env transport.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			c.log.WarnCtx(c.baseCtx(), "transport: invalid envelope", map[string]any{
				"agent_id": string(c.meta.AgentID), "err": err.Error(),
			})
			continue // 单帧损坏不下连接
		}

		switch env.Type {
		case transport.TypePong:
			// lastRecvAt 已更新,无需额外动作
		case transport.TypePing:
			c.submit(&transport.Envelope{
				ID:   env.ID,
				Type: transport.TypePong,
			})
		case transport.TypeResponse:
			c.dispatchResponse(&env)
		case transport.TypeRequest, transport.TypeEvent:
			c.owner.dispatchInbound(c, &env)
		default:
			c.log.WarnCtx(c.baseCtx(), "transport: unknown envelope type", map[string]any{
				"agent_id": string(c.meta.AgentID), "type": string(env.Type),
			})
		}
	}
}

func (c *conn) writeLoop() {
	defer c.close("write loop exit")
	for {
		select {
		case <-c.closeCh:
			return
		case raw, ok := <-c.outbound:
			if !ok {
				return
			}
			_ = c.ws.SetWriteDeadline(time.Now().Add(transport.WriteDeadline))
			if err := c.ws.WriteMessage(websocket.TextMessage, raw); err != nil {
				c.log.WarnCtx(c.baseCtx(), "transport: write error", map[string]any{
					"agent_id": string(c.meta.AgentID), "err": err.Error(),
				})
				return
			}
		}
	}
}

func (c *conn) pingLoop() {
	ticker := time.NewTicker(transport.PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.closeCh:
			return
		case now := <-ticker.C:
			lastRecv := time.Unix(0, c.lastRecvAt.Load())
			if now.Sub(lastRecv) > transport.PongTimeout {
				c.log.WarnCtx(c.baseCtx(), "transport: pong timeout, closing", map[string]any{
					"agent_id":  string(c.meta.AgentID),
					"last_recv": lastRecv.Format(time.RFC3339),
				})
				c.close("pong timeout")
				return
			}
			c.submit(&transport.Envelope{
				ID:   "hb",
				Type: transport.TypePing,
			})
		}
	}
}

// ─── 内部工具 ─────────────────────────────────────────────────────────────────

// submit marshal + 压 outbound。队列满 → ErrBackpressure;已关 → ErrAgentOffline。
func (c *conn) submit(env *transport.Envelope) error {
	if c.closed.Load() {
		return transport.ErrAgentOffline
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w: %w", err, transport.ErrTransportInternal)
	}
	select {
	case c.outbound <- raw:
		return nil
	default:
		return transport.ErrBackpressure
	}
}

// dispatchResponse 唤醒对应 pending Call。
func (c *conn) dispatchResponse(env *transport.Envelope) {
	c.pendingMu.Lock()
	ch, ok := c.pending[env.ID]
	if ok {
		delete(c.pending, env.ID)
	}
	c.pendingMu.Unlock()
	if !ok {
		c.log.DebugCtx(c.baseCtx(), "transport: unsolicited response", map[string]any{
			"agent_id": string(c.meta.AgentID), "id": env.ID,
		})
		return
	}
	// chan 容量 1,不会阻塞 readLoop。
	ch <- env
}

func (c *conn) removePending(id string) {
	c.pendingMu.Lock()
	delete(c.pending, id)
	c.pendingMu.Unlock()
}

// baseCtx 给本连接所有日志 *Ctx 调用用的上下文 —— agent 纬度字段。
// request 纬度的 trace_id 在 dispatchInbound 时叠一层。
func (c *conn) baseCtx() context.Context {
	ctx := context.Background()
	if c.meta.OnBehalfOfUserID != 0 {
		ctx = logger.WithUserID(ctx, c.meta.OnBehalfOfUserID)
	}
	return ctx
}

// ─── helpers ────────────────────────────────────────────────────────────────

func fillParams(env *transport.Envelope, params any) error {
	if params == nil {
		return nil
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params: %w: %w", err, transport.ErrTransportInternal)
	}
	env.Params = raw
	return nil
}

// isNormalClose 过滤 "对端正常断" 的日志噪音。
func isNormalClose(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, websocket.ErrCloseSent) {
		return true
	}
	if ce, ok := err.(*websocket.CloseError); ok {
		switch ce.Code {
		case websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived:
			return true
		}
	}
	return false
}
