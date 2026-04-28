// Package pmevent 订阅 pm 模块事件流(synapse:pm:events),按 event_type 触发
// channel 模块的副作用 —— 主要是"在合适时机把 channel 建出来":
//
//   - project.created    → 建 kind=project_console channel + 加 owner + Architect 成员
//   - workstream.created → lazy-create kind=workstream channel + 反指 workstream.channel_id
//
// 这是 pm 模块和 channel 模块解耦的关键:pm 不直接调用 channel,只发事件;
// channel 监听事件按需建 channel。这样跨模块依赖单向(pm → 事件 → channel)。
//
// 工作方式镜像 channel/eventcard.Writer:
//   - 单 stream(synapse:pm:events),consumer group `channel-pm-event-handler`
//   - 同一实例重启复用 consumer name(SYNAPSE_INSTANCE_ID > hostname > "pm-event")
//   - 每个 handler 幂等(NOT EXISTS 守卫 / UNIQUE 兜底),重放安全
//
// 失败处理:
//   - 业务级失败(project 不存在 / 数据冲突)→ ACK 丢弃 + warn(事件已落 stream,
//     可以 retry 但通常没意义)
//   - DB 异常 → 不 ACK,PEL 重放
package pmevent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/agents"
	"github.com/eyrihe999-stack/Synapse/internal/common/eventbus"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
)

// ConsumerGroupName 同 channel/eventcard 的命名风格;每实例独立 consumer name。
const ConsumerGroupName = "channel-pm-event-handler"

// idleConsumerSweepThreshold 启动时清理本 group 下 idle 超过此阈值的残留 consumer。
const idleConsumerSweepThreshold = 10 * time.Minute

// Config Consumer 构造参数。
type Config struct {
	// PMStream synapse:pm:events 的 key,和 pm/service 的 publisher 对齐
	PMStream string
}

// Consumer pm 事件 → channel 副作用的桥接器。
type Consumer struct {
	cfg          Config
	consumer     eventbus.ConsumerGroup
	db           *gorm.DB // 直接 raw SQL,不通过 channel.repo(handler 跨多张表)
	consumerName string
	architectPID uint64 // Architect agent 的 principal_id,启动时一次性查
	logger       logger.LoggerInterface
}

// New 构造 Consumer。db / consumer / log 必填。
//
// 启动时会查 Architect principal_id 缓存到 struct;查不到允许(PR-A 老库还没 seed
// Architect)—— project.created 时仅加 owner,不加 Architect,降级正常。
func New(cfg Config, consumer eventbus.ConsumerGroup, db *gorm.DB, log logger.LoggerInterface) (*Consumer, error) {
	consumerName := os.Getenv("SYNAPSE_INSTANCE_ID")
	if consumerName == "" {
		host, _ := os.Hostname() //sayso-lint:ignore err-swallow
		consumerName = host
	}
	if consumerName == "" {
		consumerName = "pm-event"
	}

	architectPID := lookupArchitectPrincipalID(db, log)

	return &Consumer{
		cfg:          cfg,
		consumer:     consumer,
		db:           db,
		consumerName: consumerName,
		architectPID: architectPID,
		logger:       log,
	}, nil
}

// lookupArchitectPrincipalID 启动期查 Architect agent 的 principal_id。
// 查不到返 0(降级:project.created 时只加 owner,不加 Architect)。
func lookupArchitectPrincipalID(db *gorm.DB, log logger.LoggerInterface) uint64 {
	var pid uint64
	err := db.Raw(
		"SELECT principal_id FROM agents WHERE agent_id = ? LIMIT 1",
		agents.ProjectArchitectAgentID,
	).Scan(&pid).Error
	if err != nil {
		log.Warn("pmevent: lookup architect principal_id failed", map[string]any{"err": err.Error()})
		return 0
	}
	if pid == 0 {
		log.Warn("pmevent: architect agent not seeded, will skip adding to console channels", nil)
	}
	return pid
}

