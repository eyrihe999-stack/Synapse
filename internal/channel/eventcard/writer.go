// Package eventcard 订阅 channel / task 事件流,把业务事件转成 channel_messages
// 表里的 `kind=system_event` 消息卡片。由 cmd/synapse 常驻启动。
//
// 工作方式:
//   - 两个 goroutine 各订一个 stream(synapse:channel:events / synapse:task:events),
//     同一 consumer group `channel-event-card-writer`
//   - handler 里按 event_type 分支生成 body JSON,调 MessageService.PostSystemEvent
//   - source_event_id = Redis stream message ID,UNIQUE 列做幂等,重放不重复卡片
//   - message.posted 事件**直接 ACK 跳过**(避免"系统写入消息 → 触发 message.posted
//     → consumer 又写系统消息" 的死循环)
//
// 失败处理:
//   - channel 不存在 / org 不匹配 / body 序列化失败 → ACK 丢弃 + warn log
//   - PostSystemEvent 返错(DB 异常)→ 不 ACK,PEL 重放
//   - consumer Consume 退出 → 上游 errCh 反馈,main.go 决定 graceful shutdown
package eventcard

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	chansvc "github.com/eyrihe999-stack/Synapse/internal/channel/service"
	"github.com/eyrihe999-stack/Synapse/internal/common/eventbus"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
)

// ConsumerGroupName 同组名订阅 channel / task 两个 stream。跨部署实例均衡消费。
const ConsumerGroupName = "channel-event-card-writer"

// idleConsumerSweepThreshold 启动时清理本 group 下 idle 超过此阈值的残留 consumer。
// 容器重建后 hostname 变化,旧 consumer 名永远留在 XINFO,需要主动清。
// 语义同 agentsys/runtime 里的同名常量:pending=0 + idle ≥ 10min 才清。
const idleConsumerSweepThreshold = 10 * time.Minute

// Config 构造 Writer 所需配置。
type Config struct {
	// ChannelStream synapse:channel:events(和 message service 发布的 key 对齐)
	ChannelStream string
	// TaskStream synapse:task:events(和 task service 发布的 key 对齐)
	TaskStream string
	// FallbackAuthorPrincipalID 事件里拿不到 actor_principal_id 时的兜底(通常 = 顶级
	// 系统 agent pid)。避免写入 0 触发 FK 失败。
	FallbackAuthorPrincipalID uint64
}

// MessagePoster Writer 依赖的最小 message service 接口 —— 只暴露 PostSystemEvent。
// 这样 eventcard 模块不需要 import 整个 MessageService 接口。
type MessagePoster interface {
	PostSystemEvent(ctx context.Context, channelID, authorPrincipalID uint64, bodyJSON, sourceEventID string) (*chansvc.PostedMessage, error)
}

// Writer 事件卡片写入器。
type Writer struct {
	cfg          Config
	consumer     eventbus.ConsumerGroup
	msg          MessagePoster
	consumerName string
	logger       logger.LoggerInterface
}

// New 构造 Writer。consumer / msg / logger 必填。
func New(cfg Config, consumer eventbus.ConsumerGroup, msg MessagePoster, log logger.LoggerInterface) *Writer {
	// 同 agentsys 的 consumer 命名策略:SYNAPSE_INSTANCE_ID > hostname > 静态 fallback。
	// 同实例重启复用同名 consumer,接管原 PEL 继续处理,避免 group 里累积 stale。
	consumerName := os.Getenv("SYNAPSE_INSTANCE_ID")
	if consumerName == "" {
		host, _ := os.Hostname() //sayso-lint:ignore err-swallow
		consumerName = host
	}
	if consumerName == "" {
		consumerName = "eventcard"
	}
	return &Writer{
		cfg:          cfg,
		consumer:     consumer,
		msg:          msg,
		consumerName: consumerName,
		logger:       log,
	}
}

