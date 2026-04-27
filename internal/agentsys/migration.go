// Package agentsys PR #6' 顶级系统 agent runtime。
//
// 职责:消费 channel.message.posted 事件 → 检查是否被 @ top-orchestrator →
// 按 operating_org 严格隔离调 LLM → 产出回复或 create_task。
//
// 子包:
//   - model/      audit_events / llm_usage 持久化模型
//   - repository/ 上述表的 CRUD
//   - scoped/     ★ ScopedServices:跨 org 隔离核心,绑死 (orgID, channelID, actorPID)
//   - tools/      LLM function-calling tool schema + dispatcher
//   - runtime/    orchestrator + handler(常驻 goroutine + tool-loop)
//   - prompts/    硬编码 system prompt(go:embed)
//
// 顶级 agent 实体本身在 internal/agents.TopOrchestratorAgentID 种子行里,
// 本包不创建 agent 记录,只消费它。
package agentsys

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/agentsys/model"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
)

// RunMigrations agentsys 模块迁移。
//
// 只 AutoMigrate 两张新表(audit_events / llm_usage),索引由 model tag 建。
// 表 schema 详见 model/audit.go + model/usage.go 文件头注释。
func RunMigrations(ctx context.Context, db *gorm.DB, log logger.LoggerInterface, onReady func()) error {
	log.InfoCtx(ctx, "agentsys: running MySQL migrations", nil)
	if err := db.WithContext(ctx).AutoMigrate(&model.AuditEvent{}, &model.LLMUsage{}); err != nil {
		return fmt.Errorf("agentsys auto-migrate: %w", err)
	}
	log.InfoCtx(ctx, "agentsys: migrations completed", nil)
	if onReady != nil {
		onReady()
	}
	return nil
}
