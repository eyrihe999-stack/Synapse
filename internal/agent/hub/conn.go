// conn.go agentConn 的读循环 + 写入 + JSON-RPC id 改写。
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/coder/websocket"

	"github.com/eyrihe999-stack/Synapse/pkg/logger"
)

// readLoop 持续从 WS 读帧,按 JSON-RPC id 分发给 pending chan。
//
// 退出条件:
//   - WS 关闭(对端 close 或 read 失败)→ cancel ctx,所有 pending Invoke 返 ErrAgentOffline
//   - ctx 外部 cancel → 同上
//
// 失败策略:单条消息解析失败 log warn 继续读;WS 级错误退出。
func (c *agentConn) readLoop(log logger.LoggerInterface) {
	defer c.cancel()

	for {
		// 用 Invoke 的 context 控制读超时效果;Read 本身阻塞到数据或 ctx cancel。
		_, data, err := c.conn.Read(c.ctx)
		if err != nil {
			// 正常关闭(close 1000/1001)不 warn。
			if !isNormalClose(err) {
				log.WarnCtx(c.ctx, "hub: ws read error", map[string]any{
					"agent_id": c.agentID, "err": err.Error(),
				})
			}
			return
		}

		c.lastSeenUnix.Store(time.Now().UnixNano())

		id, ok := extractRPCID(data)
		if !ok {
			// 无 id:要么是 notification,要么是格式错。MVP 不处理 notification,log + drop。
			log.WarnCtx(c.ctx, "hub: drop message without id", map[string]any{
				"agent_id": c.agentID, "len": len(data),
			})
			continue
		}

		// 投递给等待的 Invoke
		if raw, loaded := c.pending.LoadAndDelete(id); loaded {
			ch := raw.(chan []byte)
			// 非阻塞 send:如果 Invoke 已 ctx cancel 先走了,channel buffer=1 吃掉也不 block
			select {
			case ch <- data:
			default:
				// 不太可能到这里(chan buf=1 且每个 id 只发一次),防御性 log
				log.WarnCtx(c.ctx, "hub: pending chan full, dropped", map[string]any{"agent_id": c.agentID, "id": id})
			}
			continue
		}
		// 没对应 pending —— agent 发了个我们不认识的 id。可能是迟到的响应(Invoke 已超时清掉),丢弃即可。
	}
}

// pingLoop 周期发 ping,对端不响应(ctx 被取消)时读循环会退出。
func (c *agentConn) pingLoop(log logger.LoggerInterface) {
	tick := time.NewTicker(pingInterval)
	defer tick.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-tick.C:
			pingCtx, cancel := context.WithTimeout(c.ctx, writeDeadline)
			err := c.conn.Ping(pingCtx)
			cancel()
			if err != nil {
				log.WarnCtx(c.ctx, "hub: ping failed, closing conn", map[string]any{
					"agent_id": c.agentID, "err": err.Error(),
				})
				// 触发 readLoop 退出
				_ = c.conn.Close(websocket.StatusPolicyViolation, "ping timeout")
				return
			}
		}
	}
}

// writeJSON 线程安全地写一帧 JSON(text frame)。
func (c *agentConn) writeJSON(ctx context.Context, data []byte) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	writeCtx, cancel := context.WithTimeout(ctx, writeDeadline)
	defer cancel()
	return c.conn.Write(writeCtx, websocket.MessageText, data)
}

// ─── JSON-RPC id 改写 ──────────────────────────────────────────────────────

// rewriteRPCID 在 rpcBody 里把顶层 id 字段替换成 newID。返回 (新 body, 原 id)。
// 原 id 可能是 string / number / null,此处保留原类型(用 RawMessage)以便 restoreRPCID 回填。
func rewriteRPCID(rpcBody []byte, newID string) ([]byte, json.RawMessage, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(rpcBody, &envelope); err != nil {
		return nil, nil, fmt.Errorf("parse rpc body: %w", err)
	}
	originalID := envelope["id"] // 可能是 nil,代表 notification
	newIDJSON, _ := json.Marshal(newID)
	envelope["id"] = newIDJSON
	out, err := json.Marshal(envelope)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal rpc body: %w", err)
	}
	return out, originalID, nil
}

// restoreRPCID 把 respBody 里的 id 改回 originalID。originalID 为空(notification 原样)则不动。
func restoreRPCID(respBody []byte, originalID json.RawMessage) ([]byte, error) {
	if len(originalID) == 0 {
		return respBody, nil
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return nil, fmt.Errorf("parse resp body: %w", err)
	}
	envelope["id"] = originalID
	out, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshal resp body: %w", err)
	}
	return out, nil
}

// extractRPCID 从 resp 里取出 id 字段(agent 发回的我们分配的 internalID)。
// 返 (id string, ok)。ok=false 代表无 id(notification 或格式错)。
func extractRPCID(data []byte) (string, bool) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return "", false
	}
	raw, present := envelope["id"]
	if !present {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		// id 不是 string(可能是 number)。我们 Invoke 时一定发 string,所以非 string 的肯定不是我们的 pending。
		return "", false
	}
	return s, true
}

// isNormalClose websocket 对端正常关闭不应 log 为错误。
func isNormalClose(err error) bool {
	if errors.Is(err, context.Canceled) {
		return true
	}
	var closeErr websocket.CloseError
	if errors.As(err, &closeErr) {
		switch closeErr.Code {
		case websocket.StatusNormalClosure, websocket.StatusGoingAway:
			return true
		}
	}
	return false
}
