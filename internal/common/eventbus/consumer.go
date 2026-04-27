package eventbus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"

	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
)

const (
	// consumeBatchSize 单次 XREADGROUP 最多拉多少条。太大单次处理慢,太小 Redis 往返多。
	consumeBatchSize = 10

	// liveBlockTimeout 实时消费阶段 BLOCK 时长。5s 兼顾响应性与 CPU 占用;
	// ctx 取消时最多等这么久就退出。
	liveBlockTimeout = 5 * time.Second

	// replayBlockTimeout PEL 重放阶段 BLOCK 时长。go-redis v8 里 Block=0 是
	// "永远阻塞",所以重放用一个短超时避免挂死。
	replayBlockTimeout = 100 * time.Millisecond

	// errorBackoff handler 返 error 后的退让,避免同一事件被 PEL 反复重试打崩 CPU。
	errorBackoff = 500 * time.Millisecond

	// redisErrorBackoff XREADGROUP 本身失败(Redis 抖动)时的退让。
	redisErrorBackoff = 1 * time.Second
)

// NewRedisConsumerGroup 构造基于 Redis Streams consumer group 的消费者。
// client 复用外部已连好的实例。log 必传。
func NewRedisConsumerGroup(client *redis.Client, log logger.LoggerInterface) ConsumerGroup {
	return &redisConsumerGroup{client: client, log: log}
}

type redisConsumerGroup struct {
	client *redis.Client
	log    logger.LoggerInterface
}

func (c *redisConsumerGroup) EnsureGroup(ctx context.Context, stream, group string) error {
	if stream == "" || group == "" {
		return fmt.Errorf("eventbus ensure group: stream and group required")
	}
	// XGROUP CREATE ... $ MKSTREAM:
	//   - $ : 只消费 group 创建后到达的新事件;历史事件不自动重放(不符合 consumer group 新建语义)
	//   - MKSTREAM : 目标 stream 不存在时自动建(XGROUP 默认要求 stream 已存在)
	err := c.client.XGroupCreateMkStream(ctx, stream, group, "$").Err()
	if err == nil {
		return nil
	}
	// BUSYGROUP Consumer Group name already exists:幂等成功
	if strings.Contains(err.Error(), "BUSYGROUP") {
		return nil
	}
	return fmt.Errorf("eventbus ensure group: %w", err)
}

func (c *redisConsumerGroup) Consume(ctx context.Context, stream, group, consumer string, handler HandlerFunc) error {
	if stream == "" || group == "" || consumer == "" {
		return fmt.Errorf("eventbus consume: stream / group / consumer required")
	}
	if handler == nil {
		return fmt.Errorf("eventbus consume: handler required")
	}

	// Phase 1:重放该 consumer 的 PEL —— 拿回自己未 XACK 的历史事件。
	// 典型场景:handler 跑到一半进程崩,重启后这里把挂起的事件重新派下去。
	c.log.InfoCtx(ctx, "eventbus: consumer pel replay start", map[string]any{
		"stream": stream, "group": group, "consumer": consumer,
	})
	if err := c.replayPending(ctx, stream, group, consumer, handler); err != nil {
		// replay 失败不致命,log 后直接进入 live 阶段 —— live 阶段同样会处理后续事件,
		// 历史 PEL 下次启动或 XAUTOCLAIM 介入时再清理。
		c.log.WarnCtx(ctx, "eventbus: pel replay failed; proceeding to live", map[string]any{
			"stream": stream, "group": group, "consumer": consumer, "err": err.Error(),
		})
	}

	// Phase 2:消费新事件。">" 表示"仅派从未送给任何 consumer 的新事件"。
	c.log.InfoCtx(ctx, "eventbus: consumer live start", map[string]any{
		"stream": stream, "group": group, "consumer": consumer,
	})
	for ctx.Err() == nil {
		streams, err := c.client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    group,
			Consumer: consumer,
			Streams:  []string{stream, ">"},
			Count:    consumeBatchSize,
			Block:    liveBlockTimeout,
		}).Result()
		if errors.Is(err, redis.Nil) {
			// BLOCK 到期无新消息,继续下一轮
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			c.log.WarnCtx(ctx, "eventbus: xreadgroup live failed", map[string]any{
				"stream": stream, "group": group, "err": err.Error(),
			})
			if !sleepOrDone(ctx, redisErrorBackoff) {
				return nil
			}
			continue
		}
		for _, s := range streams {
			for _, m := range s.Messages {
				c.handleOne(ctx, stream, group, m, handler)
			}
		}
	}
	return nil
}

func (c *redisConsumerGroup) DeleteConsumer(ctx context.Context, stream, group, consumer string) error {
	if stream == "" || group == "" || consumer == "" {
		return fmt.Errorf("eventbus delete consumer: stream/group/consumer required")
	}
	if _, err := c.client.XGroupDelConsumer(ctx, stream, group, consumer).Result(); err != nil {
		return fmt.Errorf("eventbus xgroup delconsumer %s/%s/%s: %w", stream, group, consumer, err)
	}
	return nil
}

