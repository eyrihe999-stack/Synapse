// group.go Repository 接口中 Group 相关方法的实现。
package repository

import (
	"errors"
	"fmt"
	"time"

	"context"

	"github.com/eyrihe999-stack/Synapse/internal/permission/model"
	"gorm.io/gorm"
)

// CreateGroup 创建一条权限组记录。
//
// 流程:
//  1. INSERT into perm_groups
//  2. 同事务写一条 group.create audit(after=group snapshot)
//
// 唯一冲突(同 org 同 name)由 uk_perm_groups_org_name 索引保证;
// service 层应先查重给出友好错误,这里返回 gorm 错误时会回滚整个事务。
func (r *gormRepository) CreateGroup(ctx context.Context, group *model.Group) error {
	if group.CreatedAt.IsZero() {
		group.CreatedAt = time.Now().UTC()
	}
	if group.UpdatedAt.IsZero() {
		group.UpdatedAt = group.CreatedAt
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txr := &gormRepository{db: tx}
		if err := tx.Create(group).Error; err != nil {
			return fmt.Errorf("create group: %w", err)
		}
		return txr.writeAudit(ctx, group.OrgID,
			model.AuditActionGroupCreate, model.AuditTargetGroup, group.ID,
			nil, groupSnapshot(group), nil,
		)
	})
}

// FindGroupByID 按主键查找权限组。
func (r *gormRepository) FindGroupByID(ctx context.Context, id uint64) (*model.Group, error) {
	var g model.Group
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&g).Error; err != nil {
		return nil, err
	}
	return &g, nil
}

// FindGroupByOrgAndName 在某 org 内按 name 查找权限组。
func (r *gormRepository) FindGroupByOrgAndName(ctx context.Context, orgID uint64, name string) (*model.Group, error) {
	var g model.Group
	if err := r.db.WithContext(ctx).
		Where("org_id = ? AND name = ?", orgID, name).
		First(&g).Error; err != nil {
		return nil, err
	}
	return &g, nil
}

// ListGroupsByOrg 分页列出某 org 的所有组(按 name 字典序)。
func (r *gormRepository) ListGroupsByOrg(ctx context.Context, orgID uint64, page, size int) ([]*model.Group, int64, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	var (
		items []*model.Group
		total int64
	)
	q := r.db.WithContext(ctx).Model(&model.Group{}).Where("org_id = ?", orgID)
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count groups: %w", err)
	}
	if err := q.Order("name ASC").
		Offset((page - 1) * size).
		Limit(size).
		Find(&items).Error; err != nil {
		return nil, 0, fmt.Errorf("list groups: %w", err)
	}
	return items, total, nil
}

// ListGroupsByUser 列出某用户在某 org 中加入的所有组。
// JOIN perm_group_members 过滤,按组名字典序返回。
func (r *gormRepository) ListGroupsByUser(ctx context.Context, orgID, userID uint64) ([]*model.Group, error) {
	var items []*model.Group
	err := r.db.WithContext(ctx).
		Table("perm_groups AS g").
		Select("g.*").
		Joins("INNER JOIN perm_group_members m ON m.group_id = g.id AND m.user_id = ?", userID).
		Where("g.org_id = ?", orgID).
		Order("g.name ASC").
		Scan(&items).Error
	if err != nil {
		return nil, fmt.Errorf("list groups by user: %w", err)
	}
	return items, nil
}

// UpdateGroupName 更新组名,同事务写 group.rename audit。
func (r *gormRepository) UpdateGroupName(ctx context.Context, groupID uint64, newName string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txr := &gormRepository{db: tx}
		// 先取 before 快照
		var before model.Group
		if err := tx.Where("id = ?", groupID).First(&before).Error; err != nil {
			return err
		}
		if before.Name == newName {
			return nil // no-op,不写 audit
		}
		afterSnap := before
		afterSnap.Name = newName
		afterSnap.UpdatedAt = time.Now().UTC()

		if err := tx.Model(&model.Group{}).
			Where("id = ?", groupID).
			Updates(map[string]any{
				"name":       newName,
				"updated_at": afterSnap.UpdatedAt,
			}).Error; err != nil {
			return fmt.Errorf("update group name: %w", err)
		}
		return txr.writeAudit(ctx, before.OrgID,
			model.AuditActionGroupRename, model.AuditTargetGroup, before.ID,
			groupSnapshot(&before), groupSnapshot(&afterSnap), nil,
		)
	})
}

// DeleteGroup 删除组(级联删除所有成员关系),同事务写 group.delete audit。
//
// audit 的 before 是删除前组本体快照;级联删的成员关系不单独写 member_remove 行
// (语义上"删组"是单一动作,前端按 action=group.delete 知道连带成员清空)。
func (r *gormRepository) DeleteGroup(ctx context.Context, groupID uint64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txr := &gormRepository{db: tx}
		var before model.Group
		if err := tx.Where("id = ?", groupID).First(&before).Error; err != nil {
			return err
		}
		// 级联删成员
		if err := tx.Where("group_id = ?", groupID).Delete(&model.GroupMember{}).Error; err != nil {
			return fmt.Errorf("delete group members: %w", err)
		}
		// 删组本体
		res := tx.Where("id = ?", groupID).Delete(&model.Group{})
		if res.Error != nil {
			return fmt.Errorf("delete group: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return txr.writeAudit(ctx, before.OrgID,
			model.AuditActionGroupDelete, model.AuditTargetGroup, before.ID,
			groupSnapshot(&before), nil, nil,
		)
	})
}

