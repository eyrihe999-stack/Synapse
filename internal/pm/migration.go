// Package pm migration.go 数据库迁移入口。
package pm

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/pm/model"
)

// RunMigrations 执行 pm 模块数据库迁移。
//
// 步骤(每步幂等,失败可重入):
//  1. AutoMigrate 5 张表 —— Project / Initiative / Version / Workstream / ProjectKBRef
//     注:projects / versions 表是从 channel 物理迁过来的,老数据已存在;AutoMigrate
//     此时只对 Version 加新列(released_at / is_system / created_by / updated_at)。
//  2. ensureProjectNameActiveColumn + ensureProjectNameActiveUnique:沿用
//     channel 模块 PR #1 的"归档后释放名字"机制(代码从 channel/migration.go 迁来)
//  3. ensureInitiativeNameActiveColumn + ensureInitiativeNameActiveUnique:同理
//  4. ensureVersionStatusBackfill:老枚举值升级
//     planned     → planning
//     in_progress → active
//
// 后续步骤(T9 任务里补):
//  5. ensureDefaultInitiative / ensureBacklogVersion:每个 project 自动建 default
//  6. ensureChannelWorkstreamBackfill / ensureProjectConsoleChannel:回填存量 channel
//  7. ensureProjectKBRefBackfill:channel_kb_refs → project_kb_refs
//  8. ensureTaskWorkstreamBackfill:tasks.workstream_id 回填
func RunMigrations(ctx context.Context, db *gorm.DB, log logger.LoggerInterface, onReady func()) error {
	log.InfoCtx(ctx, "pm: running MySQL migrations", nil)

	if err := db.WithContext(ctx).AutoMigrate(
		&model.Project{},
		&model.Initiative{},
		&model.Version{},
		&model.Workstream{},
		&model.ProjectKBRef{},
	); err != nil {
		return fmt.Errorf("pm auto-migrate: %w: %w", err, ErrPMInternal)
	}

	if err := ensureProjectNameActiveColumn(ctx, db, log); err != nil {
		return fmt.Errorf("pm ensure project name_active column: %w: %w", err, ErrPMInternal)
	}
	if err := ensureProjectNameActiveUnique(ctx, db, log); err != nil {
		return fmt.Errorf("pm ensure project name_active unique: %w: %w", err, ErrPMInternal)
	}
	if err := ensureInitiativeNameActiveColumn(ctx, db, log); err != nil {
		return fmt.Errorf("pm ensure initiative name_active column: %w: %w", err, ErrPMInternal)
	}
	if err := ensureInitiativeNameActiveUnique(ctx, db, log); err != nil {
		return fmt.Errorf("pm ensure initiative name_active unique: %w: %w", err, ErrPMInternal)
	}
	if err := ensureVersionStatusBackfill(ctx, db, log); err != nil {
		return fmt.Errorf("pm ensure version status backfill: %w: %w", err, ErrPMInternal)
	}

	log.InfoCtx(ctx, "pm: migrations completed", nil)
	if onReady != nil {
		onReady()
	}
	return nil
}