func (c *redisConsumerGroup) SweepIdleConsumers(ctx context.Context, stream, group string, idleThreshold time.Duration) (int, int, error) {
	if stream == "" || group == "" {
		return 0, 0, fmt.Errorf("eventbus sweep: stream and group required")
	}
	// 不用 c.client.XInfoConsumers:go-redis v8 固定按 name/pending/idle 三对字段解析,
	// Redis 7.2+ 新增 inactive 字段后每条回复变成 8 元素,解析器会炸出
	// "got 8 elements ... wanted 6"。直接走 Do 拿 raw 数组自己按 key 挑字段,对
	// 未来 Redis 再加字段也向前兼容。
	raw, err := c.client.Do(ctx, "XINFO", "CONSUMERS", stream, group).Result()
	if err != nil {
		return 0, 0, fmt.Errorf("eventbus sweep: xinfo consumers: %w", err)
	}
	entries, ok := raw.([]interface{})
	if !ok {
		return 0, 0, fmt.Errorf("eventbus sweep: unexpected xinfo shape: %T", raw)
	}
	var deleted, skipped int
	for _, e := range entries {
		kv, ok := e.([]interface{})
		if !ok {
			skipped++
			continue
		}
		var (
			name      string
			pending   int64 = -1
			idleMs    int64 = -1
			haveField bool
		)
		for i := 0; i+1 < len(kv); i += 2 {
			k, _ := kv[i].(string)
			switch k {
			case "name":
				name, _ = kv[i+1].(string)
				haveField = true
			case "pending":
				if v, ok := kv[i+1].(int64); ok {
					pending = v
					haveField = true
				}
			case "idle":
				if v, ok := kv[i+1].(int64); ok {
					idleMs = v
					haveField = true
				}
			}
		}
		if !haveField || name == "" || pending < 0 || idleMs < 0 {
			skipped++
			continue
		}
		if pending > 0 || time.Duration(idleMs)*time.Millisecond < idleThreshold {
			skipped++
			continue
		}
		if _, err := c.client.XGroupDelConsumer(ctx, stream, group, name).Result(); err != nil {
			c.log.WarnCtx(ctx, "eventbus: delconsumer failed", map[string]any{
				"stream": stream, "group": group, "consumer": name, "err": err.Error(),
			})
			skipped++
			continue
		}
		deleted++
	}
	return deleted, skipped, nil
}

// replayPending 用 ID "0" 循环拉该 consumer 的 PEL 直到 drain 空。
// 每一轮从上次最后一条的 ID 之后继续,避免重复处理同一批。
func (c *redisConsumerGroup) replayPending(ctx context.Context, stream, group, consumer string, handler HandlerFunc) error {
	lastID := "0"
	for ctx.Err() == nil {
		streams, err := c.client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    group,
			Consumer: consumer,
			Streams:  []string{stream, lastID},
			Count:    consumeBatchSize,
			Block:    replayBlockTimeout,
		}).Result()
		if errors.Is(err, redis.Nil) {
			return nil // PEL 已空
		}
		if err != nil {
			return err
		}
		if len(streams) == 0 || len(streams[0].Messages) == 0 {
			return nil
		}
		for _, m := range streams[0].Messages {
			c.handleOne(ctx, stream, group, m, handler)
			lastID = m.ID
		}
	}
	return nil
}

// handleOne 单条事件处理:handler 返 nil 则 XACK;返 err 留 PEL + 短退让。
// 不吃 panic —— 调用方自己对 handler 加 recover 或在 handler 内处理。
func (c *redisConsumerGroup) handleOne(ctx context.Context, stream, group string, m redis.XMessage, handler HandlerFunc) {
	msg := Message{
		ID:     m.ID,
		Stream: stream,
		Fields: stringifyFields(m.Values),
	}
	if err := handler(ctx, msg); err != nil {
		c.log.WarnCtx(ctx, "eventbus: handler error; leaving in PEL", map[string]any{
			"stream": stream, "group": group, "msg_id": m.ID, "err": err.Error(),
		})
		// 短退让,避免同一事件 handler 持续失败时把 CPU 打满
		sleepOrDone(ctx, errorBackoff)
		return
	}
	if err := c.client.XAck(ctx, stream, group, m.ID).Err(); err != nil {
		c.log.WarnCtx(ctx, "eventbus: xack failed; event may be redelivered", map[string]any{
			"stream": stream, "group": group, "msg_id": m.ID, "err": err.Error(),
		})
	}
}

// stringifyFields 把 XADD 回读的 map[string]any 统一转成 map[string]string。
// 嵌套结构由 Publisher 侧 JSON marshal 成 string 再塞进 fields,所以这里对非 string
// 走 fmt.Sprint 是安全兜底(正常情况下只应遇到 string 和 nil)。
func stringifyFields(in map[string]any) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		switch s := v.(type) {
		case string:
			out[k] = s
		case nil:
			out[k] = ""
		default:
			out[k] = fmt.Sprint(v)
		}
	}
	return out
}

// sleepOrDone 睡 d 或 ctx 取消,取消时返 false。
func sleepOrDone(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
