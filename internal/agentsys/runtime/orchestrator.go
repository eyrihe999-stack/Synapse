package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/agents"
	agentsrepo "github.com/eyrihe999-stack/Synapse/internal/agents/repository"
	"github.com/eyrihe999-stack/Synapse/internal/agentsys/prompts"
	"github.com/eyrihe999-stack/Synapse/internal/agentsys/repository"
	"github.com/eyrihe999-stack/Synapse/internal/agentsys/scoped"
	"github.com/eyrihe999-stack/Synapse/internal/common/async"
	"github.com/eyrihe999-stack/Synapse/internal/common/eventbus"
	"github.com/eyrihe999-stack/Synapse/internal/common/llm"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
)

// Config orchestrator 构造参数。
//
// PR-B B2 引入"runtime 角色"参数化:同一个 Orchestrator struct 既可以作为
// top-orchestrator(全 channel 监听)跑,也可以作为 Project Architect(只 console
// channel 监听)跑。差别全在 Config 字段。
type Config struct {
	// ChannelStream eventbus 里 channel 事件的 stream key(形如 synapse:channel:events)。
	ChannelStream string

	// DailyBudgetPerOrgUSD 每 org 每日 LLM 预算上限。<=0 视为不限。
	DailyBudgetPerOrgUSD float64

	// Concurrency 进程内并发消费事件的 consumer 数(同一 Redis Streams consumer group
	// 下的 N 个 consumer,name=${hostname}-0/1/...)。<=1 走单路径串行(原行为)。
	// Config 层已夹到 [1, 32],这里信任传入值。
	Concurrency int

	// AgentID 该 runtime 代表的 agent 在 agents 表里的 agent_id(NewOrchestrator
	// 用它反查 principal_id)。空 fallback 到 agents.TopOrchestratorAgentID 兼容老调用方。
	AgentID string

	// ConsumerGroupName Redis Streams consumer group 名;同 stream 下不同 group 互不
	// 竞争事件,**这是支持 top-orch + Architect 同时跑的关键**。空 fallback 到
	// 老常量 ConsumerGroupName 兼容老调用方。
	ConsumerGroupName string

	// SystemPrompt LLM 的 system prompt 原文。空 fallback 到 prompts.TopOrchestrator。
	SystemPrompt string

	// AgentDisplayName log / 调试可读名,例如 "top-orchestrator" / "project-architect"。
	AgentDisplayName string

	// ChannelKindFilter 空 = 任意 channel;非空 = 只对这些 kind 的 channel 响应。
	// Architect 用 ["project_console"];top-orch 留空(任意 channel)。
	ChannelKindFilter []string

	// EnableProjectPreScan Architect 专用硬化:每次 LLM 调用前,在 system prompt 后
	// 自动追加"项目预扫描"段(get_project_roadmap + list_project_kb_refs + 各 doc 全文 +
	// list_org_members)。LLM 拿到上下文时**已经看到** KB 内容 + 现有结构 + 成员名单,
	// 物理上不可能跳过这些只读步骤。top-orchestrator 关闭即可。
	EnableProjectPreScan bool
}

// Orchestrator 顶级系统 agent 的运行时。
//
// 生命周期:
//  1. NewOrchestrator:查 DB 拿 top-orchestrator 的 principal_id,缓存在结构体
//  2. Run:常驻 goroutine;EnsureGroup → Consume(自动 PEL 重放 + 实时 XREADGROUP)
//  3. ctx 取消 → Consume 返回 → 进程 graceful shutdown
//
// 一个进程一个 Orchestrator;多副本部署时通过 Redis consumer group 做分发
// (同一 group 下多个 consumer 会被 Redis 均衡分派事件,无需应用层协调)。
type Orchestrator struct {
	cfg              Config
	consumer         eventbus.ConsumerGroup
	llm              llm.Chat
	scopedDeps       scoped.Deps
	auditRepo        repository.AuditRepo
	usageRepo        repository.UsageRepo
	db               *gorm.DB // 仅用于 cfg.ChannelKindFilter 非空时查 channels.kind 过滤
	logger           logger.LoggerInterface
	agentPrincipalID uint64 // 启动时一次性查出,不变(top-orch 或 Architect 的 principal_id)
	consumerName     string // XREADGROUP consumer 名;用 hostname 保证 PEL 认领稳定
	groupName        string // 解析后的 consumer group(cfg 空时 fallback)
	systemPrompt     string // 解析后的 LLM system prompt(cfg 空时 fallback)
}