// RunPostMigrations 在 channel / task 模块迁移之后跑的"二阶段迁移"。
//
// 拆出来单独跑的原因:本组步骤要写 channels.kind / channels.workstream_id /
// tasks.workstream_id,这些字段由 channel.RunMigrations / task.RunMigrations
// AutoMigrate 才 ALTER 出来,在它们之前跑会爆"字段不存在"。
//
// main.go 装配次序:
//
//	pm.RunMigrations          (建 5 张表 + Project name_active + Initiative name_active)
//	channel.RunMigrations     (channels 表 ALTER 加 kind / workstream_id)
//	task.RunMigrations        (tasks 表 ALTER 加 workstream_id)
//	pm.RunPostMigrations      (本函数 —— 数据回填 + seed)
//
// 步骤(每步幂等,失败可重入):
//  1. ensureDefaultInitiative —— 每 project 一个 (is_system=true, name=Default)
//  2. ensureBacklogVersion —— 每 project 一个 (is_system=true, name=Backlog)
//  3. ensureChannelWorkstreamBackfill —— 存量 regular channel 建对应 workstream
//     + UPDATE channels.workstream_id / kind 反指
//  4. ensureProjectConsoleChannel —— 每 project 建 kind=project_console channel
//     + 加 owner 成员
//  5. ensureProjectKBRefBackfill —— channel_kb_refs(去重)→ project_kb_refs
//  6. ensureTaskWorkstreamBackfill —— tasks.workstream_id = channel.workstream_id
func RunPostMigrations(ctx context.Context, db *gorm.DB, log logger.LoggerInterface) error {
	log.InfoCtx(ctx, "pm: running post-migrations (data seeding)", nil)

	if err := ensureDefaultInitiative(ctx, db, log); err != nil {
		return fmt.Errorf("pm post-migration ensure default initiative: %w: %w", err, ErrPMInternal)
	}
	if err := ensureBacklogVersion(ctx, db, log); err != nil {
		return fmt.Errorf("pm post-migration ensure backlog version: %w: %w", err, ErrPMInternal)
	}
	if err := ensureChannelWorkstreamBackfill(ctx, db, log); err != nil {
		return fmt.Errorf("pm post-migration ensure channel workstream backfill: %w: %w", err, ErrPMInternal)
	}
	if err := ensureProjectConsoleChannel(ctx, db, log); err != nil {
		return fmt.Errorf("pm post-migration ensure project console: %w: %w", err, ErrPMInternal)
	}
	if err := ensureProjectKBRefBackfill(ctx, db, log); err != nil {
		return fmt.Errorf("pm post-migration ensure project kb ref backfill: %w: %w", err, ErrPMInternal)
	}
	if err := ensureTaskWorkstreamBackfill(ctx, db, log); err != nil {
		return fmt.Errorf("pm post-migration ensure task workstream backfill: %w: %w", err, ErrPMInternal)
	}
	if err := dropDeprecatedChannelKBRefs(ctx, db, log); err != nil {
		return fmt.Errorf("pm post-migration drop channel_kb_refs: %w: %w", err, ErrPMInternal)
	}

	log.InfoCtx(ctx, "pm: post-migrations completed", nil)
	return nil
}

// dropDeprecatedChannelKBRefs 在数据已经迁到 project_kb_refs 之后,DROP 老表
// channel_kb_refs(PR-B B1 一并清理)。
//
// 安全前提:
//  1. ensureProjectKBRefBackfill 已经把 channel_kb_refs 数据搬到 project_kb_refs
//     (按 channel.project_id 反查聚合 + UNIQUE 去重)
//  2. channel 模块已经不再读写 channel_kb_refs(KBRefService 删除,kb_ref.go 删除,
//     KBQueryService 改成走 project_kb_refs)
//
// 第一次跑会真删表;后续跑 IF EXISTS 兜底 no-op。
func dropDeprecatedChannelKBRefs(ctx context.Context, db *gorm.DB, log logger.LoggerInterface) error {
	var exists int
	err := db.WithContext(ctx).Raw(
		"SELECT 1 FROM information_schema.TABLES WHERE table_schema = DATABASE() AND table_name = 'channel_kb_refs' LIMIT 1",
	).Scan(&exists).Error
	if err != nil {
		return fmt.Errorf("check channel_kb_refs table: %w", err)
	}
	if exists == 0 {
		return nil
	}
	if err := db.WithContext(ctx).Exec("DROP TABLE `channel_kb_refs`").Error; err != nil {
		return fmt.Errorf("drop channel_kb_refs: %w", err)
	}
	log.InfoCtx(ctx, "pm: dropped deprecated channel_kb_refs table", nil)
	return nil
}

// ensureDefaultInitiative 对每个 project 自动建一个 (is_system=true, name=Default)
// 的 initiative。已存在则跳过(WHERE NOT EXISTS 子查询)。
func ensureDefaultInitiative(ctx context.Context, db *gorm.DB, log logger.LoggerInterface) error {
	res := db.WithContext(ctx).Exec(`
		INSERT INTO initiatives (project_id, name, description, target_outcome, status, is_system, created_by, created_at, updated_at)
		SELECT p.id, ?, ?, '', ?, ?, 0, NOW(), NOW()
		FROM projects p
		WHERE NOT EXISTS (
			SELECT 1 FROM initiatives i
			WHERE i.project_id = p.id AND i.is_system = TRUE AND i.name = ?
		)
	`, DefaultInitiativeName, DefaultInitiativeDescription, InitiativeStatusActive, true, DefaultInitiativeName)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		log.InfoCtx(ctx, "pm: seeded default initiatives", map[string]any{"rows": res.RowsAffected})
	}
	return nil
}