// Run 阻塞启动 —— 起 2 个 goroutine 各订一个 stream,任一返错即 return。
// ctx 取消 → 两个 Consume 都返回。
func (w *Writer) Run(ctx context.Context) error {
	// 确保 consumer group 存在(幂等;空 stream 也建得出来)
	if err := w.consumer.EnsureGroup(ctx, w.cfg.ChannelStream, ConsumerGroupName); err != nil {
		return fmt.Errorf("ensure channel consumer group: %w", err)
	}
	if err := w.consumer.EnsureGroup(ctx, w.cfg.TaskStream, ConsumerGroupName); err != nil {
		return fmt.Errorf("ensure task consumer group: %w", err)
	}

	// 清理容器重建留下的残留 consumer;同一 group 在两个 stream 各有独立 consumer 列表,
	// 都扫一遍。失败不致命,继续启动。
	for _, stream := range []string{w.cfg.ChannelStream, w.cfg.TaskStream} {
		deleted, skipped, err := w.consumer.SweepIdleConsumers(ctx, stream, ConsumerGroupName, idleConsumerSweepThreshold)
		if err != nil {
			w.logger.WarnCtx(ctx, "eventcard: sweep idle consumers failed", map[string]any{
				"stream": stream, "group": ConsumerGroupName, "err": err.Error(),
			})
			continue
		}
		if deleted > 0 || skipped > 0 {
			w.logger.InfoCtx(ctx, "eventcard: swept idle consumers", map[string]any{
				"stream":         stream,
				"group":          ConsumerGroupName,
				"deleted":        deleted,
				"skipped":        skipped,
				"idle_threshold": idleConsumerSweepThreshold.String(),
			})
		}
	}

	w.logger.InfoCtx(ctx, "eventcard: writer starting", map[string]any{
		"channel_stream": w.cfg.ChannelStream,
		"task_stream":    w.cfg.TaskStream,
		"group":          ConsumerGroupName,
		"consumer":       w.consumerName,
	})

	var wg sync.WaitGroup
	errCh := make(chan error, 2)

	chanConsumer := w.consumerName + "-chan"
	taskConsumer := w.consumerName + "-task"

	wg.Add(1)
	go func() {
		defer wg.Done()
		// graceful shutdown:Consume 退出后从 group 摘掉自己,避免留 stale consumer。
		defer w.cleanupConsumer(w.cfg.ChannelStream, chanConsumer)
		if err := w.consumer.Consume(ctx, w.cfg.ChannelStream, ConsumerGroupName,
			chanConsumer, w.handleEvent); err != nil {
			errCh <- fmt.Errorf("channel stream consume: %w", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer w.cleanupConsumer(w.cfg.TaskStream, taskConsumer)
		if err := w.consumer.Consume(ctx, w.cfg.TaskStream, ConsumerGroupName,
			taskConsumer, w.handleEvent); err != nil {
			errCh <- fmt.Errorf("task stream consume: %w", err)
		}
	}()

	// 等任一个失败(或 ctx 取消都正常返回);shutdown 时两个 goroutine 都会退出
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

// cleanupConsumer 实例关闭时主动从 group 摘掉指定 consumer,防止 stale 累积。
// 用独立 ctx 因为父 ctx 已被取消,5s 超时 best-effort,失败只 warn。
func (w *Writer) cleanupConsumer(stream, name string) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.consumer.DeleteConsumer(cleanupCtx, stream, ConsumerGroupName, name); err != nil {
		w.logger.WarnCtx(cleanupCtx, "eventcard: cleanup delete consumer failed", map[string]any{
			"stream": stream, "group": ConsumerGroupName, "consumer": name, "err": err.Error(),
		})
		return
	}
	w.logger.InfoCtx(cleanupCtx, "eventcard: consumer deleted on shutdown", map[string]any{
		"stream": stream, "group": ConsumerGroupName, "consumer": name,
	})
}

// handleEvent 消费端业务回调。返 nil → ACK;返 error → PEL 重放。
func (w *Writer) handleEvent(ctx context.Context, msg eventbus.Message) error {
	eventType := msg.Fields["event_type"]

	// 跳过消息自身产生的事件,避免死循环("系统消息 → publishMessagePosted 关闭" 已经
	// 在 PostSystemEvent 里没调,这里多一道闸)
	if eventType == "" || eventType == "message.posted" {
		return nil
	}

	channelID, err := parseUint64(msg.Fields["channel_id"])
	if err != nil || channelID == 0 {
		// 没 channel_id 的事件(理论不应该出现)—— 丢弃 ACK
		w.logger.WarnCtx(ctx, "eventcard: event missing channel_id, skipping", map[string]any{
			"event_type": eventType, "stream_id": msg.ID,
		})
		return nil
	}

	// actor 解析:优先 actor_principal_id;fallback 到 created_by_principal_id;再 fallback 到 cfg
	actorPID := parseUint64Default(msg.Fields["actor_principal_id"], 0)
	if actorPID == 0 {
		actorPID = parseUint64Default(msg.Fields["created_by_principal_id"], 0)
	}
	if actorPID == 0 {
		actorPID = w.cfg.FallbackAuthorPrincipalID
	}
	if actorPID == 0 {
		// 仍为 0 只能跳过(DB FK 拒)
		w.logger.WarnCtx(ctx, "eventcard: unable to resolve actor, skipping", map[string]any{
			"event_type": eventType, "stream_id": msg.ID,
		})
		return nil
	}

	// 组 body JSON:所有 stream fields 全部塞进 detail(除了 event_type / channel_id
	// / actor_principal_id 这些已在顶层暴露的)。前端按 event_type 分支渲染。
	body := buildCardBody(eventType, actorPID, msg.Fields)
	raw, err := json.Marshal(body)
	if err != nil {
		w.logger.WarnCtx(ctx, "eventcard: marshal body failed, skipping", map[string]any{
			"event_type": eventType, "err": err.Error(),
		})
		return nil
	}

	if _, err := w.msg.PostSystemEvent(ctx, channelID, actorPID, string(raw), msg.ID); err != nil {
		// channel 不存在等业务错误 → 丢弃 ACK(消息卡片不是关键路径,漏一条比卡死 consumer 好)
		w.logger.WarnCtx(ctx, "eventcard: post system_event failed, skipping", map[string]any{
			"event_type": eventType, "channel_id": channelID, "err": err.Error(),
		})
		return nil
	}

	w.logger.DebugCtx(ctx, "eventcard: wrote system_event", map[string]any{
		"event_type": eventType, "channel_id": channelID, "stream_id": msg.ID,
	})
	return nil
}

// buildCardBody 把 stream fields 组装成 system_event 消息 body JSON。
//
// 约定:
//
//	{
//	  "event_type": "task.created",
//	  "actor_principal_id": 1,
//	  "detail": { ...其它 stream fields... }
//	}
//
// detail 里尽量保留所有原字段,前端按 event_type 自己挑需要的渲染;unknown event_type
// 前端降级显示"未知事件"。
func buildCardBody(eventType string, actorPID uint64, fields map[string]string) map[string]any {
	detail := make(map[string]any, len(fields))
	for k, v := range fields {
		switch k {
		case "event_type", "channel_id", "actor_principal_id", "published_at":
			continue // 顶层 / 元数据字段不重复塞 detail
		default:
			detail[k] = v
		}
	}
	return map[string]any{
		"event_type":         eventType,
		"actor_principal_id": actorPID,
		"detail":             detail,
	}
}

func parseUint64(s string) (uint64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	return strconv.ParseUint(s, 10, 64)
}

func parseUint64Default(s string, def uint64) uint64 {
	v, err := parseUint64(s)
	if err != nil {
		return def
	}
	return v
}
