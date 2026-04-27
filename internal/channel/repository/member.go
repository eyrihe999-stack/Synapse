package repository

import (
	"context"

	"github.com/eyrihe999-stack/Synapse/internal/channel/model"
)

// AddMember 插入一条 channel_members。撞 PK 返 DB 错误,service 层翻译成
// ErrMemberAlreadyExists。
func (r *gormRepository) AddMember(ctx context.Context, m *model.ChannelMember) error {
	return r.db.WithContext(ctx).Create(m).Error
}

// RemoveMember 按 (channel_id, principal_id) 删除成员。查无记录返 nil,service
// 层按 RowsAffected 判断做"成员不存在"错误翻译。
func (r *gormRepository) RemoveMember(ctx context.Context, channelID, principalID uint64) error {
	return r.db.WithContext(ctx).
		Where("channel_id = ? AND principal_id = ?", channelID, principalID).
		Delete(&model.ChannelMember{}).Error
}

// UpdateMemberRole 改成员角色。service 层负责先校验"最后一个 owner"不可降级。
func (r *gormRepository) UpdateMemberRole(ctx context.Context, channelID, principalID uint64, role string) error {
	return r.db.WithContext(ctx).Model(&model.ChannelMember{}).
		Where("channel_id = ? AND principal_id = ?", channelID, principalID).
		Update("role", role).Error
}

// FindMember 返回指定成员;查无记录返 gorm.ErrRecordNotFound。
func (r *gormRepository) FindMember(ctx context.Context, channelID, principalID uint64) (*model.ChannelMember, error) {
	var m model.ChannelMember
	if err := r.db.WithContext(ctx).
		Where("channel_id = ? AND principal_id = ?", channelID, principalID).
		First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// ListMembers 列出 channel 的所有成员(按 joined_at ASC)。单 channel 成员数
// 不大,不分页。
func (r *gormRepository) ListMembers(ctx context.Context, channelID uint64) ([]model.ChannelMember, error) {
	var ms []model.ChannelMember
	if err := r.db.WithContext(ctx).
		Where("channel_id = ?", channelID).
		Order("joined_at ASC").
		Find(&ms).Error; err != nil {
		return nil, err
	}
	return ms, nil
}

// MemberWithProfile channel 成员 + principal 可读信息聚合。
//
// 用于 orchestrator 组 LLM prompt 时把"这个 principal 是谁、什么身份"一次讲清,
// 让 LLM 能直接 create_task(assignee_principal_id=...) 而不是反问用户"Claude 是谁"。
type MemberWithProfile struct {
	PrincipalID   uint64
	Role          string // owner / member / observer
	DisplayName   string // 优先 users.display_name,其次 agents.display_name,最后 ""
	Kind          string // "user" | "agent_system" | "agent_user" | ""
	IsGlobalAgent bool   // agent.org_id=0(如 synapse-top-orchestrator)
}

// ListMembersWithProfile 跨表 JOIN 拿成员 + principal 可读信息。
//
// 一次 raw SQL 拉齐,避免 orchestrator 层循环 N+1 查。users 和 agents 同一个
// principal_id 不会并存(互斥约束),COALESCE 取到的就是唯一那条。
func (r *gormRepository) ListMembersWithProfile(ctx context.Context, channelID uint64) ([]MemberWithProfile, error) {
	type row struct {
		PrincipalID   uint64 `gorm:"column:principal_id"`
		Role          string `gorm:"column:role"`
		DisplayName   string `gorm:"column:display_name"`
		Kind          string `gorm:"column:kind"`
		IsGlobalAgent bool   `gorm:"column:is_global_agent"`
	}
	var rows []row
	err := r.db.WithContext(ctx).Raw(`
		SELECT cm.principal_id, cm.role,
		       COALESCE(u.display_name, a.display_name, '') AS display_name,
		       CASE
		         WHEN u.id IS NOT NULL THEN 'user'
		         WHEN a.id IS NOT NULL THEN CONCAT('agent_', COALESCE(a.kind, 'system'))
		         ELSE ''
		       END AS kind,
		       CASE WHEN a.id IS NOT NULL AND a.org_id = 0 THEN TRUE ELSE FALSE END AS is_global_agent
		FROM channel_members cm
		LEFT JOIN users  u ON u.principal_id = cm.principal_id
		LEFT JOIN agents a ON a.principal_id = cm.principal_id
		WHERE cm.channel_id = ?
		ORDER BY cm.joined_at ASC
	`, channelID).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]MemberWithProfile, 0, len(rows))
	for _, r := range rows {
		out = append(out, MemberWithProfile{
			PrincipalID:   r.PrincipalID,
			Role:          r.Role,
			DisplayName:   r.DisplayName,
			Kind:          r.Kind,
			IsGlobalAgent: r.IsGlobalAgent,
		})
	}
	return out, nil
}

// CountOwners 返回 channel 里 role=owner 的成员数。给"最后一个 owner"守卫用。
func (r *gormRepository) CountOwners(ctx context.Context, channelID uint64) (int64, error) {
	var n int64
	if err := r.db.WithContext(ctx).Model(&model.ChannelMember{}).
		Where("channel_id = ? AND role = ?", channelID, "owner").
		Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

// LookupUserPrincipalID 按 users.id 反查 principal_id。返 0 表示查无记录。
//
// 直接查 users 表而不经过 internal/user repository:
//   - 避免 channel → user 的跨模块依赖
//   - 只读一列,查询非常轻量
//   - 语义稳定(principal_id 是 PR #1 之后的不变字段)
func (r *gormRepository) LookupUserPrincipalID(ctx context.Context, userID uint64) (uint64, error) {
	var pid uint64
	err := r.db.WithContext(ctx).
		Table("users").
		Select("principal_id").
		Where("id = ?", userID).
		Scan(&pid).Error
	if err != nil {
		return 0, err
	}
	return pid, nil
}