// ensureBacklogVersion 对每个 project 自动建一个 (is_system=true, name=Backlog)
// 的 version,用作"未排期 workstream"的归宿。
func ensureBacklogVersion(ctx context.Context, db *gorm.DB, log logger.LoggerInterface) error {
	res := db.WithContext(ctx).Exec(`
		INSERT INTO versions (project_id, name, status, is_system, created_by, created_at, updated_at)
		SELECT p.id, ?, ?, ?, 0, NOW(), NOW()
		FROM projects p
		WHERE NOT EXISTS (
			SELECT 1 FROM versions v
			WHERE v.project_id = p.id AND v.is_system = TRUE AND v.name = ?
		)
	`, BacklogVersionName, VersionStatusActive, true, BacklogVersionName)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		log.InfoCtx(ctx, "pm: seeded backlog versions", map[string]any{"rows": res.RowsAffected})
	}
	return nil
}

// ensureChannelWorkstreamBackfill 对存量 regular(非 console) channel,在该 project
// 的 default initiative 下建对应 workstream + UPDATE channels.workstream_id / kind。
//
// 两步:
//  1. INSERT workstreams ... SELECT FROM channels(用 channel_id 列做 1:1 反指)
//  2. UPDATE channels JOIN workstreams ... SET workstream_id, kind='workstream'
//
// 幂等:第二步条件 channels.workstream_id IS NULL 防重入产生重复 workstream。
// 但第一步如果重跑,可能在 workstreams 留多余孤儿行 —— 用 NOT EXISTS 兜底。
func ensureChannelWorkstreamBackfill(ctx context.Context, db *gorm.DB, log logger.LoggerInterface) error {
	// Step 1: 为每个未挂 workstream 的 regular channel 建 workstream
	res := db.WithContext(ctx).Exec(`
		INSERT INTO workstreams (initiative_id, project_id, name, status, channel_id, created_by, created_at, updated_at)
		SELECT i.id, c.project_id, c.name, ?, c.id, c.created_by, NOW(), NOW()
		FROM channels c
		INNER JOIN initiatives i
			ON i.project_id = c.project_id AND i.is_system = TRUE AND i.name = ?
		WHERE c.workstream_id IS NULL
		  AND c.archived_at IS NULL
		  AND (c.kind IS NULL OR c.kind = '' OR c.kind = ?)
		  AND NOT EXISTS (SELECT 1 FROM workstreams w WHERE w.channel_id = c.id)
	`, WorkstreamStatusActive, DefaultInitiativeName, "regular")
	if res.Error != nil {
		return fmt.Errorf("step1 backfill workstreams: %w", res.Error)
	}
	if res.RowsAffected > 0 {
		log.InfoCtx(ctx, "pm: backfilled workstreams from existing channels", map[string]any{"rows": res.RowsAffected})
	}

	// Step 2: 反指 channels.workstream_id + 改 kind=workstream
	res = db.WithContext(ctx).Exec(`
		UPDATE channels c
		INNER JOIN workstreams w ON w.channel_id = c.id
		SET c.workstream_id = w.id, c.kind = 'workstream'
		WHERE c.workstream_id IS NULL
	`)
	if res.Error != nil {
		return fmt.Errorf("step2 backfill channels.workstream_id: %w", res.Error)
	}
	if res.RowsAffected > 0 {
		log.InfoCtx(ctx, "pm: backfilled channels.workstream_id", map[string]any{"rows": res.RowsAffected})
	}
	return nil
}

// ensureProjectConsoleChannel 对每个 project 创建一个 kind=project_console channel
// + 加 owner 成员(project.created_by 对应的 principal_id)。
//
// project_console channel 是 Project Architect agent 的工作间;v0 阶段先建 channel
// 本身,Architect agent 的 seed 等 PR-B 落地后由 channel.auto_include 机制把它加入。
func ensureProjectConsoleChannel(ctx context.Context, db *gorm.DB, log logger.LoggerInterface) error {
	// Step 1: 为每个还没有 console 的 project 建 channel
	res := db.WithContext(ctx).Exec(`
		INSERT INTO channels (org_id, project_id, name, purpose, status, kind, created_by, created_at, updated_at)
		SELECT p.org_id, p.id, ?, ?, ?, ?, p.created_by, NOW(), NOW()
		FROM projects p
		WHERE p.archived_at IS NULL
		  AND NOT EXISTS (
			SELECT 1 FROM channels c
			WHERE c.project_id = p.id AND c.kind = ?
		)
	`, "Project Console", "Project-level discussion and AI Architect workspace", "open", "project_console", "project_console")
	if res.Error != nil {
		return fmt.Errorf("step1 insert console channels: %w", res.Error)
	}
	if res.RowsAffected > 0 {
		log.InfoCtx(ctx, "pm: seeded project console channels", map[string]any{"rows": res.RowsAffected})
	}

	// Step 2: 给 console channel 加 owner 成员(creator user 的 principal_id)
	res = db.WithContext(ctx).Exec(`
		INSERT INTO channel_members (channel_id, principal_id, role, joined_at)
		SELECT c.id, u.principal_id, ?, NOW()
		FROM channels c
		INNER JOIN projects p ON p.id = c.project_id
		INNER JOIN users u ON u.id = p.created_by
		WHERE c.kind = ?
		  AND u.principal_id <> 0
		  AND NOT EXISTS (
			SELECT 1 FROM channel_members m WHERE m.channel_id = c.id AND m.principal_id = u.principal_id
		)
	`, "owner", "project_console")
	if res.Error != nil {
		return fmt.Errorf("step2 add console owner: %w", res.Error)
	}
	if res.RowsAffected > 0 {
		log.InfoCtx(ctx, "pm: added owner members to project consoles", map[string]any{"rows": res.RowsAffected})
	}
	return nil
}

