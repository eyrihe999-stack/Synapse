// Package task 通用 task 模块。详见 internal/task/const.go。
package task

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/task/model"
)

// RunMigrations task 模块数据库迁移。
//
// 步骤:
//  1. AutoMigrate 四张表 —— 字段和普通索引由 struct tag 建
//
// 无生成列 / 无复杂 DDL,比 PR #2 channel 和 PR #3 asyncjob 的迁移简单。
func RunMigrations(ctx context.Context, db *gorm.DB, log logger.LoggerInterface, onReady func()) error {
	log.InfoCtx(ctx, "task: running MySQL migrations", nil)
	if err := db.WithContext(ctx).AutoMigrate(
		&model.Task{},
		&model.TaskReviewer{},
		&model.TaskSubmission{},
		&model.TaskReview{},
	); err != nil {
		return fmt.Errorf("task auto-migrate: %w: %w", err, ErrTaskInternal)
	}
	log.InfoCtx(ctx, "task: migrations completed", nil)
	if onReady != nil {
		onReady()
	}
	return nil
}
