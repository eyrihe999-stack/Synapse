// member.go Repository 接口中 Member 资源的实现。
//
// M4:CreateMember / DeleteMember / UpdateMemberRole 在同事务里写一条
// permission_audit_log(action 见 model.AuditActionMember*)。actor_user_id
// 由 audit writer 从 ctx 读(logger.GetUserID)。
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/organization/model"
	"github.com/eyrihe999-stack/Synapse/internal/permission/audit"
	"gorm.io/gorm"
)

// CreateMember 创建一条成员关系,同事务写 member.add audit。
//
// 嵌套事务安全:gorm.Transaction 在已有 tx 内会用 savepoint,不重新开 tx。
func (r *gormRepository) CreateMember(ctx context.Context, member *model.OrgMember) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(member).Error; err != nil {
			return fmt.Errorf("create member: %w", err)
		}
		return audit.Write(ctx, tx, member.OrgID,
			model.AuditActionMemberAdd, model.AuditTargetMember, member.ID,
			nil, memberSnapshot(member), nil,
		)
	})
}

// FindMember 按 (org_id, user_id) 查找唯一成员关系。
// 不存在时返回 gorm.ErrRecordNotFound。
func (r *gormRepository) FindMember(ctx context.Context, orgID, userID uint64) (*model.OrgMember, error) {
	var member model.OrgMember
	if err := r.db.WithContext(ctx).
		Where("org_id = ? AND user_id = ?", orgID, userID).
		First(&member).Error; err != nil {
		return nil, err
	}
	return &member, nil
}

// ListMembersByOrg 分页列出 org 的成员,JOIN users + org_roles 把展示字段回填。
// 返回 (MemberWithProfile 列表, 总数, error)。
func (r *gormRepository) ListMembersByOrg(ctx context.Context, orgID uint64, page, size int) ([]*MemberWithProfile, int64, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	offset := (page - 1) * size

	var total int64
	if err := r.db.WithContext(ctx).
		Model(&model.OrgMember{}).
		Where("org_id = ?", orgID).
		Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count members: %w", err)
	}
	if total == 0 {
		return []*MemberWithProfile{}, 0, nil
	}

	type row struct {
		MemberID        uint64     `gorm:"column:member_id"`
		OrgID           uint64     `gorm:"column:org_id"`
		UserID          uint64     `gorm:"column:user_id"`
		RoleID          uint64     `gorm:"column:role_id"`
		JoinedAt        time.Time  `gorm:"column:joined_at"`
		PrincipalID     uint64     `gorm:"column:principal_id"`
		Email           string     `gorm:"column:email"`
		DisplayName     string     `gorm:"column:display_name"`
		AvatarURL       string     `gorm:"column:avatar_url"`
		Status          int32      `gorm:"column:status"`
		EmailVerifiedAt *time.Time `gorm:"column:email_verified_at"`
		LastLoginAt     *time.Time `gorm:"column:last_login_at"`
		RoleSlug        string     `gorm:"column:role_slug"`
		RoleDisplayName string     `gorm:"column:role_display_name"`
		RoleIsSystem    bool       `gorm:"column:role_is_system"`
	}
	var rows []row
	if err := r.db.WithContext(ctx).Raw(`
		SELECT m.id AS member_id, m.org_id, m.user_id, m.role_id, m.joined_at,
		       COALESCE(u.principal_id, 0)  AS principal_id,
		       COALESCE(u.email, '')        AS email,
		       COALESCE(u.display_name, '') AS display_name,
		       COALESCE(u.avatar_url, '')   AS avatar_url,
		       COALESCE(u.status, 0)        AS status,
		       u.email_verified_at          AS email_verified_at,
		       u.last_login_at              AS last_login_at,
		       COALESCE(r.slug, '')         AS role_slug,
		       COALESCE(r.display_name, '') AS role_display_name,
		       COALESCE(r.is_system, 0)     AS role_is_system
		FROM org_members m
		LEFT JOIN users u     ON u.id = m.user_id
		LEFT JOIN org_roles r ON r.id = m.role_id
		WHERE m.org_id = ?
		ORDER BY m.joined_at ASC
		LIMIT ? OFFSET ?
	`, orgID, size, offset).Scan(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("list members: %w", err)
	}

	out := make([]*MemberWithProfile, 0, len(rows))
	for _, rr := range rows {
		out = append(out, &MemberWithProfile{
			Member: &model.OrgMember{
				ID:       rr.MemberID,
				OrgID:    rr.OrgID,
				UserID:   rr.UserID,
				RoleID:   rr.RoleID,
				JoinedAt: rr.JoinedAt,
			},
			PrincipalID:     rr.PrincipalID,
			Email:           rr.Email,
			DisplayName:     rr.DisplayName,
			AvatarURL:       rr.AvatarURL,
			Status:          rr.Status,
			EmailVerifiedAt: rr.EmailVerifiedAt,
			LastLoginAt:     rr.LastLoginAt,
			RoleSlug:        rr.RoleSlug,
			RoleDisplayName: rr.RoleDisplayName,
			RoleIsSystem:    rr.RoleIsSystem,
		})
	}
	return out, total, nil
}

