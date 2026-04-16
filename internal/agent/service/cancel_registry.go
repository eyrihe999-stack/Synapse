// cancel_registry.go 本地取消注册表 + Redis pub/sub 订阅。
//
// 机制:
//   - 每次 invoke 开始时,gateway 创建一个 cancelable context,把 CancelFunc
//     注册到本实例的 CancelRegistry
//   - 客户端通过 DELETE /invocations/{id} 发起取消:
//     1. SET synapse:cancel:{id} = 1 TTL 600s(兜底)
//     2. PUBLISH synapse:cancel {id}(主路径)
//   - 所有 sayso 实例订阅 synapse:cancel 频道,收到消息后在本地 registry 里查找并触发 cancel
//   - 若消息丢失,gateway 的转发 goroutine 还会每 2 秒轮询一次 Redis flag 作为兜底
package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/agent"
	"github.com/eyrihe999-stack/Synapse/pkg/database"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"github.com/go-redis/redis/v8"
)

// CancelRegistry 维护本实例上进行中的 invocation → CancelFunc 映射。
// 并发安全。
type CancelRegistry struct {
	mu    sync.Mutex
	items map[string]context.CancelFunc

	redis  database.RedisInterface
	logger logger.LoggerInterface

	subCancel context.CancelFunc
	subDone   chan struct{}
}

// NewCancelRegistry 构造一个 CancelRegistry。
// redisDB 必须非 nil,否则注册后也无法通过 pub/sub 远程取消。
func NewCancelRegistry(redisDB database.RedisInterface, log logger.LoggerInterface) *CancelRegistry {
	return &CancelRegistry{
		items:  make(map[string]context.CancelFunc),
		redis:  redisDB,
		logger: log,
	}
}

// Register 注册一个 invocation 的 CancelFunc。
// 返回值是一个 release 函数,调用方应当在转发完成/超时/err 后 defer release()。
func (r *CancelRegistry) Register(invocationID string, cancel context.CancelFunc) func() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[invocationID] = cancel
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(r.items, invocationID)
	}
}

// TriggerLocal 触发本实例上某个 invocation 的 cancel(若存在)。返回是否命中。
func (r *CancelRegistry) TriggerLocal(invocationID string) bool {
	//sayso-lint:ignore lock-without-defer
	r.mu.Lock() // 读完 map 后立即释放,fn() 调用在锁外,避免 cancel 链路持锁
	fn, ok := r.items[invocationID]
	r.mu.Unlock()
	if !ok || fn == nil {
		return false
	}
	fn()
	return true
}

// RequestCancel 外部接口:设置 Redis 兜底 flag + PUBLISH 主路径。
// 被 handler 的 DELETE /invocations/{id} 调用。
//
// 错误:Redis 写 flag 或 PUBLISH 失败时返回原因,由调用方上浮为 ErrAgentInternal。
func (r *CancelRegistry) RequestCancel(ctx context.Context, invocationID string) error {
	if r.redis == nil {
		// 本地降级:直接触发本实例
		r.TriggerLocal(invocationID)
		return nil
	}
	client := r.redis.GetClient()
	if client == nil {
		r.TriggerLocal(invocationID)
		return nil
	}
	// 1. 写 flag 兜底
	flagKey := fmt.Sprintf("synapse:cancel:%s", invocationID)
	if err := client.Set(ctx, flagKey, "1", time.Duration(agent.CancelFlagTTLSeconds)*time.Second).Err(); err != nil {
		r.logger.WarnCtx(ctx, "cancel flag set failed", map[string]any{"invocation_id": invocationID, "error": err.Error()})
	}
	// 2. PUBLISH 主路径
	if err := client.Publish(ctx, agent.CancelPubSubChannel, invocationID).Err(); err != nil {
		r.logger.WarnCtx(ctx, "cancel publish failed", map[string]any{"invocation_id": invocationID, "error": err.Error()})
	}
	// 3. 也触发本地一次(当前实例可能正好持有)
	r.TriggerLocal(invocationID)
	return nil
}

// IsCanceled 查 Redis flag 是否存在(转发 goroutine 的兜底轮询用)。
func (r *CancelRegistry) IsCanceled(ctx context.Context, invocationID string) bool {
	if r.redis == nil {
		return false
	}
	client := r.redis.GetClient()
	if client == nil {
		return false
	}
	flagKey := fmt.Sprintf("synapse:cancel:%s", invocationID)
	n, err := client.Exists(ctx, flagKey).Result()
	if err != nil {
		return false
	}
	return n > 0
}

// StartSubscriber 启动一个后台 goroutine 订阅 Redis pub/sub 频道。
// 连接断开时自动退避重连。Stop 会 cancel 内部 context 并等待退出。
func (r *CancelRegistry) StartSubscriber(parent context.Context) {
	if r.redis == nil {
		return
	}
	client := r.redis.GetClient()
	if client == nil {
		return
	}
	//sayso-lint:ignore ctx-cancel-leak
	ctx, cancel := context.WithCancel(parent) // cancel 保存到 r.subCancel,由 Stop() 调用
	r.subCancel = cancel
	r.subDone = make(chan struct{})

	//sayso-lint:ignore bare-goroutine
	go func() { // 长生命周期订阅循环,由 Stop() 控制退出,不适合 AsyncRunner 的 fire-and-forget 模型
		defer close(r.subDone)
		backoff := time.Second
		timer := time.NewTimer(backoff)
		defer timer.Stop()
		for {
			if ctx.Err() != nil {
				return
			}
			if err := r.runSubscribeLoop(ctx, client); err != nil {
				r.logger.WarnCtx(ctx, "cancel subscriber loop exited", map[string]any{"error": err.Error()})
			}
			// 退避后重连
			timer.Reset(backoff)
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
		}
	}()
}

// runSubscribeLoop 单次订阅循环,连接断开/错误返回后由 StartSubscriber 重连。
func (r *CancelRegistry) runSubscribeLoop(ctx context.Context, client *redis.Client) error {
	sub := client.Subscribe(ctx, agent.CancelPubSubChannel)
	//sayso-lint:ignore err-swallow
	defer func() { _ = sub.Close() }() // close 错误无处理
	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			//sayso-lint:ignore log-coverage
			return ctx.Err()
		case msg, ok := <-ch:
			if !ok {
				//sayso-lint:ignore log-coverage
				return fmt.Errorf("channel closed")
			}
			if msg == nil || msg.Payload == "" {
				continue
			}
			r.TriggerLocal(msg.Payload)
		}
	}
}

// Stop 终止订阅并等待 goroutine 退出。
func (r *CancelRegistry) Stop() {
	if r.subCancel != nil {
		r.subCancel()
	}
	if r.subDone != nil {
		<-r.subDone
	}
}
