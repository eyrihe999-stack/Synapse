// migration.go agent 模块数据库迁移。
//
// agent 模块的 6 张表里:
//   - agents / agent_methods / agent_secrets / agent_publishes
//     走 GORM AutoMigrate(常规表)
//   - agent_invocations / agent_invocation_payloads 使用 MySQL RANGE
//     分区,必须手写 DDL,**不能** AutoMigrate(会丢分区定义)
//
// 启动顺序:
//  1. AutoMigrate 前 4 张表
//  2. 调用 EnsureAgentIndexes 补索引
//  3. 调用 EnsurePartitionTables 确保分区表存在(首次建表 + 确保未来几个月分区)
package agent

import (
	"context"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/agent/model"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"gorm.io/gorm"
)

// RunMigrations 执行 agent 模块所有表/分区/索引的迁移。
// onReady 在全部成功后被调用,用于 handler.Ready 标记。
//
// 错误:AutoMigrate、索引创建或分区表创建任一步骤失败均返回包装 ErrAgentInternal 的错误。
func RunMigrations(ctx context.Context, db *gorm.DB, log logger.LoggerInterface, onReady func()) error {
	// 常规表:4 张
	if err := db.WithContext(ctx).AutoMigrate(
		&model.Agent{},
		&model.AgentMethod{},
		&model.AgentSecret{},
		&model.AgentPublish{},
	); err != nil {
		log.ErrorCtx(ctx, "agent 模块 AutoMigrate 失败", err, nil)
		return fmt.Errorf("agent auto-migrate: %w: %w", err, ErrAgentInternal)
	}
	log.InfoCtx(ctx, "agent 模块 AutoMigrate 完成", nil)

	if err := model.EnsureAgentIndexes(db); err != nil {
		log.ErrorCtx(ctx, "agent 模块索引创建失败", err, nil)
		return fmt.Errorf("agent ensure indexes: %w: %w", err, ErrAgentInternal)
	}
	log.InfoCtx(ctx, "agent 模块索引创建完成", nil)

	// 分区表:agent_invocations + agent_invocation_payloads
	if err := model.EnsurePartitionTables(ctx, db); err != nil {
		log.ErrorCtx(ctx, "agent 分区表创建失败", err, nil)
		return fmt.Errorf("agent ensure partitions: %w: %w", err, ErrAgentInternal)
	}
	log.InfoCtx(ctx, "agent 分区表创建完成", nil)

	if onReady != nil {
		onReady()
	}
	return nil
}