// ensureProjectKBRefBackfill 把 channel_kb_refs 按 channel.project_id 聚合,
// 去重写入 project_kb_refs。冲突走 UNIQUE(uk_project_kb_refs_uniq)兜底。
func ensureProjectKBRefBackfill(ctx context.Context, db *gorm.DB, log logger.LoggerInterface) error {
	// 先检查 channel_kb_refs 表存在(老库可能没有,新库会被 channel migration 建)。
	var exists int
	err := db.WithContext(ctx).Raw(
		"SELECT 1 FROM information_schema.TABLES WHERE table_schema = DATABASE() AND table_name = 'channel_kb_refs' LIMIT 1",
	).Scan(&exists).Error
	if err != nil {
		return fmt.Errorf("check channel_kb_refs table: %w", err)
	}
	if exists == 0 {
		return nil
	}

	res := db.WithContext(ctx).Exec(`
		INSERT IGNORE INTO project_kb_refs (project_id, kb_source_id, kb_document_id, attached_by, attached_at)
		SELECT c.project_id, k.kb_source_id, k.kb_document_id, k.added_by, k.added_at
		FROM channel_kb_refs k
		INNER JOIN channels c ON c.id = k.channel_id
	`)
	if res.Error != nil {
		return fmt.Errorf("backfill project_kb_refs: %w", res.Error)
	}
	if res.RowsAffected > 0 {
		log.InfoCtx(ctx, "pm: backfilled project_kb_refs from channel_kb_refs", map[string]any{"rows": res.RowsAffected})
	}
	return nil
}

// ensureTaskWorkstreamBackfill 沿着 task.channel_id → channel.workstream_id 把
// tasks.workstream_id 反指。
//
// 幂等:WHERE tasks.workstream_id IS NULL,不会重复填。
func ensureTaskWorkstreamBackfill(ctx context.Context, db *gorm.DB, log logger.LoggerInterface) error {
	res := db.WithContext(ctx).Exec(`
		UPDATE tasks t
		INNER JOIN channels c ON c.id = t.channel_id
		SET t.workstream_id = c.workstream_id
		WHERE t.workstream_id IS NULL AND c.workstream_id IS NOT NULL
	`)
	if res.Error != nil {
		return fmt.Errorf("backfill tasks.workstream_id: %w", res.Error)
	}
	if res.RowsAffected > 0 {
		log.InfoCtx(ctx, "pm: backfilled tasks.workstream_id", map[string]any{"rows": res.RowsAffected})
	}
	return nil
}

// ensureProjectNameActiveColumn 在 projects 表加 name_active 生成列。
//
// 生成规则:`name_active = IF(archived_at IS NULL, name, NULL)`;归档时自动
// 变 NULL,于是 (org_id, name_active) 唯一约束就"跳过"归档行 —— 名字被释放。
//
// MySQL 的 UNIQUE INDEX 允许多个 NULL 值共存,这是本机制成立的前提。
//
// 代码原本在 channel/migration.go,因 Project 物理迁到 pm 同时迁过来。幂等。
func ensureProjectNameActiveColumn(ctx context.Context, db *gorm.DB, log logger.LoggerInterface) error {
	var n int
	err := db.WithContext(ctx).Raw(
		"SELECT 1 FROM information_schema.COLUMNS WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ? LIMIT 1",
		"projects", "name_active",
	).Scan(&n).Error
	if err != nil {
		return fmt.Errorf("check projects.name_active column: %w", err)
	}
	if n == 1 {
		return nil
	}

	ddl := "ALTER TABLE `projects` ADD COLUMN `name_active` VARCHAR(128) " +
		"GENERATED ALWAYS AS (IF(`archived_at` IS NULL, `name`, NULL)) STORED"
	if err := db.WithContext(ctx).Exec(ddl).Error; err != nil {
		return fmt.Errorf("add projects.name_active column: %w", err)
	}
	log.InfoCtx(ctx, "pm: added projects.name_active generated column", nil)
	return nil
}

