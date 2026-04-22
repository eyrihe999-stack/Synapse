// role.go Repository 接口中 OrgRole 资源的实现。
//
// M5:CreateRole / UpdateRoleDisplayName / UpdateRolePermissions / DeleteRole
// 同事务写一条 permission_audit_log(action 见 model.AuditActionRole*)。
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/organization/model"
	"github.com/eyrihe999-stack/Synapse/internal/permission/audit"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CreateRole 创建一条角色记录,同事务写 role.create audit。
//
// (org_id, slug) 冲突由 uk_roles_org_slug 唯一索引保证;service 层应先查重给出友好错误。
//
// 系统角色 seed(SeedSystemRolesForOrg / 老 batch seed)走自己的路径,不经此方法,
// 因此不会写 audit(系统初始化操作不需要审计)。
func (r *gormRepository) CreateRole(ctx context.Context, role *model.OrgRole) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(role).Error; err != nil {
			return fmt.Errorf("create role: %w", err)
		}
		return audit.Write(ctx, tx, role.OrgID,
			model.AuditActionRoleCreate, model.AuditTargetRole, role.ID,
			nil, roleSnapshot(role), nil,
		)
	})
}

// FindRoleByID 按主键查找角色。
// 不存在时返回 gorm.ErrRecordNotFound,service 层应翻译为 ErrRoleNotFound。
func (r *gormRepository) FindRoleByID(ctx context.Context, id uint64) (*model.OrgRole, error) {
	var role model.OrgRole
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&role).Error; err != nil {
		return nil, err
	}
	return &role, nil
}

// FindRoleByOrgAndSlug 在某 org 内按 slug 查找角色。
func (r *gormRepository) FindRoleByOrgAndSlug(ctx context.Context, orgID uint64, slug string) (*model.OrgRole, error) {
	var role model.OrgRole
	if err := r.db.WithContext(ctx).
		Where("org_id = ? AND slug = ?", orgID, slug).
		First(&role).Error; err != nil {
		return nil, err
	}
	return &role, nil
}

// ListRolesByOrg 列出某 org 的所有角色。
// 排序:is_system DESC(系统角色在前),然后自定义角色按 slug ASC;
// 三条系统角色按 (owner, admin, member) 的固定顺序(MySQL FIELD 函数硬编码)。
func (r *gormRepository) ListRolesByOrg(ctx context.Context, orgID uint64) ([]*model.OrgRole, error) {
	var roles []*model.OrgRole
	err := r.db.WithContext(ctx).
		Where("org_id = ?", orgID).
		Order(clause.Expr{
			SQL: "is_system DESC, FIELD(slug, ?, ?, ?) DESC, slug ASC",
			Vars: []any{
				model.SystemRoleSlugOwner,
				model.SystemRoleSlugAdmin,
				model.SystemRoleSlugMember,
			},
		}).
		Find(&roles).Error
	if err != nil {
		return nil, fmt.Errorf("list roles by org: %w", err)
	}
	return roles, nil
}

// UpdateRoleDisplayName 只更新 display_name 字段,同事务写 role.update audit。
//
// no-op(displayName 没变) → 不写 audit。
func (r *gormRepository) UpdateRoleDisplayName(ctx context.Context, id uint64, displayName string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before model.OrgRole
		if err := tx.Where("id = ?", id).First(&before).Error; err != nil {
			return err
		}
		if before.DisplayName == displayName {
			return nil
		}
		afterSnap := before
		afterSnap.DisplayName = displayName
		afterSnap.UpdatedAt = time.Now().UTC()

		if err := tx.Model(&model.OrgRole{}).
			Where("id = ?", id).
			Updates(map[string]any{
				"display_name": displayName,
				"updated_at":   afterSnap.UpdatedAt,
			}).Error; err != nil {
			return fmt.Errorf("update role display name: %w", err)
		}
		return audit.Write(ctx, tx, before.OrgID,
			model.AuditActionRoleUpdate, model.AuditTargetRole, before.ID,
			roleSnapshot(&before), roleSnapshot(&afterSnap), nil,
		)
	})
}