// NewOrchestrator 构造 Orchestrator。
//
// 失败场景:
//   - 查不到 cfg.AgentID 对应的 agent 行(seed 没跑)
//
// db 仅在 cfg.ChannelKindFilter 非空时使用(Architect 走 console-only 过滤);
// top-orch 路径传 nil 也行。
//
// 其它依赖(llm / scoped / repos)都已由调用方构造好,这里只收集。
func NewOrchestrator(
	ctx context.Context,
	cfg Config,
	consumer eventbus.ConsumerGroup,
	llmClient llm.Chat,
	agentRepo agentsrepo.Repository,
	scopedDeps scoped.Deps,
	auditRepo repository.AuditRepo,
	usageRepo repository.UsageRepo,
	db *gorm.DB,
	log logger.LoggerInterface,
) (*Orchestrator, error) {
	agentID := cfg.AgentID
	if agentID == "" {
		agentID = agents.TopOrchestratorAgentID // 兼容老调用方
	}
	agentRow, err := agentRepo.FindByAgentID(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("locate agent %s: %w", agentID, err)
	}
	if agentRow.PrincipalID == 0 {
		return nil, fmt.Errorf("agent %s principal_id is 0 — migration not applied?", agentID)
	}
	groupName := cfg.ConsumerGroupName
	if groupName == "" {
		groupName = ConsumerGroupName
	}
	systemPrompt := cfg.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = prompts.TopOrchestrator
	}

	// consumer name 优先 SYNAPSE_INSTANCE_ID env(同实例重启复用同名,接管原 PEL),
	// fallback hostname,再 fallback 静态字符串。INSTANCE_ID 给容器化部署用 ——
	// 容器 hostname 每次重建都变,但 INSTANCE_ID 可绑死一个实例语义(比如 statefulset 序号)。
	consumerName := os.Getenv("SYNAPSE_INSTANCE_ID")
	if consumerName == "" {
		host, _ := os.Hostname() //sayso-lint:ignore err-swallow
		consumerName = host
	}
	if consumerName == "" {
		consumerName = "orchestrator"
	}

	return &Orchestrator{
		cfg:              cfg,
		consumer:         consumer,
		llm:              llmClient,
		scopedDeps:       scopedDeps,
		auditRepo:        auditRepo,
		usageRepo:        usageRepo,
		db:               db,
		logger:           log,
		agentPrincipalID: agentRow.PrincipalID,
		consumerName:     consumerName,
		groupName:        groupName,
		systemPrompt:     systemPrompt,
	}, nil
}

// passesChannelKindFilter 当 cfg.ChannelKindFilter 非空时查 channels.kind,
// 不在 filter 列表则返 false(handleEvent 跳过此事件)。
//
// db 缺失或查询失败按"通过"处理 —— 宁可让 LLM 多算一次,也不要因为基础设施问题
// 让 Architect 完全失声。
func (o *Orchestrator) passesChannelKindFilter(ctx context.Context, channelID uint64) bool {
	if len(o.cfg.ChannelKindFilter) == 0 {
		return true
	}
	if o.db == nil {
		return true
	}
	var kind string
	err := o.db.WithContext(ctx).Raw(
		"SELECT kind FROM channels WHERE id = ?", channelID,
	).Scan(&kind).Error
	if err != nil {
		o.logger.WarnCtx(ctx, "agentsys: channel kind lookup failed, allowing", map[string]any{
			"channel_id": channelID, "err": err.Error(),
		})
		return true
	}
	for _, allowed := range o.cfg.ChannelKindFilter {
		if kind == allowed {
			return true
		}
	}
	return false
}

