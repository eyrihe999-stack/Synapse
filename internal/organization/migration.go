// migration.go 组织模块数据库迁移(建表、加索引、历史数据回填)。
package organization

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/organization/model"
	"github.com/eyrihe999-stack/Synapse/internal/permission"
	"gorm.io/gorm"
)

// RunMigrations 执行组织模块的表迁移与索引创建。
//
// 执行步骤:
//  1. AutoMigrate:创建 orgs / org_roles / org_members 表,并给 org_members 增量加 role_id 列
//     (新列声明为 NOT NULL DEFAULT 0,兼容历史行 —— 回填后不会再出现 0)
//  2. seedSystemRolesForAllOrgs:给每个 active org 幂等 INSERT 三条系统角色
//  3. backfillMemberRoles:把 role_id=0 的历史 OrgMember 回填到对应的系统角色
//     - owner member → owner 角色
//     - 其他 member  → member 角色
//  4. EnsureOrgIndexes:幂等创建/补齐所有索引
//
// 所有步骤幂等,可重复跑。onReady 在迁移成功完成后被调用。
//
// 可能的错误:
//   - ErrOrgInternal:AutoMigrate、seed、回填、或索引创建失败时返回
func RunMigrations(ctx context.Context, db *gorm.DB, log logger.LoggerInterface, onReady func()) error {
	if err := db.WithContext(ctx).AutoMigrate(
		&model.Org{},
		&model.OrgRole{},
		&model.OrgMember{},
		&model.OrgInvitation{},
	); err != nil {
		log.ErrorCtx(ctx, "组织模块 AutoMigrate 失败", err, nil)
		return fmt.Errorf("organization auto-migrate: %w: %w", err, ErrOrgInternal)
	}
	log.InfoCtx(ctx, "组织模块 AutoMigrate 完成", nil)

	if err := seedSystemRolesForAllOrgs(ctx, db); err != nil {
		log.ErrorCtx(ctx, "组织模块系统角色 seed 失败", err, nil)
		return fmt.Errorf("organization seed system roles: %w: %w", err, ErrOrgInternal)
	}
	log.InfoCtx(ctx, "组织模块系统角色 seed 完成", nil)

	// M4 backfill:把所有 permissions 字段为空的系统角色行填上对应默认 perm 集。
	// 已经设置过 permissions(包括 owner 手动改过的)被 WHERE 过滤,不覆盖。
	if affected, err := backfillSystemRolePermissions(ctx, db); err != nil {
		log.ErrorCtx(ctx, "组织模块系统角色权限回填失败", err, nil)
		return fmt.Errorf("organization backfill system role permissions: %w: %w", err, ErrOrgInternal)
	} else if affected > 0 {
		log.InfoCtx(ctx, "组织模块系统角色权限回填完成", map[string]any{"rows_updated": affected})
	}

	// M6 ensure:某些 perm 是后续 release 才加的(如 audit.read_all),需要补到老的 owner/admin 行。
	// 这一步幂等:perm 已存在则跳过。和 backfillSystemRolePermissions 不同,这里允许追加到非空 permissions。
	if affected, err := ensureSystemRolePerm(ctx, db, []string{SystemRoleSlugOwner, SystemRoleSlugAdmin}, permission.PermAuditReadAll); err != nil {
		log.ErrorCtx(ctx, "ensure audit.read_all 失败", err, nil)
		return fmt.Errorf("organization ensure audit.read_all: %w: %w", err, ErrOrgInternal)
	} else if affected > 0 {
		log.InfoCtx(ctx, "为老 owner/admin 补充 audit.read_all", map[string]any{"rows_updated": affected})
	}

	// integration.gitlab.manage:仅 owner —— 同步源会消费 owner 自己的凭据,且写入量级 / IO 影响远超
	// 普通成员的 manual_upload。admin 默认不放,避免"管理员不知情下挂起 owner 凭据持续拉数据"的语义陷阱。
	if affected, err := ensureSystemRolePerm(ctx, db, []string{SystemRoleSlugOwner}, permission.PermIntegrationGitLabManage); err != nil {
		log.ErrorCtx(ctx, "ensure integration.gitlab.manage 失败", err, nil)
		return fmt.Errorf("organization ensure integration.gitlab.manage: %w: %w", err, ErrOrgInternal)
	} else if affected > 0 {
		log.InfoCtx(ctx, "为老 owner 补充 integration.gitlab.manage", map[string]any{"rows_updated": affected})
	}

	affected, err := backfillMemberRoles(ctx, db)
	if err != nil {
		log.ErrorCtx(ctx, "组织模块 member.role_id 回填失败", err, nil)
		return fmt.Errorf("organization backfill member roles: %w: %w", err, ErrOrgInternal)
	}
	if affected > 0 {
		log.InfoCtx(ctx, "组织模块 member.role_id 回填完成", map[string]any{"rows_updated": affected})
	}

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

// seedSystemRolesForAllOrgs 给每个 active org 幂等插入三条系统角色。
//
// 用 INSERT IGNORE + SELECT 从 orgs 表出发,配合 org_roles 的唯一索引 uk_roles_org_slug 幂等。
// 已有的角色(包括历史手动插入)不会被覆盖。
func seedSystemRolesForAllOrgs(ctx context.Context, db *gorm.DB) error {
	for _, r := range SystemRoleDefaults {
		// INSERT IGNORE 依赖 org_roles 的 (org_id, slug) 唯一索引 —— 由 AutoMigrate 从 struct tag
		// 创建;EnsureOrgIndexes 之前跑也能命中(MySQL 唯一约束在建表时就生效)。
		err := db.WithContext(ctx).Exec(`
			INSERT IGNORE INTO `+TableOrgRoles+` (org_id, slug, display_name, is_system, created_at, updated_at)
			SELECT o.id, ?, ?, 1, NOW(), NOW()
			FROM `+TableOrgs+` o
			WHERE o.status = ?
		`, r.Slug, r.DisplayName, OrgStatusActive).Error
		if err != nil {
			return fmt.Errorf("seed system role %s: %w", r.Slug, err)
		}
	}
	return nil
}

// backfillSystemRolePermissions 把 permissions 为 NULL / '[]' 的系统角色行
// 回填成对应的默认 perm 集合。
//
// 幂等条件:WHERE 过滤 (permissions IS NULL OR permissions = '[]' OR JSON_LENGTH(permissions) = 0)
// 这样:
//   - 老库刚加列 → permissions 为 '[]'(列默认值)→ 命中,被填上默认
//   - owner 已改过(非空)→ 不命中,不覆盖手动配置
//
// 只处理 is_system=true 的行;custom role 的 permissions 由 admin 通过 role.manage 接口改。
func backfillSystemRolePermissions(ctx context.Context, db *gorm.DB) (int64, error) {
	var total int64
	for _, slug := range []string{
		SystemRoleSlugOwner, SystemRoleSlugAdmin, SystemRoleSlugMember,
	} {
		perms := permission.SystemRoleDefaultPermissions(slug)
		permsJSON, err := json.Marshal(perms)
		if err != nil {
			return 0, fmt.Errorf("marshal default perms for %s: %w", slug, err)
		}
		sql := `
			UPDATE ` + TableOrgRoles + `
			SET permissions = ?, updated_at = NOW()
			WHERE is_system = 1 AND slug = ?
			  AND (permissions IS NULL OR permissions = '[]' OR JSON_LENGTH(permissions) = 0)
		`
		res := db.WithContext(ctx).Exec(sql, string(permsJSON), slug)
		if res.Error != nil {
			return 0, fmt.Errorf("backfill perms for %s: %w", slug, res.Error)
		}
		total += res.RowsAffected
	}
	return total, nil
}

// ensureSystemRolePerm 给指定的若干系统角色 slug,把 perm 追加到它们的 permissions 字段
// (如果还没有的话)。幂等:已经包含 perm 的行不会被改。
//
// 用途:某 release 加新 perm 后(如 M6 加 audit.read_all),让老库的对应系统角色自动获得。
// 自定义角色不动。允许追加到非空 permissions(和 backfillSystemRolePermissions 的"仅空"语义不同)。
func ensureSystemRolePerm(ctx context.Context, db *gorm.DB, slugs []string, perm string) (int64, error) {
	if len(slugs) == 0 {
		return 0, nil
	}
	// JSON_CONTAINS 的第二参数要求是 JSON value(字符串需带引号),用 JSON_QUOTE 包一下。
	sql := `
		UPDATE ` + TableOrgRoles + `
		SET permissions = JSON_ARRAY_APPEND(permissions, '$', ?),
		    updated_at = NOW()
		WHERE is_system = 1
		  AND slug IN ?
		  AND NOT JSON_CONTAINS(permissions, JSON_QUOTE(?))
	`
	res := db.WithContext(ctx).Exec(sql, perm, slugs, perm)
	if res.Error != nil {
		return 0, fmt.Errorf("ensure system role perm %s: %w", perm, res.Error)
	}
	return res.RowsAffected, nil
}

// backfillMemberRoles 把 role_id=0 的 OrgMember 回填到对应的系统角色 id。
//
//  - owner member (user_id = orgs.owner_user_id) → 该 org 的 owner 角色
//  - 其他 member                                 → 该 org 的 member 角色
//
// 返回受影响的行数(两条 UPDATE 之和)。已回填过的行 role_id != 0,WHERE 过滤掉。
func backfillMemberRoles(ctx context.Context, db *gorm.DB) (int64, error) {
	var total int64

	ownerSQL := `
		UPDATE ` + TableOrgMembers + ` m
		INNER JOIN ` + TableOrgs + ` o ON o.id = m.org_id
		INNER JOIN ` + TableOrgRoles + ` r ON r.org_id = m.org_id AND r.slug = ?
		SET m.role_id = r.id
		WHERE m.role_id = 0 AND m.user_id = o.owner_user_id
	`
	res := db.WithContext(ctx).Exec(ownerSQL, SystemRoleSlugOwner)
	if res.Error != nil {
		return 0, fmt.Errorf("backfill owner member role: %w", res.Error)
	}
	total += res.RowsAffected

	memberSQL := `
		UPDATE ` + TableOrgMembers + ` m
		INNER JOIN ` + TableOrgRoles + ` r ON r.org_id = m.org_id AND r.slug = ?
		SET m.role_id = r.id
		WHERE m.role_id = 0
	`
	res = db.WithContext(ctx).Exec(memberSQL, SystemRoleSlugMember)
	if res.Error != nil {
		return 0, fmt.Errorf("backfill regular member role: %w", res.Error)
	}
	total += res.RowsAffected

	return total, nil
}
