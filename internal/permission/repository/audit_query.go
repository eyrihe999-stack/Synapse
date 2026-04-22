// audit_query.go 审计日志的查询实现(M6)。
//
// 写入由 internal/permission/audit/writer.go 负责;本文件只读。
package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/eyrihe999-stack/Synapse/internal/permission/model"
)

// ListAuditLogByOrg 见接口注释。
//
// 实现:WHERE 拼接条件 + ORDER BY id DESC + LIMIT (limit+1) 探测 hasMore。
// 选择 id 作为 keyset(自增主键,严格唯一且单调,比 created_at 更适合分页)。
func (r *gormRepository) ListAuditLogByOrg(ctx context.Context, orgID uint64, filter AuditFilter) ([]*model.PermissionAuditLog, bool, error) {
	const defaultLimit, maxLimit = 20, 100
	limit := filter.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	q := r.db.WithContext(ctx).
		Model(&model.PermissionAuditLog{}).
		Where("org_id = ?", orgID)

	if filter.ActorUserID > 0 {
		q = q.Where("actor_user_id = ?", filter.ActorUserID)
	}
	if filter.TargetType != "" {
		q = q.Where("target_type = ?", filter.TargetType)
	}
	if filter.TargetID > 0 {
		q = q.Where("target_id = ?", filter.TargetID)
	}
	if filter.Action != "" {
		q = q.Where("action = ?", filter.Action)
	}
	if p := strings.TrimSpace(filter.ActionPrefix); p != "" {
		// LIKE 通配符转义:不允许调用方传入的 prefix 里有 % / _ 误命中。
		// MySQL 字符串字面量里 \ 本身是 escape 字符,所以 ESCAPE '\\' 在 SQL 里需要写成
		// '\\\\' 才能传一个真实的反斜杠给 MySQL parser。
		esc := escapeLike(p)
		q = q.Where(`action LIKE ? ESCAPE '\\'`, esc+"%")
	}
	if filter.BeforeID > 0 {
		q = q.Where("id < ?", filter.BeforeID)
	}

	// 多取一条探测 hasMore
	var rows []*model.PermissionAuditLog
	if err := q.Order("id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, false, fmt.Errorf("list audit log: %w", err)
	}
	hasMore := false
	if len(rows) > limit {
		hasMore = true
		rows = rows[:limit]
	}
	return rows, hasMore, nil
}

// escapeLike 转义 LIKE 通配符 % / _ / \,搭配 SQL 的 ESCAPE '\\' 子句生效。
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