// Run 阻塞启动 consumer goroutine。ctx 取消 → 退出。
func (c *Consumer) Run(ctx context.Context) error {
	if err := c.consumer.EnsureGroup(ctx, c.cfg.PMStream, ConsumerGroupName); err != nil {
		return fmt.Errorf("ensure pm consumer group: %w", err)
	}
	deleted, skipped, err := c.consumer.SweepIdleConsumers(ctx, c.cfg.PMStream, ConsumerGroupName, idleConsumerSweepThreshold)
	if err != nil {
		c.logger.WarnCtx(ctx, "pmevent: sweep idle consumers failed", map[string]any{
			"stream": c.cfg.PMStream, "err": err.Error(),
		})
	} else if deleted > 0 || skipped > 0 {
		c.logger.InfoCtx(ctx, "pmevent: swept idle consumers", map[string]any{
			"stream": c.cfg.PMStream, "deleted": deleted, "skipped": skipped,
		})
	}

	c.logger.InfoCtx(ctx, "pmevent: consumer starting", map[string]any{
		"stream": c.cfg.PMStream, "group": ConsumerGroupName, "consumer": c.consumerName,
		"architect_pid": c.architectPID,
	})

	var wg sync.WaitGroup
	errCh := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer c.cleanupConsumer()
		if err := c.consumer.Consume(ctx, c.cfg.PMStream, ConsumerGroupName,
			c.consumerName, c.handleEvent); err != nil {
			errCh <- fmt.Errorf("pm stream consume: %w", err)
		}
	}()

	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

// cleanupConsumer 实例关闭时主动从 group 摘掉 consumer。
func (c *Consumer) cleanupConsumer() {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.consumer.DeleteConsumer(cleanupCtx, c.cfg.PMStream, ConsumerGroupName, c.consumerName); err != nil {
		c.logger.WarnCtx(cleanupCtx, "pmevent: cleanup delete consumer failed", map[string]any{
			"stream": c.cfg.PMStream, "consumer": c.consumerName, "err": err.Error(),
		})
		return
	}
	c.logger.InfoCtx(cleanupCtx, "pmevent: consumer deleted on shutdown", map[string]any{
		"stream": c.cfg.PMStream, "consumer": c.consumerName,
	})
}

// handleEvent 路由分发。返 nil → ACK;返 error → PEL 重放。
func (c *Consumer) handleEvent(ctx context.Context, msg eventbus.Message) error {
	eventType := msg.Fields["event_type"]
	switch eventType {
	case "project.created":
		return c.handleProjectCreated(ctx, msg)
	case "workstream.created":
		return c.handleWorkstreamCreated(ctx, msg)
	default:
		// 其它事件目前没有 channel 侧副作用(initiative.created / version.created
		// 等不需要建 channel),静默 ACK。
		return nil
	}
}

// handleProjectCreated 创建 Project Console channel + 加 owner + Architect。
//
// 幂等:NOT EXISTS 守卫 channel 已存在;channel_members 用 INSERT IGNORE。
func (c *Consumer) handleProjectCreated(ctx context.Context, msg eventbus.Message) error {
	projectID, _ := strconv.ParseUint(msg.Fields["project_id"], 10, 64)
	if projectID == 0 {
		c.logger.WarnCtx(ctx, "pmevent: project.created missing project_id", map[string]any{"stream_id": msg.ID})
		return nil
	}

	tx := c.db.WithContext(ctx)

	// Step 1: 建 console channel(NOT EXISTS 守卫)
	if err := tx.Exec(`
		INSERT INTO channels (org_id, project_id, name, purpose, status, kind, created_by, created_at, updated_at)
		SELECT p.org_id, p.id, ?, ?, ?, ?, p.created_by, NOW(), NOW()
		FROM projects p
		WHERE p.id = ?
		  AND p.archived_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM channels c WHERE c.project_id = p.id AND c.kind = ?)
	`, "Project Console", "Project-level discussion and AI Architect workspace",
		"open", "project_console", projectID, "project_console").Error; err != nil {
		return fmt.Errorf("insert console channel: %w", err)
	}

	// Step 2: 加 owner 成员
	if err := tx.Exec(`
		INSERT IGNORE INTO channel_members (channel_id, principal_id, role, joined_at)
		SELECT c.id, u.principal_id, ?, NOW()
		FROM channels c
		INNER JOIN projects p ON p.id = c.project_id
		INNER JOIN users u ON u.id = p.created_by
		WHERE c.project_id = ? AND c.kind = ? AND u.principal_id <> 0
	`, "owner", projectID, "project_console").Error; err != nil {
		return fmt.Errorf("insert console owner: %w", err)
	}

	// Step 3: 加 Architect 成员(architectPID=0 跳过 — agent 还没 seed 时降级)
	if c.architectPID > 0 {
		if err := tx.Exec(`
			INSERT IGNORE INTO channel_members (channel_id, principal_id, role, joined_at)
			SELECT c.id, ?, ?, NOW()
			FROM channels c
			WHERE c.project_id = ? AND c.kind = ?
		`, c.architectPID, "admin", projectID, "project_console").Error; err != nil {
			return fmt.Errorf("insert architect: %w", err)
		}
	}

	c.logger.InfoCtx(ctx, "pmevent: console channel ensured", map[string]any{
		"project_id": projectID, "stream_id": msg.ID,
	})
	return nil
}

