// role_history.go Repository 接口中角色变更历史资源的实现。
package repository

import (
	"context"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/organization/model"
)

// AppendRoleHistory 追加一条角色变更历史。
// 这是 append-only 日志,不存在 Update / Delete。
func (r *gormRepository) AppendRoleHistory(ctx context.Context, entry *model.OrgMemberRoleHistory) error {
	if err := r.db.WithContext(ctx).Create(entry).Error; err != nil {
		return fmt.Errorf("append role history: %w", err)
	}
	return nil
}

// ListRoleHistoryByMember 列出某成员的角色变更历史,按时间倒序,最多返回 limit 条。
// limit <= 0 时使用默认值 50。
func (r *gormRepository) ListRoleHistoryByMember(ctx context.Context, orgID, userID uint64, limit int) ([]*model.OrgMemberRoleHistory, error) {
	if limit <= 0 {
		limit = 50
	}
	var list []*model.OrgMemberRoleHistory
	if err := r.db.WithContext(ctx).
		Where("org_id = ? AND user_id = ?", orgID, userID).
		Order("created_at DESC").
		Limit(limit).
		Find(&list).Error; err != nil {
		return nil, fmt.Errorf("list role history: %w", err)
	}
	return list, nil
}
