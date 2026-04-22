// invitation.go Repository 接口中 OrgInvitation 资源的实现。
package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/organization/model"
)

// CreateInvitation 创建一条邀请记录。
func (r *gormRepository) CreateInvitation(ctx context.Context, inv *model.OrgInvitation) error {
	if err := r.db.WithContext(ctx).Create(inv).Error; err != nil {
		return fmt.Errorf("create invitation: %w", err)
	}
	return nil
}

// FindInvitationByID 按主键查找邀请。
func (r *gormRepository) FindInvitationByID(ctx context.Context, id uint64) (*model.OrgInvitation, error) {
	var inv model.OrgInvitation
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&inv).Error; err != nil {
		return nil, err
	}
	return &inv, nil
}

// FindInvitationByTokenHash 按 token_hash 查找邀请。
func (r *gormRepository) FindInvitationByTokenHash(ctx context.Context, tokenHash string) (*model.OrgInvitation, error) {
	var inv model.OrgInvitation
	if err := r.db.WithContext(ctx).Where("token_hash = ?", tokenHash).First(&inv).Error; err != nil {
		return nil, err
	}
	return &inv, nil
}

// FindPendingInvitation 查找 (org_id, email) 下 status=pending 的邀请。
// email 大小写不敏感比较(LOWER(email) = LOWER(?))。
func (r *gormRepository) FindPendingInvitation(ctx context.Context, orgID uint64, email string) (*model.OrgInvitation, error) {
	var inv model.OrgInvitation
	if err := r.db.WithContext(ctx).
		Where("org_id = ? AND LOWER(email) = LOWER(?) AND status = ?",
			orgID, email, model.InvitationStatusPending).
		First(&inv).Error; err != nil {
		return nil, err
	}
	return &inv, nil
}

// ListInvitationsByOrg 分页列出邀请,JOIN org_roles 带角色展示字段。
// statusFilter 为空时返所有状态;非空按 status 过滤。按 created_at DESC 排序。
func (r *gormRepository) ListInvitationsByOrg(ctx context.Context, orgID uint64, statusFilter string, page, size int) ([]*InvitationWithRole, int64, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	offset := (page - 1) * size

	q := r.db.WithContext(ctx).Model(&model.OrgInvitation{}).Where("org_id = ?", orgID)
	if statusFilter != "" {
		q = q.Where("status = ?", statusFilter)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count invitations: %w", err)
	}
	if total == 0 {
		return []*InvitationWithRole{}, 0, nil
	}

	type row struct {
		InvitationID     uint64    `gorm:"column:invitation_id"`
		OrgID            uint64    `gorm:"column:org_id"`
		InviterUserID    uint64    `gorm:"column:inviter_user_id"`
		Email            string    `gorm:"column:email"`
		RoleID           uint64    `gorm:"column:role_id"`
		TokenHash        string    `gorm:"column:token_hash"`
		Status           string    `gorm:"column:status"`
		ExpiresAt        time.Time `gorm:"column:expires_at"`
		AcceptedAt       *time.Time `gorm:"column:accepted_at"`
		AcceptedUserID   uint64    `gorm:"column:accepted_user_id"`
		CreatedAt        time.Time `gorm:"column:created_at"`
		UpdatedAt        time.Time `gorm:"column:updated_at"`
		RoleSlug         string    `gorm:"column:role_slug"`
		RoleDisplayName  string    `gorm:"column:role_display_name"`
		RoleIsSystem     bool      `gorm:"column:role_is_system"`
	}
	var rows []row

	baseSQL := `
		SELECT i.id AS invitation_id, i.org_id, i.inviter_user_id, i.email, i.role_id,
		       i.token_hash, i.status, i.expires_at, i.accepted_at, i.accepted_user_id,
		       i.created_at, i.updated_at,
		       COALESCE(r.slug, '')         AS role_slug,
		       COALESCE(r.display_name, '') AS role_display_name,
		       COALESCE(r.is_system, 0)     AS role_is_system
		FROM org_invitations i
		LEFT JOIN org_roles r ON r.id = i.role_id
		WHERE i.org_id = ?`
	args := []any{orgID}
	if statusFilter != "" {
		baseSQL += " AND i.status = ?"
		args = append(args, statusFilter)
	}
	baseSQL += " ORDER BY i.created_at DESC LIMIT ? OFFSET ?"
	args = append(args, size, offset)

	if err := r.db.WithContext(ctx).Raw(baseSQL, args...).Scan(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("list invitations: %w", err)
	}

	out := make([]*InvitationWithRole, 0, len(rows))
	for _, rr := range rows {
		out = append(out, &InvitationWithRole{
			Invitation: &model.OrgInvitation{
				ID:             rr.InvitationID,
				OrgID:          rr.OrgID,
				InviterUserID:  rr.InviterUserID,
				Email:          rr.Email,
				RoleID:         rr.RoleID,
				TokenHash:      rr.TokenHash,
				Status:         rr.Status,
				ExpiresAt:      rr.ExpiresAt,
				AcceptedAt:     rr.AcceptedAt,
				AcceptedUserID: rr.AcceptedUserID,
				CreatedAt:      rr.CreatedAt,
				UpdatedAt:      rr.UpdatedAt,
			},
			RoleSlug:        rr.RoleSlug,
			RoleDisplayName: rr.RoleDisplayName,
			RoleIsSystem:    rr.RoleIsSystem,
		})
	}
	return out, total, nil
}