// AgentPrincipalID 暴露给测试 / 外部校验;业务代码不应该读它。
func (o *Orchestrator) AgentPrincipalID() uint64 { return o.agentPrincipalID }

// TopOrchestratorPrincipalID 老 API 别名,保留兼容(eventcard.Writer / main.go 用)。
func (o *Orchestrator) TopOrchestratorPrincipalID() uint64 { return o.agentPrincipalID }

// Run 同步阻塞消费。ctx 取消即返 nil;底层 Consume 错误(连接 Redis 失败等)原样返回。
// 调用方一般起一个独立 goroutine:
//
//	go func() {
//	  if err := orch.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
//	    log.Error("orchestrator exited", err)
//	  }
//	}()
//
// 并发模型:cfg.Concurrency > 1 时,起 N 个 goroutine,每个以独立 consumer name
// (${consumerName}-0/1/...)加入**同一** consumer group。Redis Streams 按 group
// 内 consumer 自动轮派事件 —— 进程内天然 N 路并行。每个 goroutine 维护自己的
// PEL,XACK 互不干扰。注意:不做 per-channel 串行化,同 channel 两条几乎同时
// 到达的 @ 事件**可能**并行处理(LLM prompt 里各自看到的 history 是读 DB 那刻
// 的快照,两个回复可能彼此看不见)。生产中影响概率低,真出问题再加 mutex。
func (o *Orchestrator) Run(ctx context.Context) error {
	if err := o.consumer.EnsureGroup(ctx, o.cfg.ChannelStream, o.groupName); err != nil {
		return fmt.Errorf("ensure consumer group: %w", err)
	}

	if deleted, skipped, err := o.consumer.SweepIdleConsumers(
		ctx, o.cfg.ChannelStream, o.groupName, idleConsumerSweepThreshold,
	); err != nil {
		o.logger.WarnCtx(ctx, "agentsys: sweep idle consumers failed", map[string]any{
			"stream": o.cfg.ChannelStream, "group": o.groupName, "err": err.Error(),
		})
	} else if deleted > 0 || skipped > 0 {
		o.logger.InfoCtx(ctx, "agentsys: swept idle consumers", map[string]any{
			"stream":         o.cfg.ChannelStream,
			"group":          o.groupName,
			"deleted":        deleted,
			"skipped":        skipped,
			"idle_threshold": idleConsumerSweepThreshold.String(),
		})
	}

	n := o.cfg.Concurrency
	if n <= 1 {
		o.logger.InfoCtx(ctx, "agentsys: top-orchestrator runtime starting", map[string]any{
			"stream":               o.cfg.ChannelStream,
			"group":                o.groupName,
			"consumer":             o.consumerName,
			"top_orchestrator_pid": o.agentPrincipalID,
			"concurrency":          1,
		})
		// graceful shutdown:Consume 因 ctx 取消退出后,主动从 group 摘掉自己
		// (避免在 group 里留尸体)。用独立 ctx 因为父 ctx 已经 cancel 了。
		defer o.cleanupConsumer(o.consumerName)
		return o.consumer.Consume(ctx, o.cfg.ChannelStream, o.groupName, o.consumerName, o.handleEvent)
	}

	o.logger.InfoCtx(ctx, "agentsys: top-orchestrator runtime starting", map[string]any{
		"stream":               o.cfg.ChannelStream,
		"group":                o.groupName,
		"consumer_prefix":      o.consumerName,
		"top_orchestrator_pid": o.agentPrincipalID,
		"concurrency":          n,
	})

	// AsyncRunner 做 goroutine 生命周期管理 + panic 兜底 + 有超时的 graceful shutdown。
	// 用它有点"借鸡下蛋":语义上它是给 fire-and-forget 任务控并发的,这里被借来当"N 个常驻
	// worker 启动器" —— N 个 slot 被 N 个 consumer 永久占满,acquire/reject 能力用不上,
	// 但 panic recovery + shutdown 等待这两样对长跑 consumer 同样有价值。
	runner := async.NewAsyncRunner("agentsys-orchestrator", int64(n), o.logger)

	// 每个 consumer 自己决定退出 —— Consume 循环里观察 rctx.Done()(runner 内部 ctx),
	// runner.Shutdown 触发 cancel 后 XREADGROUP BLOCK 立即返回,Consume 退出,WaitGroup 清零。
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		consumerName := fmt.Sprintf("%s-%d", o.consumerName, i)
		if err := runner.Go(ctx, consumerName, func(rctx context.Context) {
			defer o.cleanupConsumer(consumerName)
			if err := o.consumer.Consume(rctx, o.cfg.ChannelStream, o.groupName, consumerName, o.handleEvent); err != nil {
				select {
				case errCh <- err:
				default:
				}
			}
		}); err != nil {
			// 理论上不可能:我们只提交 N 次且 runner 容量就是 N,acquire 一次成功一次。
			// 若真落到这里(runner 已 Shutdown / ctx 超时)视为启动失败。
			return fmt.Errorf("spawn consumer %s: %w", consumerName, err)
		}
	}

	// 主协程在这里挂起直到 orchestrator ctx 取消。
	<-ctx.Done()

	// Graceful shutdown:给 consumer 最多 orchestratorShutdownTimeout 时间退出。
	// 超时后 runner 放弃等待,goroutine 可能仍在跑,但 Run 返回让上层继续 shutdown 流程。
	shutdownCtx, cancel := context.WithTimeout(context.Background(), orchestratorShutdownTimeout)
	defer cancel()
	if err := runner.Shutdown(shutdownCtx); err != nil {
		o.logger.WarnCtx(ctx, "agentsys: orchestrator shutdown timed out", map[string]any{
			"err": err.Error(),
		})
	}

	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

