// migration.go source 模块数据库迁移(建表 + 加索引)。
package source

import (
	"context"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/source/model"
	"gorm.io/gorm"
)

// RunMigrations 执行 source 模块的表迁移与索引创建。
//
// 步骤:
//  1. AutoMigrate:创建/补齐 sources 表(包括新加的 name 列)
//  2. 回填:把已有 manual_upload 行的 name 置为"我的上传"(幂等)
//  3. EnsureSourceIndexes:幂等创建/补齐索引(含 uk_sources_owner_name)
//
// 顺序重要:回填必须在索引建立之前完成,避免历史空 name 行和 uk_sources_owner_name 冲突。
// 每一步都幂等,重复调用安全。onReady 在迁移成功后被调用。
func RunMigrations(ctx context.Context, db *gorm.DB, log logger.LoggerInterface, onReady func()) error {
	if err := db.WithContext(ctx).AutoMigrate(&model.Source{}); err != nil {
		log.ErrorCtx(ctx, "source 模块 AutoMigrate 失败", err, nil)
		return fmt.Errorf("source auto-migrate: %w: %w", err, ErrSourceInternal)
	}
	log.InfoCtx(ctx, "source 模块 AutoMigrate 完成", nil)

	if err := backfillManualUploadName(ctx, db); err != nil {
		log.ErrorCtx(ctx, "source 模块 manual_upload name 回填失败", err, nil)
		return fmt.Errorf("source backfill name: %w: %w", err, ErrSourceInternal)
	}
	log.InfoCtx(ctx, "source 模块 manual_upload name 回填完成", nil)

	if err := model.EnsureSourceIndexes(db); err != nil {
		log.ErrorCtx(ctx, "source 模块索引创建失败", err, nil)
		return fmt.Errorf("source ensure indexes: %w: %w", err, ErrSourceInternal)
	}
	log.InfoCtx(ctx, "source 模块索引创建完成", nil)

	if onReady != nil {
		onReady()
	}
	return nil
}

// backfillManualUploadName 把已存在但 name 仍为空的 manual_upload 行回填为"我的上传"。
// 每个 (org_id, owner_user_id) 下 manual_upload 最多一条,回填后不会和自建 custom 冲突
// (custom 的 name 由 CreateCustomSource 强制非空;且约束 owner 自己不重名)。
func backfillManualUploadName(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).
		Model(&model.Source{}).
		Where("kind = ? AND (name IS NULL OR name = '')", model.KindManualUpload).
		Update("name", model.DefaultManualUploadName).Error
}