// UpdateInvitationFields 部分更新邀请字段。
func (r *gormRepository) UpdateInvitationFields(ctx context.Context, id uint64, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	if err := r.db.WithContext(ctx).
		Model(&model.OrgInvitation{}).
		Where("id = ?", id).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("update invitation fields: %w", err)
	}
	return nil
}

// ListInvitationsByEmail 查询某 email 收到的所有邀请(被邀请人收件箱)。
// 按 LOWER(email) 大小写不敏感匹配。JOIN orgs + org_roles 带回展示字段。
// 按 i.created_at DESC 排序。
// statusFilter:空 → 所有状态;非空 → 精确过滤。
//
// 只返 orgs.status='active' 的行 —— 组织解散后它发出的邀请从收件箱隐去,
// 语义上"已作废",调用方不会看到 ghost 邀请。
func (r *gormRepository) ListInvitationsByEmail(ctx context.Context, email, statusFilter string) ([]*MyInvitationRow, error) {
	type row struct {
		InvitationID     uint64     `gorm:"column:invitation_id"`
		OrgID            uint64     `gorm:"column:org_id"`
		InviterUserID    uint64     `gorm:"column:inviter_user_id"`
		Email            string     `gorm:"column:email"`
		RoleID           uint64     `gorm:"column:role_id"`
		TokenHash        string     `gorm:"column:token_hash"`
		Status           string     `gorm:"column:status"`
		ExpiresAt        time.Time  `gorm:"column:expires_at"`
		AcceptedAt       *time.Time `gorm:"column:accepted_at"`
		AcceptedUserID   uint64     `gorm:"column:accepted_user_id"`
		CreatedAt        time.Time  `gorm:"column:created_at"`
		UpdatedAt        time.Time  `gorm:"column:updated_at"`
		OrgSlug          string     `gorm:"column:org_slug"`
		OrgDisplayName   string     `gorm:"column:org_display_name"`
		RoleSlug         string     `gorm:"column:role_slug"`
		RoleDisplayName  string     `gorm:"column:role_display_name"`
		RoleIsSystem     bool       `gorm:"column:role_is_system"`
	}
	baseSQL := `
		SELECT i.id AS invitation_id, i.org_id, i.inviter_user_id, i.email, i.role_id,
		       i.token_hash, i.status, i.expires_at, i.accepted_at, i.accepted_user_id,
		       i.created_at, i.updated_at,
		       o.slug AS org_slug, o.display_name AS org_display_name,
		       COALESCE(r.slug, '')         AS role_slug,
		       COALESCE(r.display_name, '') AS role_display_name,
		       COALESCE(r.is_system, 0)     AS role_is_system
		FROM org_invitations i
		INNER JOIN orgs o      ON o.id = i.org_id AND o.status = ?
		LEFT JOIN org_roles r  ON r.id = i.role_id
		WHERE LOWER(i.email) = LOWER(?)`
	args := []any{"active", email}
	if statusFilter != "" {
		baseSQL += " AND i.status = ?"
		args = append(args, statusFilter)
	}
	baseSQL += " ORDER BY i.created_at DESC"

	var rows []row
	if err := r.db.WithContext(ctx).Raw(baseSQL, args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list invitations by email: %w", err)
	}

	out := make([]*MyInvitationRow, 0, len(rows))
	for _, rr := range rows {
		out = append(out, &MyInvitationRow{
			Invitation: &model.OrgInvitation{
				ID:             rr.InvitationID,
				OrgID:          rr.OrgID,
				InviterUserID:  rr.InviterUserID,
				Email:          rr.Email,
				RoleID:         rr.RoleID,
				TokenHash:      rr.TokenHash,
				Status:         rr.Status,
				ExpiresAt:      rr.ExpiresAt,
				AcceptedAt:     rr.AcceptedAt,
				AcceptedUserID: rr.AcceptedUserID,
				CreatedAt:      rr.CreatedAt,
				UpdatedAt:      rr.UpdatedAt,
			},
			OrgSlug:         rr.OrgSlug,
			OrgDisplayName:  rr.OrgDisplayName,
			RoleSlug:        rr.RoleSlug,
			RoleDisplayName: rr.RoleDisplayName,
			RoleIsSystem:    rr.RoleIsSystem,
		})
	}
	return out, nil
}

