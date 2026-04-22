// audit.go 权限组 mutation 的 audit 写入薄包装。
//
// 实际的 audit 行写入逻辑在 internal/permission/audit/writer.go(共享给所有需要写入
// permission_audit_log 的模块);本文件仅给 group.go 提供一个绑定到 *gormRepository.db
// 的快捷调用,避免每个 mutation 方法都重复 import audit 包。
package repository

import (
	"context"

	"github.com/eyrihe999-stack/Synapse/internal/permission/audit"
)

// writeAudit 把当前 repo 持有的 db / tx 交给 audit.Write。
// 调用方需要保证 db 处于业务事务内,以让 audit 行与业务行原子落库。
func (r *gormRepository) writeAudit(
	ctx context.Context,
	orgID uint64,
	action, targetType string,
	targetID uint64,
	before, after, metadata any,
) error {
	return audit.Write(ctx, r.db, orgID, action, targetType, targetID, before, after, metadata)
}
