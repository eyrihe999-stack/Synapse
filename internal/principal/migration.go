package principal

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/principal/model"
)

// RunMigrations 执行 principal 模块数据库迁移:建 principals 身份根表。
//
// 必须在 user / agents 各自的 RunMigrations 之前跑,因为那两个模块会在自己
// 的 migration 里回填 principals 行并写回 principal_id。
//
// 角色 / 权限不属于本模块:RBAC 走已有的 org_roles + org_members.role_id。
func RunMigrations(ctx context.Context, db *gorm.DB, log logger.LoggerInterface, onReady func()) error {
	log.InfoCtx(ctx, "principal: running MySQL migrations", nil)

	// 一次性清理 principal_roles 表(早期草案产物,对齐现有 RBAC 后废弃,见 §3.5.4)。
	// 该表从未被业务代码写入;幂等地 drop,没有就跳过。
	if err := dropLegacyPrincipalRoles(ctx, db, log); err != nil {
		return fmt.Errorf("principal drop legacy roles table: %w: %w", err, ErrPrincipalInternal)
	}

	if err := db.WithContext(ctx).AutoMigrate(&model.Principal{}); err != nil {
		log.ErrorCtx(ctx, "principal: AutoMigrate failed", err, nil)
		return fmt.Errorf("principal auto-migrate: %w: %w", err, ErrPrincipalInternal)
	}
	log.InfoCtx(ctx, "principal: migrations completed", nil)
	if onReady != nil {
		onReady()
	}
	return nil
}

// Create 在给定 db 句柄上新建一条 Principal 行,返回新 ID。
//
// 提供给 user / agents 模块的 backfill 逻辑复用,不走 repository 层是因为
// 这个操作只在 migration 阶段用,业务运行时 agent / user 的创建还是各自模块
// 自己来(见 §3.5.5 迁移路径,PR #1 不改业务写路径)。
//
// 调用方若需要事务,自己把 db 换成 tx 句柄即可。
func Create(ctx context.Context, db *gorm.DB, p *model.Principal) error {
	if err := db.WithContext(ctx).Create(p).Error; err != nil {
		return fmt.Errorf("principal create: %w: %w", err, ErrPrincipalInternal)
	}
	return nil
}

// dropLegacyPrincipalRoles 幂等地 DROP 早期草案里的 principal_roles 表。
//
// 上下文:PR #1 早期版本(collaboration-design §3.5.6 初稿)曾建议用一张独立的
// principal_roles 表承载"principal × org × role"绑定;后续对齐现有 RBAC 设施
// (org_roles + org_members.role_id)后废弃。该表从未被业务代码写入 —— 如果
// 存在,也必然为空,可安全 DROP。
func dropLegacyPrincipalRoles(ctx context.Context, db *gorm.DB, log logger.LoggerInterface) error {
	var n int
	err := db.WithContext(ctx).Raw(
		"SELECT 1 FROM information_schema.TABLES WHERE table_schema = DATABASE() AND table_name = ? LIMIT 1",
		"principal_roles",
	).Scan(&n).Error
	if err != nil {
		return fmt.Errorf("check principal_roles exists: %w", err)
	}
	if n == 0 {
		return nil
	}
	if err := db.WithContext(ctx).Exec("DROP TABLE `principal_roles`").Error; err != nil {
		return fmt.Errorf("drop principal_roles: %w", err)
	}
	log.InfoCtx(ctx, "principal: dropped legacy principal_roles table", nil)
	return nil
}
