// migration.go 权限模块数据库迁移(建表 + 加索引)。
package permission

import (
	"context"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/permission/model"
	"gorm.io/gorm"
)

// RunMigrations 执行权限模块的表迁移与索引创建。
//
// 执行步骤:
//  1. AutoMigrate:创建 perm_groups / perm_group_members / permission_audit_log 表
//  2. EnsurePermIndexes:幂等创建/补齐所有索引
//
// 所有步骤幂等,可重复跑。onReady 在迁移成功完成后被调用。
//
// 可能的错误:
//   - ErrPermInternal:AutoMigrate 或索引创建失败时返回
func RunMigrations(ctx context.Context, db *gorm.DB, log logger.LoggerInterface, onReady func()) error {
	if err := db.WithContext(ctx).AutoMigrate(
		&model.Group{},
		&model.GroupMember{},
		&model.PermissionAuditLog{},
		&model.ResourceACL{},
	); err != nil {
		log.ErrorCtx(ctx, "权限模块 AutoMigrate 失败", err, nil)
		return fmt.Errorf("permission auto-migrate: %w: %w", err, ErrPermInternal)
	}
	log.InfoCtx(ctx, "权限模块 AutoMigrate 完成", nil)

	if err := model.EnsurePermIndexes(db); err != nil {
		log.ErrorCtx(ctx, "权限模块索引创建失败", err, nil)
		return fmt.Errorf("permission ensure indexes: %w: %w", err, ErrPermInternal)
	}
	log.InfoCtx(ctx, "权限模块索引创建完成", nil)

	if onReady != nil {
		onReady()
	}
	return nil
}
