// migration.go 组织模块数据库迁移(建表、加索引)。
package organization

import (
	"context"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/organization/model"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"gorm.io/gorm"
)

// RunMigrations 执行组织模块的表迁移与索引创建。
//
// 执行步骤:
//  1. AutoMigrate:仅做增量表/列添加,不修改已有结构
//  2. EnsureOrgIndexes:创建所有索引(幂等)
//
// onReady 在迁移成功完成后被调用(用于 main.go 协调服务就绪状态)。
// 任一步骤失败都会打 ErrorCtx 并返回 error。
func RunMigrations(ctx context.Context, db *gorm.DB, log logger.LoggerInterface, onReady func()) error {
	if err := db.WithContext(ctx).AutoMigrate(
		&model.Org{},
		&model.OrgRole{},
		&model.OrgMember{},
		&model.OrgInvitation{},
		&model.OrgMemberRoleHistory{},
	); err != nil {
		log.ErrorCtx(ctx, "组织模块 AutoMigrate 失败", err, nil)
		return fmt.Errorf("organization auto-migrate: %w: %w", err, ErrOrgInternal)
	}
	log.InfoCtx(ctx, "组织模块 AutoMigrate 完成", nil)

	if err := model.EnsureOrgIndexes(db); err != nil {
		log.ErrorCtx(ctx, "组织模块索引创建失败", err, nil)
		return fmt.Errorf("organization ensure indexes: %w: %w", err, ErrOrgInternal)
	}
	log.InfoCtx(ctx, "组织模块索引创建完成", nil)

	if onReady != nil {
		onReady()
	}
	return nil
}
