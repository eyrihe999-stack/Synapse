// migration.go user_integration 模块的 MySQL schema 迁移。
//
// 结构和 organization.RunMigrations 对齐:AutoMigrate + EnsureIndexes(均幂等)。
package user_integration

import (
	"context"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/common/database"
	"github.com/eyrihe999-stack/Synapse/internal/user_integration/model"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"gorm.io/gorm"
)

// RunMigrations 创建 user_integrations 表及索引。
//
// 幂等:AutoMigrate 只做增量列/表,EnsureIndexes 按名查重跳过已存在索引。重复调用无副作用。
//
// 返回 error 的场景(均 wrap ErrIntegrationInternal,装配层应 fatal):
//
//   - AutoMigrate 失败:MySQL 不可达 / 当前 DB 用户无 DDL 权限 / schema drift 无法兼容
//   - EnsureIndexes 失败:同上,或已有不同 shape 的同名索引导致冲突(人工介入 DROP 再重跑)
func RunMigrations(ctx context.Context, db *gorm.DB, log logger.LoggerInterface, onReady func()) error {
	if err := db.WithContext(ctx).AutoMigrate(&model.UserIntegration{}); err != nil {
		log.ErrorCtx(ctx, "user_integration AutoMigrate 失败", err, nil)
		return fmt.Errorf("user_integration auto-migrate: %w: %w", err, ErrIntegrationInternal)
	}
	log.InfoCtx(ctx, "user_integration AutoMigrate 完成", nil)

	if err := database.EnsureIndexes(db, indexSpecs); err != nil {
		log.ErrorCtx(ctx, "user_integration 索引创建失败", err, nil)
		return fmt.Errorf("user_integration ensure indexes: %w: %w", err, ErrIntegrationInternal)
	}
	log.InfoCtx(ctx, "user_integration 索引创建完成", nil)

	if onReady != nil {
		onReady()
	}
	return nil
}

var indexSpecs = []database.IndexSpec{
	{
		Table:   TableUserIntegrations,
		Name:    "uk_ui_user_provider_account",
		Columns: []string{"user_id", "provider", "external_account_id"},
		Unique:  true,
	},
	{
		Table:   TableUserIntegrations,
		Name:    "idx_ui_user",
		Columns: []string{"user_id"},
	},
	{
		Table:   TableUserIntegrations,
		Name:    "idx_ui_user_status",
		Columns: []string{"user_id", "status"},
	},
}
