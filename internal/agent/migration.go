// migration.go agent 模块数据库迁移。
package agent

import (
	"context"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/agent/model"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"gorm.io/gorm"
)

// RunMigrations 执行 agent 模块的数据库迁移,失败时返回 ErrAgentInternal。
func RunMigrations(ctx context.Context, db *gorm.DB, log logger.LoggerInterface, onReady func()) error {
	log.Info("agent: running migrations", nil)

	// 兼容迁移：若 version 列已存在但有 NULL 值，先填充默认值
	db.WithContext(ctx).Exec("UPDATE agents SET version = '0.1.0' WHERE version IS NULL OR version = ''")

	if err := db.WithContext(ctx).AutoMigrate(
		&model.Agent{},
		&model.AgentSession{},
		&model.AgentMessage{},
		&model.AgentPublish{},
	); err != nil {
		return fmt.Errorf("agent auto-migrate: %w: %w", err, ErrAgentInternal)
	}

	if err := model.EnsureAgentIndexes(db); err != nil {
		return fmt.Errorf("agent ensure indexes: %w: %w", err, ErrAgentInternal)
	}

	log.Info("agent: migrations completed", nil)
	if onReady != nil {
		onReady()
	}
	return nil
}
