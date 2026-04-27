// Package eventbus 基于 Redis Streams 的轻量事件总线。
//
// 用途:跨模块事件通道 —— asyncjob 终态完成事件、workflow 内部跃迁事件。
// 物理后端:Redis Streams + consumer group,复用已连上的 *redis.Client(不另建连接)。
//
// ══════════════════════════════════════════════════════════════════════════
//
//	投递语义 (at-least-once):
//	  - Publisher 一次 XADD 失败不会自动重试,返 error 给调用方决定降级
//	  - Consumer 通过 consumer group 消费,handler 返 nil → XACK;返 err → 不 XACK,
//	    事件留在 consumer 的 PEL,下次 Consume 启动时用 "0" 重放
//	  - 业务层 handler 必须幂等(如 UPDATE ... WHERE status='running',RowsAffected=0
//	    视为"已被推过"跳过),因为同一事件可能被投递多次
//	  - 不做自动 retry / 死信:retry 语义留在上游(workflow 层);PEL 长期堆积
//	    由运维侧监控 XPENDING / XINFO GROUPS
//
//	设计决策:
//	  - 为何不走 Postgres LISTEN/NOTIFY:主库 MySQL 没有;Postgres 仅给 pgvector 用
//	    且 Host 可空,不能当事件通道
//	  - 为何不用进程内 Go channel:单进程假设卡多副本部署;Phase 2 channel 消息 /
//	    inbox 也要持久化事件底座,一次建好复用
//	  - 为何选 Streams 不选 Pub/Sub:Pub/Sub fire-and-forget,订阅者不在线即丢;
//	    Streams 持久化 + consumer group PEL + XACK,宕机重启可重放
//	  - 详见 docs/collaboration-design.md §3.7.4 决策 (d)
//
// ══════════════════════════════════════════════════════════════════════════
package eventbus

import (
	"context"
	"time"
)

// Message 消费端拿到的一条事件。
type Message struct {
	// ID Redis stream 分配的 ID(形如 "1713865200000-0"),用于 XACK 定位。
	ID string
	// Stream 源 stream key。多 stream 共享同一 handler 时用于分流。
	Stream string
	// Fields XADD 写入的 key/value。嵌套结构由 Publisher 侧 JSON marshal 后放进来,
	// 消费端自己 unmarshal —— eventbus 不做结构化约束。
	Fields map[string]string
}

// HandlerFunc 消费端业务回调。
//
// 语义:
//   - 返 nil:框架 XACK,事件从 PEL 移除
//   - 返 error:框架不 XACK,事件留在该 consumer 的 PEL,重启后自动重放
//   - 不做自动重试:业务层决定是"真失败需要人工介入"还是"转瞬即逝的系统抖动";
//     前者运维看 XPENDING 告警,后者下次 Consume 启动会自己重放
type HandlerFunc func(ctx context.Context, msg Message) error

// Publisher 事件发布接口。
//
// 实现者:NewRedisPublisher(见 publisher.go)。
// 调用者:asyncjob.service(终态后发 completion 事件);未来 workflow 引擎(发内部跃迁事件)。
type Publisher interface {
	// Publish 向 stream 追加一条事件,返回 Redis 分配的 ID。
	//
	// fields 任意 map[string]any,框架把非 string 值走 fmt.Sprint 转 string;
	// 嵌套结构(如 map / struct)调用方自行 json.Marshal 后塞入,避免信息丢失。
	//
	// 可能返回的错误:
	//   - stream 或 fields 为空 → 参数错误
	//   - Redis 连接 / 命令错误 → 原样包装后返回(调用方记 log 降级,不回滚 DB)
	Publish(ctx context.Context, stream string, fields map[string]any) (id string, err error)
}

// ConsumerGroup 消费者接口。
//
// 单个 Consume 调用是同步阻塞的长循环,一般起独立 goroutine 跑;多 consumer
// 名字不同时可并行(Redis 按 group 做消息分发)。
//
// 实现者:NewRedisConsumerGroup(见 consumer.go)。
// 调用者:PR #5 的 workflow engine(本 PR 只交付接口 + 实现,不在 cmd 里起 consumer)。
type ConsumerGroup interface {
	// EnsureGroup 幂等创建 consumer group(XGROUP CREATE MKSTREAM ... $)。
	// group 已存在(BUSYGROUP)视作成功。"$" 意味着只消费"创建之后"到达的事件,
	// 不重放历史 —— 新 group 首次启动前的历史事件靠 DB reaper 对账兜底。
	EnsureGroup(ctx context.Context, stream, group string) error

	// Consume 同步消费,返回时意味着 ctx 已取消或永久性错误。生命周期:
	//   1. 先用 "0" 重放该 consumer 的 PEL(未 XACK 事件)—— 覆盖"handler 跑到一半进程崩"
	//   2. 再切 ">" 消费 group 新事件;XREADGROUP BLOCK 5s,ctx 取消即返
	//
	// consumer 名建议用 hostname 等稳定值(不要用 pid),否则重启后 PEL 里的
	// 未 ACK 事件没人认领,要等 XAUTOCLAIM 超时转移(MVP 未做)。
	Consume(ctx context.Context, stream, group, consumer string, handler HandlerFunc) error

	// DeleteConsumer 从 group 里摘掉指定 consumer。一般用于实例 graceful shutdown 时
	// 主动清理自己,避免在 group 里留尸体。
	//
	// 行为:不管 consumer 当前 pending 状态,直接 XGROUP DELCONSUMER。如果 PEL 不为空,
	// 那些事件会**丢失重投机会**(下次 Consume 启动 PEL 重放就拿不到了)。所以调用方应该
	// 在确认"我处理完了"或者"接受丢失"的语义下才调。
	//
	// 不存在的 consumer 调也不报错(Redis 行为)。
	DeleteConsumer(ctx context.Context, stream, group, consumer string) error

	// SweepIdleConsumers 清理 group 下 pending=0 且 idle >= idleThreshold 的 consumer。
	// 用途:容器重建后 hostname 变化,旧 consumer 不会自动从 group 移除,XINFO CONSUMERS
	// 会越积越多。启动时调一次扫一遍即可。
	//
	// 安全边界:
	//   - 只删 pending=0 的 consumer。pending>0 意味着有未 ACK 事件,XGROUP DELCONSUMER
	//     会把这些事件的 PEL 一并丢弃,此场景应先 XAUTOCLAIM 转给活跃 consumer(未实现)。
	//   - idleThreshold 建议 ≥10min:活跃 consumer BLOCK 命中间隙 idle 也不会超过 handler
	//     跑完时间(分钟级),10min 足以把"活跃"和"残留(小时/天级)"区分开。
	//
	// 返回:deleted=实际删除数;skipped=匹配但跳过(pending>0 / idle 不够 / 单个删除失败)数。
	// Redis 抖动 / 单条删除失败计入 skipped 不致命;err 仅在枚举 consumers 失败时返回。
	SweepIdleConsumers(ctx context.Context, stream, group string, idleThreshold time.Duration) (deleted int, skipped int, err error)
}
