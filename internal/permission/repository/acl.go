// acl.go Repository 接口中 ResourceACL 资源的实现。
package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/permission/audit"
	"github.com/eyrihe999-stack/Synapse/internal/permission/model"
	"gorm.io/gorm"
)

// GrantACL 创建一条 ACL 行,同事务写一条 audit。
//
// auditAction / auditTarget 由调用方传入(各模块自定):
//   - source 模块用 source.AuditActionSourceACLGrant + source.AuditTargetSourceACL
//   - 未来其他资源类型走自己的 action / target 名
func (r *gormRepository) GrantACL(ctx context.Context, acl *model.ResourceACL, auditAction, auditTarget string) error {
	if acl.CreatedAt.IsZero() {
		acl.CreatedAt = time.Now().UTC()
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(acl).Error; err != nil {
			return fmt.Errorf("grant acl: %w", err)
		}
		return audit.Write(ctx, tx, acl.OrgID,
			auditAction, auditTarget, acl.ID,
			nil, aclSnapshot(acl),
			map[string]any{
				"resource_type": acl.ResourceType,
				"resource_id":   acl.ResourceID,
				"subject_type":  acl.SubjectType,
				"subject_id":    acl.SubjectID,
				"permission":    acl.Permission,
			},
		)
	})
}

// FindACLByID 按主键查找。
func (r *gormRepository) FindACLByID(ctx context.Context, id uint64) (*model.ResourceACL, error) {
	var a model.ResourceACL
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

// FindACL 按 (resource, subject) 精确查找。
func (r *gormRepository) FindACL(ctx context.Context, resourceType string, resourceID uint64, subjectType string, subjectID uint64) (*model.ResourceACL, error) {
	var a model.ResourceACL
	err := r.db.WithContext(ctx).
		Where("resource_type = ? AND resource_id = ? AND subject_type = ? AND subject_id = ?",
			resourceType, resourceID, subjectType, subjectID).
		First(&a).Error
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// ListACLByResource 列出某资源的所有 ACL(按 created_at ASC)。
func (r *gormRepository) ListACLByResource(ctx context.Context, resourceType string, resourceID uint64) ([]*model.ResourceACL, error) {
	var items []*model.ResourceACL
	err := r.db.WithContext(ctx).
		Where("resource_type = ? AND resource_id = ?", resourceType, resourceID).
		Order("created_at ASC").
		Find(&items).Error
	if err != nil {
		return nil, fmt.Errorf("list acl by resource: %w", err)
	}
	return items, nil
}

// UpdateACLPermission 改 permission;若 newPermission == 当前值则 no-op 不写 audit。
func (r *gormRepository) UpdateACLPermission(ctx context.Context, aclID uint64, newPermission, auditAction, auditTarget string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before model.ResourceACL
		if err := tx.Where("id = ?", aclID).First(&before).Error; err != nil {
			return err
		}
		if before.Permission == newPermission {
			return nil
		}
		afterSnap := before
		afterSnap.Permission = newPermission

		if err := tx.Model(&model.ResourceACL{}).
			Where("id = ?", aclID).
			Update("permission", newPermission).Error; err != nil {
			return fmt.Errorf("update acl permission: %w", err)
		}
		return audit.Write(ctx, tx, before.OrgID,
			auditAction, auditTarget, before.ID,
			aclSnapshot(&before), aclSnapshot(&afterSnap),
			map[string]any{
				"resource_type":  before.ResourceType,
				"resource_id":    before.ResourceID,
				"subject_type":   before.SubjectType,
				"subject_id":     before.SubjectID,
				"old_permission": before.Permission,
				"new_permission": newPermission,
			},
		)
	})
}

// RevokeACL 删除一条 ACL 行,同事务写 audit。
func (r *gormRepository) RevokeACL(ctx context.Context, aclID uint64, auditAction, auditTarget string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before model.ResourceACL
		if err := tx.Where("id = ?", aclID).First(&before).Error; err != nil {
			return err
		}
		res := tx.Where("id = ?", aclID).Delete(&model.ResourceACL{})
		if res.Error != nil {
			return fmt.Errorf("revoke acl: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return audit.Write(ctx, tx, before.OrgID,
			auditAction, auditTarget, before.ID,
			aclSnapshot(&before), nil,
			map[string]any{
				"resource_type": before.ResourceType,
				"resource_id":   before.ResourceID,
				"subject_type":  before.SubjectType,
				"subject_id":    before.SubjectID,
				"permission":    before.Permission,
			},
		)
	})
}

