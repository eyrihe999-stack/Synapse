package user

import (
	"context"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/user/model"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"gorm.io/gorm"
)

// RunMigrations 执行 user 模块数据库迁移。
//
// 错误:AutoMigrate 失败时返回包装 ErrUserInternal 的错误。
func RunMigrations(ctx context.Context, db *gorm.DB, log logger.LoggerInterface, onReady func()) error {
	if err := db.WithContext(ctx).AutoMigrate(&model.User{}); err != nil {
		log.ErrorCtx(ctx, "user 模块 AutoMigrate 失败", err, nil)
		return fmt.Errorf("auto migrate user: %w: %w", err, ErrUserInternal)
	}
	log.InfoCtx(ctx, "user 模块 AutoMigrate 完成", nil)
	if onReady != nil {
		onReady()
	}
	return nil
}