// handleWorkstreamCreated lazy-create kind=workstream channel + 反指 workstream.channel_id。
//
// 幂等:workstreams.channel_id IS NULL 守卫 — 已挂 channel 的 workstream 跳过(重放安全)。
//
// 决策 4:不加 Architect 成员;workstream channel 由 top-orchestrator(auto_include)
// 自然加入,不是 Architect 的活。
func (c *Consumer) handleWorkstreamCreated(ctx context.Context, msg eventbus.Message) error {
	workstreamID, _ := strconv.ParseUint(msg.Fields["workstream_id"], 10, 64)
	if workstreamID == 0 {
		return nil
	}

	tx := c.db.WithContext(ctx)
	return tx.Transaction(func(t *gorm.DB) error {
		// 查 workstream 当前状态(if channel_id 已挂,直接幂等返回)
		var ws struct {
			ID         uint64
			ProjectID  uint64
			Name       string
			ChannelID  *uint64
			CreatedBy  uint64
		}
		err := t.Raw(
			"SELECT id, project_id, name, channel_id, created_by FROM workstreams WHERE id = ?",
			workstreamID,
		).Scan(&ws).Error
		if err != nil {
			return fmt.Errorf("find workstream: %w", err)
		}
		if ws.ID == 0 {
			c.logger.WarnCtx(ctx, "pmevent: workstream not found, skipping", map[string]any{
				"workstream_id": workstreamID,
			})
			return nil
		}
		if ws.ChannelID != nil && *ws.ChannelID != 0 {
			// 已挂 channel,幂等跳过
			return nil
		}

		// INSERT channel
		now := time.Now()
		if err := t.Exec(`
			INSERT INTO channels (org_id, project_id, name, purpose, status, kind, workstream_id, created_by, created_at, updated_at)
			SELECT p.org_id, p.id, ?, ?, ?, ?, ?, ?, ?, ?
			FROM projects p
			WHERE p.id = ?
		`, ws.Name, "Workstream collaboration",
			"open", "workstream", ws.ID, ws.CreatedBy, now, now,
			ws.ProjectID).Error; err != nil {
			return fmt.Errorf("insert workstream channel: %w", err)
		}

		// 拿到刚 INSERT 的 channel_id(同事务可见)
		var channelID uint64
		if err := t.Raw(
			"SELECT id FROM channels WHERE workstream_id = ? AND kind = ? ORDER BY id DESC LIMIT 1",
			ws.ID, "workstream",
		).Scan(&channelID).Error; err != nil {
			return fmt.Errorf("find new workstream channel id: %w", err)
		}
		if channelID == 0 {
			return errors.New("workstream channel insert succeeded but id not found")
		}

		// UPDATE workstreams.channel_id 反指
		if err := t.Exec(
			"UPDATE workstreams SET channel_id = ?, updated_at = ? WHERE id = ? AND channel_id IS NULL",
			channelID, now, ws.ID,
		).Error; err != nil {
			return fmt.Errorf("update workstream channel_id: %w", err)
		}

		// 加 owner 成员(creator 的 principal)
		if err := t.Exec(`
			INSERT IGNORE INTO channel_members (channel_id, principal_id, role, joined_at)
			SELECT ?, u.principal_id, ?, ?
			FROM users u
			WHERE u.id = ? AND u.principal_id <> 0
		`, channelID, "owner", now, ws.CreatedBy).Error; err != nil {
			return fmt.Errorf("insert owner member: %w", err)
		}

		// auto-include agents:把 auto_include_in_new_channels=TRUE 的 agent 拉进来
		// (top-orchestrator 是其中之一;Architect 由于 false 不会被加 — 决策 4)
		if err := t.Exec(`
			INSERT IGNORE INTO channel_members (channel_id, principal_id, role, joined_at)
			SELECT ?, a.principal_id, ?, ?
			FROM agents a
			WHERE a.auto_include_in_new_channels = TRUE
			  AND a.enabled = TRUE
			  AND (a.org_id = 0 OR a.org_id = (SELECT org_id FROM projects WHERE id = ?))
			  AND a.principal_id <> 0
		`, channelID, "member", now, ws.ProjectID).Error; err != nil {
			return fmt.Errorf("auto-include agents: %w", err)
		}

		c.logger.InfoCtx(ctx, "pmevent: workstream channel created", map[string]any{
			"workstream_id": ws.ID, "channel_id": channelID, "stream_id": msg.ID,
		})
		return nil
	})
}