// ListInvitationsByInviter 查询某用户作为 inviter 发出的邀请(发件箱视图)。
// 跨 org,只返 orgs.status='active' 的行;statusFilter 为空时返全部状态,非空精确过滤。
// JOIN orgs + org_roles 一次性带回展示字段,按 i.created_at DESC 排序。
func (r *gormRepository) ListInvitationsByInviter(ctx context.Context, inviterUserID uint64, statusFilter string) ([]*SentInvitationRow, error) {
	type row struct {
		InvitationID     uint64     `gorm:"column:invitation_id"`
		OrgID            uint64     `gorm:"column:org_id"`
		InviterUserID    uint64     `gorm:"column:inviter_user_id"`
		Email            string     `gorm:"column:email"`
		RoleID           uint64     `gorm:"column:role_id"`
		TokenHash        string     `gorm:"column:token_hash"`
		Status           string     `gorm:"column:status"`
		ExpiresAt        time.Time  `gorm:"column:expires_at"`
		AcceptedAt       *time.Time `gorm:"column:accepted_at"`
		AcceptedUserID   uint64     `gorm:"column:accepted_user_id"`
		CreatedAt        time.Time  `gorm:"column:created_at"`
		UpdatedAt        time.Time  `gorm:"column:updated_at"`
		OrgSlug          string     `gorm:"column:org_slug"`
		OrgDisplayName   string     `gorm:"column:org_display_name"`
		RoleSlug         string     `gorm:"column:role_slug"`
		RoleDisplayName  string     `gorm:"column:role_display_name"`
		RoleIsSystem     bool       `gorm:"column:role_is_system"`
	}
	baseSQL := `
		SELECT i.id AS invitation_id, i.org_id, i.inviter_user_id, i.email, i.role_id,
		       i.token_hash, i.status, i.expires_at, i.accepted_at, i.accepted_user_id,
		       i.created_at, i.updated_at,
		       o.slug AS org_slug, o.display_name AS org_display_name,
		       COALESCE(r.slug, '')         AS role_slug,
		       COALESCE(r.display_name, '') AS role_display_name,
		       COALESCE(r.is_system, 0)     AS role_is_system
		FROM org_invitations i
		INNER JOIN orgs o      ON o.id = i.org_id AND o.status = ?
		LEFT JOIN org_roles r  ON r.id = i.role_id
		WHERE i.inviter_user_id = ?`
	args := []any{"active", inviterUserID}
	if statusFilter != "" {
		baseSQL += " AND i.status = ?"
		args = append(args, statusFilter)
	}
	baseSQL += " ORDER BY i.created_at DESC"

	var rows []row
	if err := r.db.WithContext(ctx).Raw(baseSQL, args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list invitations by inviter: %w", err)
	}

	out := make([]*SentInvitationRow, 0, len(rows))
	for _, rr := range rows {
		out = append(out, &SentInvitationRow{
			Invitation: &model.OrgInvitation{
				ID:             rr.InvitationID,
				OrgID:          rr.OrgID,
				InviterUserID:  rr.InviterUserID,
				Email:          rr.Email,
				RoleID:         rr.RoleID,
				TokenHash:      rr.TokenHash,
				Status:         rr.Status,
				ExpiresAt:      rr.ExpiresAt,
				AcceptedAt:     rr.AcceptedAt,
				AcceptedUserID: rr.AcceptedUserID,
				CreatedAt:      rr.CreatedAt,
				UpdatedAt:      rr.UpdatedAt,
			},
			OrgSlug:         rr.OrgSlug,
			OrgDisplayName:  rr.OrgDisplayName,
			RoleSlug:        rr.RoleSlug,
			RoleDisplayName: rr.RoleDisplayName,
			RoleIsSystem:    rr.RoleIsSystem,
		})
	}
	return out, nil
}