// BulkRevokeACLsByResource 批量删除某资源的所有 ACL 行,同事务为每行写一条 revoke audit。
// 主要场景:删除上层资源(例如 source)时清理遗留 ACL。
//
// 返回实际被删除的行数;无匹配时返回 0、nil(幂等)。
// 每一行审计 metadata 带 bulk_reason="resource deleted" 便于与单条 revoke 区分。
func (r *gormRepository) BulkRevokeACLsByResource(
	ctx context.Context,
	resourceType string,
	resourceID uint64,
	auditAction, auditTarget string,
) (int64, error) {
	var deleted int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []*model.ResourceACL
		if err := tx.Where("resource_type = ? AND resource_id = ?", resourceType, resourceID).
			Find(&rows).Error; err != nil {
			return fmt.Errorf("find acls for bulk revoke: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		res := tx.Where("resource_type = ? AND resource_id = ?", resourceType, resourceID).
			Delete(&model.ResourceACL{})
		if res.Error != nil {
			return fmt.Errorf("bulk revoke acls: %w", res.Error)
		}
		deleted = res.RowsAffected
		for _, row := range rows {
			if err := audit.Write(ctx, tx, row.OrgID,
				auditAction, auditTarget, row.ID,
				aclSnapshot(row), nil,
				map[string]any{
					"resource_type": row.ResourceType,
					"resource_id":   row.ResourceID,
					"subject_type":  row.SubjectType,
					"subject_id":    row.SubjectID,
					"permission":    row.Permission,
					"bulk_reason":   "resource deleted",
				},
			); err != nil {
				return fmt.Errorf("write bulk revoke audit: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}

// ListVisibleResourceIDsBySubjects 给定 subjects → 命中 ACL 的 distinct resource_id 集合。
//
// SQL 大致:
//
//	SELECT DISTINCT resource_id FROM resource_acl
//	WHERE org_id = ? AND resource_type = ?
//	  AND ( (subject_type='group' AND subject_id IN (...))
//	     OR (subject_type='user'  AND subject_id IN (...)) )
//	  AND permission IN (... 满足 minPermission 的级别 ...)
//
// minPermission='read'  → 任何 ACL 行都算 (read+write)
// minPermission='write' → 只算 permission='write'
//
// 空 subjects 直接返空(避免空 IN ()导致 SQL 错)。
func (r *gormRepository) ListVisibleResourceIDsBySubjects(ctx context.Context, orgID uint64, resourceType string, subjects []ACLSubject, minPermission string) ([]uint64, error) {
	if len(subjects) == 0 {
		return nil, nil
	}

	var groupIDs, userIDs []uint64
	for _, s := range subjects {
		switch s.Type {
		case model.ACLSubjectTypeGroup:
			groupIDs = append(groupIDs, s.ID)
		case model.ACLSubjectTypeUser:
			userIDs = append(userIDs, s.ID)
		}
	}
	if len(groupIDs) == 0 && len(userIDs) == 0 {
		return nil, nil
	}

	q := r.db.WithContext(ctx).
		Table("resource_acl").
		Select("DISTINCT resource_id").
		Where("org_id = ? AND resource_type = ?", orgID, resourceType)

	// permission 过滤
	if minPermission == model.ACLPermWrite {
		q = q.Where("permission = ?", model.ACLPermWrite)
	}

	// subject IN clause:OR 拼两类
	switch {
	case len(groupIDs) > 0 && len(userIDs) > 0:
		q = q.Where("(subject_type = ? AND subject_id IN ?) OR (subject_type = ? AND subject_id IN ?)",
			model.ACLSubjectTypeGroup, groupIDs,
			model.ACLSubjectTypeUser, userIDs)
	case len(groupIDs) > 0:
		q = q.Where("subject_type = ? AND subject_id IN ?", model.ACLSubjectTypeGroup, groupIDs)
	case len(userIDs) > 0:
		q = q.Where("subject_type = ? AND subject_id IN ?", model.ACLSubjectTypeUser, userIDs)
	}

	var ids []uint64
	if err := q.Scan(&ids).Error; err != nil {
		return nil, fmt.Errorf("list visible resource ids: %w", err)
	}
	return ids, nil
}

// ListGroupIDsByUser 列出某 user 在某 org 中加入的所有 group id。
//
// PermContextMiddleware 调:每请求一次,塞 ctx 后续判定零打 DB。
func (r *gormRepository) ListGroupIDsByUser(ctx context.Context, orgID, userID uint64) ([]uint64, error) {
	var ids []uint64
	err := r.db.WithContext(ctx).
		Table("perm_groups AS g").
		Select("g.id").
		Joins("INNER JOIN perm_group_members m ON m.group_id = g.id AND m.user_id = ?", userID).
		Where("g.org_id = ?", orgID).
		Scan(&ids).Error
	if err != nil {
		return nil, fmt.Errorf("list group ids by user: %w", err)
	}
	return ids, nil
}

// aclSnapshot 把 ResourceACL 转为 audit 用的快照。
func aclSnapshot(a *model.ResourceACL) map[string]any {
	return map[string]any{
		"id":            a.ID,
		"org_id":        a.OrgID,
		"resource_type": a.ResourceType,
		"resource_id":   a.ResourceID,
		"subject_type":  a.SubjectType,
		"subject_id":    a.SubjectID,
		"permission":    a.Permission,
		"granted_by":    a.GrantedBy,
		"created_at":    a.CreatedAt.Unix(),
	}
}

