// Package asyncjob 通用异步任务模块。详见 internal/asyncjob/model/models.go。
package asyncjob

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/asyncjob/model"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
)

// RunMigrations asyncjob 模块迁移。
//
// 步骤:
//  1. AutoMigrate async_jobs —— 字段和普通索引由 model tag 建
//  2. ensureIdempotencyActiveColumn:加 idem_active 生成列,空幂等键 → NULL
//  3. ensureIdempotencyActiveUnique:在 (org_id, kind, idem_active) 建唯一索引
//
// 步骤 2 / 3 是 PR #3 新增。沿用 channel 模块的"生成列 + UNIQUE 允许多 NULL"套路
// (参见 internal/channel/migration.go),绕开 MySQL 不支持 partial unique index 的限制。
func RunMigrations(ctx context.Context, db *gorm.DB, log logger.LoggerInterface, onReady func()) error {
	log.InfoCtx(ctx, "asyncjob: running MySQL migrations", nil)
	if err := db.WithContext(ctx).AutoMigrate(&model.Job{}); err != nil {
		return fmt.Errorf("asyncjob auto-migrate: %w", err)
	}
	if err := ensureIdempotencyActiveColumn(ctx, db, log); err != nil {
		return fmt.Errorf("asyncjob ensure idem_active column: %w", err)
	}
	if err := ensureIdempotencyActiveUnique(ctx, db, log); err != nil {
		return fmt.Errorf("asyncjob ensure idem_active unique: %w", err)
	}
	log.InfoCtx(ctx, "asyncjob: migrations completed", nil)
	if onReady != nil {
		onReady()
	}
	return nil
}

// ensureIdempotencyActiveColumn 在 async_jobs 表上建 idem_active 生成列。
//
// 生成规则:`idem_active = IF(idempotency_key = '', NULL, idempotency_key)`;
// 空幂等键 → NULL,MySQL 的 UNIQUE 对多个 NULL 值允许并存,于是"没填幂等键"的
// 任务不受唯一约束拖累;填了幂等键的 (org_id, kind, key) 三元组唯一。
//
// 幂等 —— 先查 information_schema 判断列是否已存在。
func ensureIdempotencyActiveColumn(ctx context.Context, db *gorm.DB, log logger.LoggerInterface) error {
	var n int
	err := db.WithContext(ctx).Raw(
		"SELECT 1 FROM information_schema.COLUMNS WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ? LIMIT 1",
		"async_jobs", "idem_active",
	).Scan(&n).Error
	if err != nil {
		return fmt.Errorf("check idem_active column: %w", err)
	}
	if n == 1 {
		return nil
	}

	ddl := "ALTER TABLE `async_jobs` ADD COLUMN `idem_active` VARCHAR(128) " +
		"GENERATED ALWAYS AS (IF(`idempotency_key` = '', NULL, `idempotency_key`)) STORED"
	if err := db.WithContext(ctx).Exec(ddl).Error; err != nil {
		return fmt.Errorf("add idem_active column: %w", err)
	}
	log.InfoCtx(ctx, "asyncjob: added async_jobs.idem_active generated column", nil)
	return nil
}

// ensureIdempotencyActiveUnique 在 async_jobs(org_id, kind, idem_active) 建唯一索引。
// 依赖 idem_active 列已存在。幂等 —— 按索引名查重。
func ensureIdempotencyActiveUnique(ctx context.Context, db *gorm.DB, log logger.LoggerInterface) error {
	var n int
	err := db.WithContext(ctx).Raw(
		"SELECT 1 FROM information_schema.STATISTICS WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ? LIMIT 1",
		"async_jobs", "uk_async_jobs_org_kind_idem_active",
	).Scan(&n).Error
	if err != nil {
		return fmt.Errorf("check idem_active unique index: %w", err)
	}
	if n == 1 {
		return nil
	}

	ddl := "CREATE UNIQUE INDEX `uk_async_jobs_org_kind_idem_active` ON `async_jobs` (`org_id`, `kind`, `idem_active`)"
	if err := db.WithContext(ctx).Exec(ddl).Error; err != nil {
		return fmt.Errorf("create idem_active unique index: %w", err)
	}
	log.InfoCtx(ctx, "asyncjob: created uk_async_jobs_org_kind_idem_active unique index", nil)
	return nil
}
