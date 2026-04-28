package channel

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/channel/model"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
)

// RunMigrations 执行 channel 模块数据库迁移。
//
// AutoMigrate 9 张属于 channel 协作载体的表(Project / Version / ChannelVersion
// 已迁到 pm 模块,由 pm.RunMigrations 负责)。
//
// projects.name_active 生成列 + 唯一索引也迁到 pm/migration.go;channel 模块
// 不再操作 projects 表。
//
// 调用次序约束:**main.go 必须先调 pm.RunMigrations,再调 channel.RunMigrations**
// —— 因为 channel.Channel 表的 project_id 外键依赖 projects 表;workstream_id
// 外键依赖 workstreams 表(T9 引入)。
func RunMigrations(ctx context.Context, db *gorm.DB, log logger.LoggerInterface, onReady func()) error {
	log.InfoCtx(ctx, "channel: running MySQL migrations", nil)

	if err := db.WithContext(ctx).AutoMigrate(
		&model.Channel{},
		&model.ChannelMember{},
		&model.ChannelMessage{},
		&model.ChannelMessageMention{},
		&model.ChannelMessageReaction{},
		&model.ChannelDocument{},
		&model.ChannelDocumentVersion{},
		&model.ChannelDocumentLock{},
		&model.ChannelAttachment{},
	); err != nil {
		return fmt.Errorf("channel auto-migrate: %w: %w", err, ErrChannelInternal)
	}

	log.InfoCtx(ctx, "channel: migrations completed", nil)
	if onReady != nil {
		onReady()
	}
	return nil
}

// projects.name_active 生成列 + 唯一索引相关 helper 已迁到 pm/migration.go。
// channel 模块不再操作 projects 表的 schema。
