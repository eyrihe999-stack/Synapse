// hub.go 对外 Hub API:connect / invoke / online info。
package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/eyrihe999-stack/Synapse/pkg/logger"
)

// Hub 管理所有 agent 的 live WS 连接。并发安全。
type Hub struct {
	log   logger.LoggerInterface
	conns sync.Map // agentID(uint64) → *agentConn
}

// New 构造 Hub。
func New(log logger.LoggerInterface) *Hub {
	if log == nil {
		panic("hub: log must be non-nil")
	}
	return &Hub{log: log}
}

// Attach 接收一条已 upgrade 的 WS 连接,做握手(拉 tools/list)并注册到 hub。
// 调用方(handler 层)保证:
//   - conn 已完成 HTTP Upgrade
//   - agentID / ownerUID 已通过 OAuth token + ownership 校验
//
// 阻塞到连接关闭(读循环退出)。handler goroutine 直接 defer 它即可。
func (h *Hub) Attach(ctx context.Context, conn *websocket.Conn, agentID, ownerUID uint64) {
	// 1. 若已存在同 agent 的旧连接,强制关闭,新连接顶上(典型场景:agent 重启还没收到旧 close)。
	if old, ok := h.conns.LoadAndDelete(agentID); ok {
		oc := old.(*agentConn)
		h.log.InfoCtx(ctx, "hub: evicting stale conn for agent", map[string]any{"agent_id": agentID})
		oc.cancel()
		_ = oc.conn.Close(websocket.StatusPolicyViolation, "replaced by newer connection")
	}

	connCtx, cancel := context.WithCancel(ctx)
	ac := &agentConn{
		agentID:     agentID,
		ownerUID:    ownerUID,
		conn:        conn,
		connectedAt: time.Now(),
		ctx:         connCtx,
		cancel:      cancel,
	}
	ac.lastSeenUnix.Store(ac.connectedAt.UnixNano())

	h.conns.Store(agentID, ac)

	// 2. 启 read loop + ping loop
	go ac.readLoop(h.log)
	go ac.pingLoop(h.log)

	// 3. 发 tools/list 握手。失败不致命:agent 可能还没初始化完,后续再发也行。
	if err := h.refreshTools(connCtx, ac); err != nil {
		h.log.WarnCtx(connCtx, "hub: initial tools/list failed", map[string]any{
			"agent_id": agentID, "err": err.Error(),
		})
	}

	h.log.InfoCtx(ctx, "hub: agent connected", map[string]any{
		"agent_id": agentID, "owner_uid": ownerUID, "tool_count": len(ac.tools),
	})

	// 4. 阻塞到连接关闭。读循环退出会 cancel ctx。
	<-connCtx.Done()

	// 5. 清理
	h.conns.CompareAndDelete(agentID, ac) // 只有 ac 还是当前 conn 才删(避免覆盖了新连接)
	_ = conn.Close(websocket.StatusNormalClosure, "")
	h.log.InfoCtx(ctx, "hub: agent disconnected", map[string]any{"agent_id": agentID})
}

// Invoke 把 rpcBody 经 WS 发给指定 agent,等待响应返回。
//
// rpcBody 必须是合法 JSON-RPC 2.0 请求体。Hub 会:
//   1. 把顶层 id 改写为内部 uuid(多路复用)
//   2. 发给 agent
//   3. 等 agent 回复(同 internalID)
//   4. 把 id 换回原值,返给调用方
//
// ctx 控制超时与取消;ctx.Deadline() 有效则用之,否则用 defaultInvokeTimeout。
// agent 不在线 → ErrAgentOffline(调用方可决定 fallback 到 HTTP)。
func (h *Hub) Invoke(ctx context.Context, agentID uint64, rpcBody []byte) ([]byte, error) {
	raw, ok := h.conns.Load(agentID)
	if !ok {
		return nil, ErrAgentOffline
	}
	ac := raw.(*agentConn)

	internalID, err := newInternalID()
	if err != nil {
		return nil, fmt.Errorf("gen internal id: %w", err)
	}

	rewritten, originalID, err := rewriteRPCID(rpcBody, internalID)
	if err != nil {
		return nil, err
	}

	// 注册 pending chan(buf 1,防 read loop 推送时阻塞)
	ch := make(chan []byte, 1)
	ac.pending.Store(internalID, ch)
	defer ac.pending.Delete(internalID)

	// 应用默认超时
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultInvokeTimeout)
		defer cancel()
	}

	// 写
	if err := ac.writeJSON(ctx, rewritten); err != nil {
		// 连接死了?readLoop 会处理,这里返给调用方。
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, ErrInvokeTimeout
		}
		return nil, fmt.Errorf("ws write: %w", err)
	}

	// 等响应
	select {
	case resp := <-ch:
		restored, err := restoreRPCID(resp, originalID)
		if err != nil {
			return nil, ErrInvalidPayload
		}
		return restored, nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ErrInvokeTimeout
		}
		return nil, ctx.Err()
	case <-ac.ctx.Done():
		// 连接在等待期间断了
		return nil, ErrAgentOffline
	}
}

