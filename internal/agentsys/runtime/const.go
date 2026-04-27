// Package runtime 顶级系统 agent 事件处理主循环。
//
// 消费 synapse:channel:events.message.posted → 判断是否被 @ top-orchestrator →
// 预算检查 → 构造 ScopedServices → LLM tool-loop → 回复 / 派 task。
package runtime

import "time"

// ─── 行为参数 —— 全部硬编码(路径:internal/agentsys/runtime/const.go)───────
//
// 当前阶段不进 config,理由见 docs/collaboration-roadmap.md PR #6' 实施前锁定
// 表格"Tool-loop 上限"行。先跑一段时间观察真实 token 消耗分布再决定要不要进 yaml。

const (
	// MaxToolRounds 单次事件处理里 LLM tool-call loop 的最大轮数。
	// 每轮 = 1 次 LLM 调用 + 可能的 1 次 tool 执行。
	//
	// 8 轮:加 search_kb / get_kb_document 后,典型路径变成"听→列成员→搜 KB→
	// 看几篇文档→派任务→回用户",轻松吃 5-7 轮。8 轮给检索-推理留余量,真到 8
	// 仍没收敛说明 LLM 在绕圈,应该停下来回用户。
	MaxToolRounds = 8

	// RecentMessageWindow 组 prompt 时回灌多少条最近消息作为上下文。
	// 20 条 ≈ 几轮对话,平衡"有足够上下文"和"token 成本"。
	RecentMessageWindow = 20

	// ConsumerGroupName 顶级 orchestrator 的 consumer group 名(全局一个,不 per-org)。
	ConsumerGroupName = "top-orchestrator"

	// ChannelEventType 只响应这个事件类型,其它 event_type 直接 ACK 跳过。
	ChannelEventType = "message.posted"
)

// orchestratorShutdownTimeout 进程关闭时给 N 个 consumer 的最长退出窗口。
// 覆盖 XREADGROUP BLOCK 超时(liveBlockTimeout)+ 正在跑的 handleEvent(LLM tool-loop
// 每轮最多 60s,5 轮极端 5 min)—— 取 30s 作为"绝大多数场景够用"的中间值,真遇到
// LLM 长时卡住宁可放弃等待也不阻塞 process shutdown。
const orchestratorShutdownTimeout = 30 * time.Second

// idleConsumerSweepThreshold 启动时清理 group 下 idle 超过此阈值的残留 consumer。
// 容器重建后 hostname 变化导致旧 consumer 名永久留在 XINFO,需要主动删。
// 10 分钟:活跃 consumer BLOCK 5s + handleEvent(极端 ~5 min) idle 不会超过,残留通常
// 小时/天级,10 分钟足以区分;只清 pending=0 的,pending>0 留给人工排查。
const idleConsumerSweepThreshold = 10 * time.Minute