// UpdateRolePermissions 替换 role 的 permissions 字段为 newPerms,同事务写 role.permissions_change audit。
//
// 不区分系统角色 / 自定义角色 —— 调用方(service)负责判定权限上限和谁可以改谁。
// no-op(perms 集合相同) → 不写 audit。
func (r *gormRepository) UpdateRolePermissions(ctx context.Context, id uint64, newPerms []string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before model.OrgRole
		if err := tx.Where("id = ?", id).First(&before).Error; err != nil {
			return err
		}
		if equalStringSets(before.Permissions, newPerms) {
			return nil
		}
		afterSnap := before
		afterSnap.Permissions = model.PermissionSet(newPerms)
		afterSnap.UpdatedAt = time.Now().UTC()

		if err := tx.Model(&model.OrgRole{}).
			Where("id = ?", id).
			Updates(map[string]any{
				"permissions": model.PermissionSet(newPerms),
				"updated_at":  afterSnap.UpdatedAt,
			}).Error; err != nil {
			return fmt.Errorf("update role permissions: %w", err)
		}
		return audit.Write(ctx, tx, before.OrgID,
			model.AuditActionRolePermissionsChange, model.AuditTargetRole, before.ID,
			roleSnapshot(&before), roleSnapshot(&afterSnap),
			map[string]any{
				"is_system": before.IsSystem,
				"role_slug": before.Slug,
			},
		)
	})
}

// DeleteRole 硬删除一条角色,同事务写 role.delete audit。
// 调用方须先保证无成员挂该角色。
func (r *gormRepository) DeleteRole(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before model.OrgRole
		if err := tx.Where("id = ?", id).First(&before).Error; err != nil {
			return err
		}
		res := tx.Where("id = ?", id).Delete(&model.OrgRole{})
		if res.Error != nil {
			return fmt.Errorf("delete role: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return audit.Write(ctx, tx, before.OrgID,
			model.AuditActionRoleDelete, model.AuditTargetRole, before.ID,
			roleSnapshot(&before), nil, nil,
		)
	})
}

// roleSnapshot 把 OrgRole 转为 audit 用的快照。
func roleSnapshot(r *model.OrgRole) map[string]any {
	perms := []string(r.Permissions)
	if perms == nil {
		perms = []string{}
	}
	return map[string]any{
		"id":           r.ID,
		"org_id":       r.OrgID,
		"slug":         r.Slug,
		"display_name": r.DisplayName,
		"is_system":    r.IsSystem,
		"permissions":  perms,
		"created_at":   r.CreatedAt.Unix(),
		"updated_at":   r.UpdatedAt.Unix(),
	}
}

// equalStringSets 判断两个 []string 作为集合是否相等(顺序无关,允许 nil/空互等)。
func equalStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	m := make(map[string]struct{}, len(a))
	for _, x := range a {
		m[x] = struct{}{}
	}
	for _, y := range b {
		if _, ok := m[y]; !ok {
			return false
		}
	}
	return true
}

// CountCustomRolesByOrg 统计某 org 的自定义角色数(is_system=false)。
func (r *gormRepository) CountCustomRolesByOrg(ctx context.Context, orgID uint64) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).
		Model(&model.OrgRole{}).
		Where("org_id = ? AND is_system = ?", orgID, false).
		Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count custom roles: %w", err)
	}
	return count, nil
}

// CountMembersByRole 统计某 role 下的成员数,删除前用。
func (r *gormRepository) CountMembersByRole(ctx context.Context, roleID uint64) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).
		Model(&model.OrgMember{}).
		Where("role_id = ?", roleID).
		Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count members by role: %w", err)
	}
	return count, nil
}

// SeedSystemRolesForOrg 给一个 org 幂等插入三条系统角色,返回 slug→OrgRole 的映射。
// 已存在(同名 slug)的不重复插入,但仍会加载回 map。
func (r *gormRepository) SeedSystemRolesForOrg(ctx context.Context, orgID uint64) (map[string]*model.OrgRole, error) {
	out := make(map[string]*model.OrgRole, 3)
	for _, def := range []struct {
		Slug        string
		DisplayName string
	}{
		{Slug: model.SystemRoleSlugOwner, DisplayName: "Owner"},
		{Slug: model.SystemRoleSlugAdmin, DisplayName: "Admin"},
		{Slug: model.SystemRoleSlugMember, DisplayName: "Member"},
	} {
		role := &model.OrgRole{
			OrgID:       orgID,
			Slug:        def.Slug,
			DisplayName: def.DisplayName,
			IsSystem:    true,
		}
		if err := r.db.WithContext(ctx).Create(role).Error; err != nil {
			// 同 org 同 slug 唯一约束冲突 → 回读已存在的行
			existing, findErr := r.FindRoleByOrgAndSlug(ctx, orgID, def.Slug)
			if findErr != nil && !errors.Is(findErr, gorm.ErrRecordNotFound) {
				return nil, fmt.Errorf("seed %s: create failed and re-read failed: %w", def.Slug, findErr)
			}
			if existing != nil {
				out[def.Slug] = existing
				continue
			}
			return nil, fmt.Errorf("seed system role %s: %w", def.Slug, err)
		}
		out[def.Slug] = role
	}
	return out, nil
}