// IsEmailMemberOfOrg 查询某 email 是否已是 org 的成员。
// JOIN users + org_members,按 LOWER(email) 比较。
func (r *gormRepository) IsEmailMemberOfOrg(ctx context.Context, orgID uint64, email string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Raw(`
		SELECT COUNT(*) FROM org_members m
		INNER JOIN users u ON u.id = m.user_id
		WHERE m.org_id = ? AND LOWER(u.email) = ?
	`, orgID, strings.ToLower(email)).Scan(&count).Error
	if err != nil {
		return false, fmt.Errorf("check email is member: %w", err)
	}
	return count > 0, nil
}

// SearchInviteCandidates 在 users 表中搜候选邀请对象。
//
// 只返 users.status = 1(active)的行 —— 常量在 user/model 里定义,但本文件不导入 user
// 包防循环(repo 是底层);直接写字面 1 并加注释说明同步点。
// 如果 users 表 status 枚举后续变动,需要同步改这里。
//
// 三种搜索类型:
//   - email   : LOWER(email) = LOWER(?)    精确,limit 强制 1
//   - user_id : id = ?                     精确,limit 强制 1
//   - name    : display_name LIKE ?        模糊,query 由 service 层包装 '%...%' 后传入
//
// IsMember / HasPendingInvite 用 EXISTS 子查询打标,不 JOIN 避免重复行。
// 返回集按 display_name ASC 排序,保证用户看到稳定的列表。
func (r *gormRepository) SearchInviteCandidates(
	ctx context.Context, orgID uint64, searchType, query string, limit int,
) ([]*InviteCandidate, error) {
	if limit <= 0 {
		limit = 10
	}

	type row struct {
		UserID           uint64 `gorm:"column:user_id"`
		Email            string `gorm:"column:email"`
		DisplayName      string `gorm:"column:display_name"`
		AvatarURL        string `gorm:"column:avatar_url"`
		IsMember         bool   `gorm:"column:is_member"`
		HasPendingInvite bool   `gorm:"column:has_pending_invite"`
	}

	// 共用的 SELECT 头 + EXISTS 子查询。不同 type 只改 WHERE 子句。
	//
	// EXISTS 在 MySQL 下返回 0/1,Scan 进 bool 字段 GORM 自动处理。
	selectPrefix := `
		SELECT u.id AS user_id,
		       u.email,
		       COALESCE(u.display_name, '') AS display_name,
		       COALESCE(u.avatar_url, '')   AS avatar_url,
		       EXISTS(SELECT 1 FROM org_members m
		              WHERE m.org_id = ? AND m.user_id = u.id) AS is_member,
		       EXISTS(SELECT 1 FROM org_invitations i
		              WHERE i.org_id = ?
		                AND LOWER(i.email) = LOWER(u.email)
		                AND i.status = 'pending') AS has_pending_invite
		FROM users u
		WHERE u.status = 1`

	var rows []row
	var err error
	switch searchType {
	case InviteSearchTypeEmail:
		err = r.db.WithContext(ctx).Raw(
			selectPrefix+` AND LOWER(u.email) = LOWER(?) LIMIT 1`,
			orgID, orgID, query,
		).Scan(&rows).Error
	case InviteSearchTypeUserID:
		// query 期望是十进制数字串;service 层已校验过
		err = r.db.WithContext(ctx).Raw(
			selectPrefix+` AND u.id = ? LIMIT 1`,
			orgID, orgID, query,
		).Scan(&rows).Error
	case InviteSearchTypeName:
		// query 预期由 service 层包装成 '%keyword%',这里直接传进 LIKE
		err = r.db.WithContext(ctx).Raw(
			selectPrefix+` AND u.display_name LIKE ? ORDER BY u.display_name ASC LIMIT ?`,
			orgID, orgID, query, limit,
		).Scan(&rows).Error
	default:
		return nil, fmt.Errorf("unknown search type %q", searchType)
	}
	if err != nil {
		return nil, fmt.Errorf("search invite candidates: %w", err)
	}

	out := make([]*InviteCandidate, 0, len(rows))
	for _, rr := range rows {
		out = append(out, &InviteCandidate{
			UserID:           rr.UserID,
			Email:            rr.Email,
			DisplayName:      rr.DisplayName,
			AvatarURL:        rr.AvatarURL,
			IsMember:         rr.IsMember,
			HasPendingInvite: rr.HasPendingInvite,
		})
	}
	return out, nil
}