// UpdateMemberRole 更新 (org_id, user_id) 成员的 role_id,同事务写 member.role_change audit。
//
// 不存在或 role_id 没变 → 不写 audit。
func (r *gormRepository) UpdateMemberRole(ctx context.Context, orgID, userID, roleID uint64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before model.OrgMember
		if err := tx.Where("org_id = ? AND user_id = ?", orgID, userID).First(&before).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				// service 层有 FindMember 预检;走到这里也按 no-op 处理(不报错不写 audit)
				return nil
			}
			return fmt.Errorf("find member for role change: %w", err)
		}
		if before.RoleID == roleID {
			return nil
		}
		afterSnap := before
		afterSnap.RoleID = roleID

		if err := tx.Model(&model.OrgMember{}).
			Where("org_id = ? AND user_id = ?", orgID, userID).
			Update("role_id", roleID).Error; err != nil {
			return fmt.Errorf("update member role: %w", err)
		}
		return audit.Write(ctx, tx, before.OrgID,
			model.AuditActionMemberRoleChange, model.AuditTargetMember, before.ID,
			memberSnapshot(&before), memberSnapshot(&afterSnap),
			map[string]any{
				"old_role_id": before.RoleID,
				"new_role_id": roleID,
			},
		)
	})
}

// CountMembersByOrg 统计某 org 的成员数。
func (r *gormRepository) CountMembersByOrg(ctx context.Context, orgID uint64) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).
		Model(&model.OrgMember{}).
		Where("org_id = ?", orgID).
		Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count members by org: %w", err)
	}
	return count, nil
}

// CountMembersByUser 统计某用户加入的 active org 数量。
// 通过 JOIN orgs 过滤掉 dissolved org。
func (r *gormRepository) CountMembersByUser(ctx context.Context, userID uint64) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Raw(`
		SELECT COUNT(*) FROM org_members m
		INNER JOIN orgs o ON o.id = m.org_id
		WHERE m.user_id = ? AND o.status = ?
	`, userID, model.OrgStatusActive).Scan(&count).Error
	if err != nil {
		return 0, fmt.Errorf("count joined orgs: %w", err)
	}
	return count, nil
}

// DeleteMember 删除一条成员关系(踢出或主动退出),同事务写 member.remove audit。
//
// 不存在 → no-op,不写 audit(保持原有幂等语义)。actor==target 由 audit row
// 的 actor_user_id == metadata.user_id 区分(self-leave vs kicked)。
func (r *gormRepository) DeleteMember(ctx context.Context, orgID, userID uint64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before model.OrgMember
		err := tx.Where("org_id = ? AND user_id = ?", orgID, userID).First(&before).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // 幂等
		}
		if err != nil {
			return fmt.Errorf("find member before delete: %w", err)
		}
		res := tx.Where("org_id = ? AND user_id = ?", orgID, userID).Delete(&model.OrgMember{})
		if res.Error != nil {
			return fmt.Errorf("delete member: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return nil // 并发被别人删了
		}
		return audit.Write(ctx, tx, before.OrgID,
			model.AuditActionMemberRemove, model.AuditTargetMember, before.ID,
			memberSnapshot(&before), nil,
			map[string]any{"user_id": before.UserID},
		)
	})
}

// memberSnapshot 把 OrgMember 转为 audit before/after 用的快照。
func memberSnapshot(m *model.OrgMember) map[string]any {
	return map[string]any{
		"id":        m.ID,
		"org_id":    m.OrgID,
		"user_id":   m.UserID,
		"role_id":   m.RoleID,
		"joined_at": m.JoinedAt.Unix(),
	}
}

// DeleteMembersByOrg 删除 org 下所有成员(解散时级联)。
func (r *gormRepository) DeleteMembersByOrg(ctx context.Context, orgID uint64) error {
	if err := r.db.WithContext(ctx).
		Where("org_id = ?", orgID).
		Delete(&model.OrgMember{}).Error; err != nil {
		return fmt.Errorf("delete members by org: %w", err)
	}
	return nil
}
