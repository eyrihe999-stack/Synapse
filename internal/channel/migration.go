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
// 步骤:
//  1. AutoMigrate 5 张表 —— 由 struct GORM tag 建的字段和索引在这一步到位
//  2. ensureProjectNameActiveColumn:加 name_active 生成列(归档后释放名字)
//  3. ensureProjectNameActiveUnique:在 (org_id, name_active) 建唯一索引
//
// 为什么不用 EnsureIndex helper:因为 name_active 是 generated column,
// 要用 ALTER TABLE ADD COLUMN ... GENERATED ALWAYS AS ... 建,而不是 CREATE INDEX。
// 两步都是幂等的(查 information_schema 判断后再建)。
func RunMigrations(ctx context.Context, db *gorm.DB, log logger.LoggerInterface, onReady func()) error {
	log.InfoCtx(ctx, "channel: running MySQL migrations", nil)

	if err := db.WithContext(ctx).AutoMigrate(
		&model.Project{},
		&model.Version{},
		&model.Channel{},
		&model.ChannelVersion{},
		&model.ChannelMember{},
		&model.ChannelMessage{},
		&model.ChannelMessageMention{},
		&model.ChannelMessageReaction{},
		&model.ChannelKBRef{},
		&model.ChannelDocument{},
		&model.ChannelDocumentVersion{},
		&model.ChannelDocumentLock{},
		&model.ChannelAttachment{},
	); err != nil {
		return fmt.Errorf("channel auto-migrate: %w: %w", err, ErrChannelInternal)
	}

	if err := ensureProjectNameActiveColumn(ctx, db, log); err != nil {
		return fmt.Errorf("channel ensure name_active column: %w: %w", err, ErrChannelInternal)
	}

	if err := ensureProjectNameActiveUnique(ctx, db, log); err != nil {
		return fmt.Errorf("channel ensure name_active unique: %w: %w", err, ErrChannelInternal)
	}

	log.InfoCtx(ctx, "channel: migrations completed", nil)
	if onReady != nil {
		onReady()
	}
	return nil
}

// ensureProjectNameActiveColumn 在 projects 表上建 name_active 生成列。
//
// 生成规则:`name_active = IF(archived_at IS NULL, name, NULL)`;归档时自动
// 变 NULL,于是 (org_id, name_active) 唯一约束就"跳过"归档行 —— 名字被释放。
//
// MySQL 的 UNIQUE INDEX 允许多个 NULL 值共存,这是本机制成立的前提。
func ensureProjectNameActiveColumn(ctx context.Context, db *gorm.DB, log logger.LoggerInterface) error {
	var n int
	err := db.WithContext(ctx).Raw(
		"SELECT 1 FROM information_schema.COLUMNS WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ? LIMIT 1",
		"projects", "name_active",
	).Scan(&n).Error
	if err != nil {
		return fmt.Errorf("check name_active column: %w", err)
	}
	if n == 1 {
		return nil
	}

	ddl := "ALTER TABLE `projects` ADD COLUMN `name_active` VARCHAR(128) " +
		"GENERATED ALWAYS AS (IF(`archived_at` IS NULL, `name`, NULL)) STORED"
	if err := db.WithContext(ctx).Exec(ddl).Error; err != nil {
		return fmt.Errorf("add name_active column: %w", err)
	}
	log.InfoCtx(ctx, "channel: added projects.name_active generated column", nil)
	return nil
}

// ensureProjectNameActiveUnique 在 projects(org_id, name_active) 建唯一索引。
//
// 依赖 name_active 列已存在。幂等 —— 按索引名查重。
func ensureProjectNameActiveUnique(ctx context.Context, db *gorm.DB, log logger.LoggerInterface) error {
	var n int
	err := db.WithContext(ctx).Raw(
		"SELECT 1 FROM information_schema.STATISTICS WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ? LIMIT 1",
		"projects", "uk_projects_org_name_active",
	).Scan(&n).Error
	if err != nil {
		return fmt.Errorf("check name_active unique index: %w", err)
	}
	if n == 1 {
		return nil
	}

	ddl := "CREATE UNIQUE INDEX `uk_projects_org_name_active` ON `projects` (`org_id`, `name_active`)"
	if err := db.WithContext(ctx).Exec(ddl).Error; err != nil {
		return fmt.Errorf("create name_active unique index: %w", err)
	}
	log.InfoCtx(ctx, "channel: created uk_projects_org_name_active unique index", nil)
	return nil
}