// IsOnline 快速查 agent 是否有活连接。
func (h *Hub) IsOnline(agentID uint64) bool {
	_, ok := h.conns.Load(agentID)
	return ok
}

// OnlineInfo 快照。agent 不在线返 nil, false。
func (h *Hub) OnlineInfo(agentID uint64) (*OnlineInfo, bool) {
	raw, ok := h.conns.Load(agentID)
	if !ok {
		return nil, false
	}
	ac := raw.(*agentConn)
	ac.toolsMu.RLock()
	tools := make([]json.RawMessage, len(ac.tools))
	copy(tools, ac.tools)
	ac.toolsMu.RUnlock()
	return &OnlineInfo{
		AgentID:     ac.agentID,
		OwnerUID:    ac.ownerUID,
		ConnectedAt: ac.connectedAt,
		LastSeen:    time.Unix(0, ac.lastSeenUnix.Load()),
		Tools:       tools,
	}, true
}

// ListOnline 返回当前所有在线 agent ID 的快照。顺序任意。
func (h *Hub) ListOnline() []uint64 {
	out := make([]uint64, 0)
	h.conns.Range(func(key, _ any) bool {
		out = append(out, key.(uint64))
		return true
	})
	return out
}

// ─── 握手 ──────────────────────────────────────────────────────────────────

// refreshTools 主动发一次 tools/list 给 agent,缓存响应里的 tools 数组。
func (h *Hub) refreshTools(ctx context.Context, ac *agentConn) error {
	reqCtx, cancel := context.WithTimeout(ctx, initialToolsTimeout)
	defer cancel()

	body := []byte(`{"jsonrpc":"2.0","id":"init","method":"tools/list"}`)
	resp, err := h.invokeOnConn(reqCtx, ac, body)
	if err != nil {
		return err
	}

	// 解 {result: {tools: [...]}}
	var envelope struct {
		Result struct {
			Tools []json.RawMessage `json:"tools"`
		} `json:"result"`
		Error *struct{} `json:"error,omitempty"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		return fmt.Errorf("parse tools/list resp: %w", err)
	}
	if envelope.Error != nil {
		return fmt.Errorf("tools/list returned error")
	}

	ac.toolsMu.Lock()
	ac.tools = envelope.Result.Tools
	ac.toolsMu.Unlock()
	return nil
}

// invokeOnConn 在已知 agentConn 上发 invoke(不经 hub.conns 查找)。
// 供 refreshTools 等内部握手路径用。
func (h *Hub) invokeOnConn(ctx context.Context, ac *agentConn, rpcBody []byte) ([]byte, error) {
	internalID, err := newInternalID()
	if err != nil {
		return nil, err
	}
	rewritten, originalID, err := rewriteRPCID(rpcBody, internalID)
	if err != nil {
		return nil, err
	}
	ch := make(chan []byte, 1)
	ac.pending.Store(internalID, ch)
	defer ac.pending.Delete(internalID)

	if err := ac.writeJSON(ctx, rewritten); err != nil {
		return nil, err
	}
	select {
	case resp := <-ch:
		return restoreRPCID(resp, originalID)
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-ac.ctx.Done():
		return nil, ErrAgentOffline
	}
}

// newInternalID 16 bytes 随机 → 32 char hex。足够防碰撞,加前缀方便日志辨认。
func newInternalID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "syn-" + hex.EncodeToString(b[:]), nil
}