// CountGroupsByOrg 统计某 org 的组数。
func (r *gormRepository) CountGroupsByOrg(ctx context.Context, orgID uint64) (int64, error) {
	var n int64
	if err := r.db.WithContext(ctx).
		Model(&model.Group{}).
		Where("org_id = ?", orgID).
		Count(&n).Error; err != nil {
		return 0, fmt.Errorf("count groups: %w", err)
	}
	return n, nil
}

// AddGroupMember 把 user 加入组,同事务写 group.member_add audit。
//
// 复合主键 (group_id, user_id) 冲突时返回 gorm 的 duplicate key error,
// service 层翻译为 ErrGroupMemberExists。
func (r *gormRepository) AddGroupMember(ctx context.Context, groupID, userID uint64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txr := &gormRepository{db: tx}
		// 取 group 拿 org_id(audit 必填)
		var g model.Group
		if err := tx.Where("id = ?", groupID).First(&g).Error; err != nil {
			return err
		}
		mem := &model.GroupMember{
			GroupID:  groupID,
			UserID:   userID,
			JoinedAt: time.Now().UTC(),
		}
		if err := tx.Create(mem).Error; err != nil {
			return fmt.Errorf("add group member: %w", err)
		}
		return txr.writeAudit(ctx, g.OrgID,
			model.AuditActionGroupMemberAdd, model.AuditTargetGroupMember, groupID,
			nil, memberSnapshot(mem),
			map[string]any{"group_id": groupID, "group_name": g.Name, "user_id": userID},
		)
	})
}

// RemoveGroupMember 把 user 从组移除,同事务写 group.member_remove audit。
//
// 不存在时返回 gorm.ErrRecordNotFound 由 service 层翻译。
func (r *gormRepository) RemoveGroupMember(ctx context.Context, groupID, userID uint64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txr := &gormRepository{db: tx}
		var g model.Group
		if err := tx.Where("id = ?", groupID).First(&g).Error; err != nil {
			return err
		}
		// 取 before 行用于 audit
		var before model.GroupMember
		err := tx.Where("group_id = ? AND user_id = ?", groupID, userID).First(&before).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return gorm.ErrRecordNotFound
		}
		if err != nil {
			return fmt.Errorf("load member before delete: %w", err)
		}
		res := tx.Where("group_id = ? AND user_id = ?", groupID, userID).
			Delete(&model.GroupMember{})
		if res.Error != nil {
			return fmt.Errorf("remove group member: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return txr.writeAudit(ctx, g.OrgID,
			model.AuditActionGroupMemberRemove, model.AuditTargetGroupMember, groupID,
			memberSnapshot(&before), nil,
			map[string]any{"group_id": groupID, "group_name": g.Name, "user_id": userID},
		)
	})
}

// IsGroupMember 判断 user 是否在组中。
func (r *gormRepository) IsGroupMember(ctx context.Context, groupID, userID uint64) (bool, error) {
	var n int64
	if err := r.db.WithContext(ctx).
		Model(&model.GroupMember{}).
		Where("group_id = ? AND user_id = ?", groupID, userID).
		Count(&n).Error; err != nil {
		return false, fmt.Errorf("check group member: %w", err)
	}
	return n > 0, nil
}

// CountGroupMembers 统计某组的成员数。
func (r *gormRepository) CountGroupMembers(ctx context.Context, groupID uint64) (int64, error) {
	var n int64
	if err := r.db.WithContext(ctx).
		Model(&model.GroupMember{}).
		Where("group_id = ?", groupID).
		Count(&n).Error; err != nil {
		return 0, fmt.Errorf("count group members: %w", err)
	}
	return n, nil
}

// ListGroupMembers 分页列出某组的成员(按 joined_at ASC)。
func (r *gormRepository) ListGroupMembers(ctx context.Context, groupID uint64, page, size int) ([]*model.GroupMember, int64, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	var (
		items []*model.GroupMember
		total int64
	)
	q := r.db.WithContext(ctx).Model(&model.GroupMember{}).Where("group_id = ?", groupID)
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count members: %w", err)
	}
	if err := q.Order("joined_at ASC").
		Offset((page - 1) * size).
		Limit(size).
		Find(&items).Error; err != nil {
		return nil, 0, fmt.Errorf("list members: %w", err)
	}
	return items, total, nil
}

// ─── snapshot 辅助 ────────────────────────────────────────────────────────────
//
// audit 的 before/after 字段直接 JSON-marshal 这两个 struct;字段名固定,前端可解析。

// groupSnapshot 把 Group 转为 audit 用的快照。
func groupSnapshot(g *model.Group) map[string]any {
	return map[string]any{
		"id":            g.ID,
		"org_id":        g.OrgID,
		"name":          g.Name,
		"owner_user_id": g.OwnerUserID,
		"created_at":    g.CreatedAt.Unix(),
		"updated_at":    g.UpdatedAt.Unix(),
	}
}

// memberSnapshot 把 GroupMember 转为 audit 用的快照。
func memberSnapshot(m *model.GroupMember) map[string]any {
	return map[string]any{
		"group_id":  m.GroupID,
		"user_id":   m.UserID,
		"joined_at": m.JoinedAt.Unix(),
	}
}
