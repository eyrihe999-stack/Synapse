// Package asyncjob 通用异步任务模块。详见 internal/asyncjob/model/models.go。
package asyncjob

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/asyncjob/model"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
)

// RunMigrations AutoMigrate async_jobs 表。索引由 model tag 维护。
// 可能返回:DB 连接 / DDL 执行失败错误(由 GORM 抛出,原样包装)。
func RunMigrations(ctx context.Context, db *gorm.DB, log logger.LoggerInterface, onReady func()) error {
	log.Info("asyncjob: running MySQL migrations", nil)
	if err := db.WithContext(ctx).AutoMigrate(&model.Job{}); err != nil {
		return fmt.Errorf("asyncjob auto-migrate: %w", err)
	}
	log.Info("asyncjob: migrations completed", nil)
	if onReady != nil {
		onReady()
	}
	return nil
}