// ensureProjectNameActiveUnique 在 projects(org_id, name_active) 建唯一索引。
func ensureProjectNameActiveUnique(ctx context.Context, db *gorm.DB, log logger.LoggerInterface) error {
	var n int
	err := db.WithContext(ctx).Raw(
		"SELECT 1 FROM information_schema.STATISTICS WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ? LIMIT 1",
		"projects", "uk_projects_org_name_active",
	).Scan(&n).Error
	if err != nil {
		return fmt.Errorf("check projects.name_active unique index: %w", err)
	}
	if n == 1 {
		return nil
	}

	ddl := "CREATE UNIQUE INDEX `uk_projects_org_name_active` ON `projects` (`org_id`, `name_active`)"
	if err := db.WithContext(ctx).Exec(ddl).Error; err != nil {
		return fmt.Errorf("create projects.name_active unique index: %w", err)
	}
	log.InfoCtx(ctx, "pm: created uk_projects_org_name_active unique index", nil)
	return nil
}

// ensureInitiativeNameActiveColumn 同上,但操作 initiatives 表。
func ensureInitiativeNameActiveColumn(ctx context.Context, db *gorm.DB, log logger.LoggerInterface) error {
	var n int
	err := db.WithContext(ctx).Raw(
		"SELECT 1 FROM information_schema.COLUMNS WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ? LIMIT 1",
		"initiatives", "name_active",
	).Scan(&n).Error
	if err != nil {
		return fmt.Errorf("check initiatives.name_active column: %w", err)
	}
	if n == 1 {
		return nil
	}

	ddl := "ALTER TABLE `initiatives` ADD COLUMN `name_active` VARCHAR(128) " +
		"GENERATED ALWAYS AS (IF(`archived_at` IS NULL, `name`, NULL)) STORED"
	if err := db.WithContext(ctx).Exec(ddl).Error; err != nil {
		return fmt.Errorf("add initiatives.name_active column: %w", err)
	}
	log.InfoCtx(ctx, "pm: added initiatives.name_active generated column", nil)
	return nil
}

// ensureInitiativeNameActiveUnique 在 initiatives(project_id, name_active) 建唯一索引。
func ensureInitiativeNameActiveUnique(ctx context.Context, db *gorm.DB, log logger.LoggerInterface) error {
	var n int
	err := db.WithContext(ctx).Raw(
		"SELECT 1 FROM information_schema.STATISTICS WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ? LIMIT 1",
		"initiatives", "uk_initiatives_project_name_active",
	).Scan(&n).Error
	if err != nil {
		return fmt.Errorf("check initiatives.name_active unique index: %w", err)
	}
	if n == 1 {
		return nil
	}

	ddl := "CREATE UNIQUE INDEX `uk_initiatives_project_name_active` ON `initiatives` (`project_id`, `name_active`)"
	if err := db.WithContext(ctx).Exec(ddl).Error; err != nil {
		return fmt.Errorf("create initiatives.name_active unique index: %w", err)
	}
	log.InfoCtx(ctx, "pm: created uk_initiatives_project_name_active unique index", nil)
	return nil
}

// ensureVersionStatusBackfill 把 versions.status 从老枚举值升级到新枚举值:
//
//	planned     → planning
//	in_progress → active
//
// 幂等:UPDATE 撞不到行就 0 影响。新装数据库 versions 表为空,no-op。
func ensureVersionStatusBackfill(ctx context.Context, db *gorm.DB, log logger.LoggerInterface) error {
	res := db.WithContext(ctx).Exec(
		"UPDATE `versions` SET `status` = 'planning' WHERE `status` = 'planned'",
	)
	if res.Error != nil {
		return fmt.Errorf("backfill versions.status planned→planning: %w", res.Error)
	}
	if res.RowsAffected > 0 {
		log.InfoCtx(ctx, "pm: backfilled versions.status", map[string]any{
			"old_value": "planned", "new_value": "planning", "rows": res.RowsAffected,
		})
	}

	res = db.WithContext(ctx).Exec(
		"UPDATE `versions` SET `status` = 'active' WHERE `status` = 'in_progress'",
	)
	if res.Error != nil {
		return fmt.Errorf("backfill versions.status in_progress→active: %w", res.Error)
	}
	if res.RowsAffected > 0 {
		log.InfoCtx(ctx, "pm: backfilled versions.status", map[string]any{
			"old_value": "in_progress", "new_value": "active", "rows": res.RowsAffected,
		})
	}

	return nil
}