// cleanupConsumer 实例关闭时主动从 consumer group 里摘掉指定 consumer,避免在
// group 里留"尸体"(stale consumer)。给 5s 超时:父 ctx 已被取消,这是个 best-effort,
// 失败只 warn 不影响 shutdown 流程。
//
// 用独立 context.Background 而不是父 ctx,因为 Run 退出场景里父 ctx 已经被 cancel,
// 用它跑 Redis 命令会立刻返 ctx canceled。
func (o *Orchestrator) cleanupConsumer(name string) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := o.consumer.DeleteConsumer(cleanupCtx, o.cfg.ChannelStream, o.groupName, name); err != nil {
		o.logger.WarnCtx(cleanupCtx, "agentsys: cleanup delete consumer failed", map[string]any{
			"stream": o.cfg.ChannelStream, "group": o.groupName, "consumer": name, "err": err.Error(),
		})
		return
	}
	o.logger.InfoCtx(cleanupCtx, "agentsys: consumer deleted on shutdown", map[string]any{
		"stream": o.cfg.ChannelStream, "group": o.groupName, "consumer": name,
	})
}

// handleEvent 单条事件的入口。此函数实现了 roadmap PR #6' 范围里 runtime 主循环的
// 步骤 1-4(过滤 / @检测 / 预算 / 组 scoped),步骤 5-8 的 tool-loop 委托给
// handler.go 的 handleMention。
//
// 返回 nil → eventbus 框架 XACK;返 err → 留 PEL,下次重启重放。
func (o *Orchestrator) handleEvent(ctx context.Context, msg eventbus.Message) error {
	// 1. 过滤非 message.posted(包括未来可能加的 message.updated / archive.done 等)
	if msg.Fields["event_type"] != ChannelEventType {
		return nil
	}

	orgID, err := parseUint64(msg.Fields["org_id"])
	if err != nil {
		// 格式错的事件视为有毒消息,直接 ACK 丢弃(避免无限重放)
		o.logger.WarnCtx(ctx, "agentsys: malformed org_id, acking", map[string]any{
			"stream_id": msg.ID, "raw": msg.Fields["org_id"],
		})
		return nil
	}
	channelID, err := parseUint64(msg.Fields["channel_id"])
	if err != nil {
		o.logger.WarnCtx(ctx, "agentsys: malformed channel_id, acking", map[string]any{
			"stream_id": msg.ID, "raw": msg.Fields["channel_id"],
		})
		return nil
	}
	// author_principal_id 和 message_id 是组 Trigger 上下文用的:让 agent 回复自动 @
	// 提问人 + 自动标记为对触发消息的回复。解析失败视为无触发上下文(降级到普通 post)。
	authorPID, _ := parseUint64(msg.Fields["author_principal_id"])   //sayso-lint:ignore err-swallow
	triggerMsgID, _ := parseUint64(msg.Fields["message_id"])         //sayso-lint:ignore err-swallow

	// 2. 防级联 —— 顶级 agent 自己发的消息不处理。正常情况下 @ 检测会挡住(agent 回复
	// 一般不会 @ 自己),但这里显式加守门,确保未来万一 agent 的 mention 列表里出现了
	// topOrchestratorPID(例如人调 post_message tool 手填)也不会触发自回复循环。
	if authorPID != 0 && authorPID == o.agentPrincipalID {
		return nil
	}

	// 3. @ 检测 —— 未被 @ 当前 agent 就直接 ACK(不响应、不写 audit、不耗 token)
	mentions := parseMentionCSV(msg.Fields["mentioned_principal_ids"])
	if !containsPID(mentions, o.agentPrincipalID) {
		return nil
	}

	// 3a. ChannelKindFilter(Architect 用):非 project_console channel 直接 ACK 跳过
	if !o.passesChannelKindFilter(ctx, channelID) {
		return nil
	}

	// 4. 预算检查
	if o.cfg.DailyBudgetPerOrgUSD > 0 {
		over, err := o.isOverBudget(ctx, orgID)
		if err != nil {
			// DB 问题 → 不 ACK,下次重放。别让"查账本报错"变成"直接放行"。
			return fmt.Errorf("check budget: %w", err)
		}
		if over {
			o.handleBudgetExceeded(ctx, orgID, channelID)
			return nil // 已处理(回了"预算用完"消息 + 写 audit),ACK
		}
	}

	// 5. 构 scoped + LLM tool-loop(handler.go)
	s := scoped.NewForTrigger(orgID, channelID, o.agentPrincipalID,
		scoped.Trigger{AuthorPrincipalID: authorPID, MessageID: triggerMsgID},
		o.scopedDeps,
	)
	return o.handleMention(ctx, s)
}

// parseMentionCSV 把 "101,102,103" 形态解析成 []uint64;
// 空串 / 格式错的项跳过,不报错(消费端对生产端的小错容忍)。
func parseMentionCSV(raw string) []uint64 {
	if raw == "" {
		return nil
	}
	out := make([]uint64, 0, 4)
	start := 0
	for i := 0; i <= len(raw); i++ {
		if i < len(raw) && raw[i] != ',' {
			continue
		}
		if i > start {
			if v, err := strconv.ParseUint(raw[start:i], 10, 64); err == nil {
				out = append(out, v)
			}
		}
		start = i + 1
	}
	return out
}

func containsPID(ids []uint64, target uint64) bool {
	for _, v := range ids {
		if v == target {
			return true
		}
	}
	return false
}

func parseUint64(s string) (uint64, error) {
	if s == "" {
		return 0, errors.New("empty")
	}
	return strconv.ParseUint(s, 10, 64)
}
