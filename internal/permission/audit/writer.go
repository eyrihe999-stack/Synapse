// Package audit 是权限审计日志的共享写入入口。
//
// 审计表 permission_audit_log 由 permission 模块拥有(schema 在 internal/permission/model),
// 但需要被多个上层模块写入(权限组本身、source、未来的 ACL / member.role 改动)。
// 本包仅暴露写入函数,接受调用方持有的 *gorm.DB(可以是主连接也可以是 tx),
// 让调用方把 audit 写入放进自己的业务事务里,保证原子性。
//
// Action / TargetType 常量由各模块自行定义(每个模块拥有自己的命名空间),
// 本包只关心通用写入逻辑。
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/permission/model"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Write 在指定 db / tx 内插一条 PermissionAuditLog 行。
//
// 参数:
//   - db:可以是主 *gorm.DB,也可以是 Transaction 内的 tx。建议在业务事务内传入 tx,
//     保证 audit 行与业务行同事务落库。
//   - orgID:被操作对象所属的 org;查询时按 org 过滤。必填。
//   - action:动作名(各模块自定义,如 "group.create" / "source.visibility_change")。必填。
//   - targetType / targetID:被操作对象类型与主键(各模块自定义 type 名)。必填。
//   - before / after / metadata:可空,nil → 写 SQL NULL;非 nil 则 JSON-marshal。
//
// actor_user_id 从 ctx 读(由 middleware 注入,见 logger.WithUserID);系统/迁移路径记 0。
//
// 任一 marshal 失败被视为编程错误,直接返回(事务回滚),不静默丢审计。
func Write(
	ctx context.Context,
	db *gorm.DB,
	orgID uint64,
	action, targetType string,
	targetID uint64,
	before, after, metadata any,
) error {
	beforeJSON, err := marshalNullable(before)
	if err != nil {
		return fmt.Errorf("audit before marshal: %w", err)
	}
	afterJSON, err := marshalNullable(after)
	if err != nil {
		return fmt.Errorf("audit after marshal: %w", err)
	}
	metaJSON, err := marshalNullable(metadata)
	if err != nil {
		return fmt.Errorf("audit metadata marshal: %w", err)
	}

	row := &model.PermissionAuditLog{
		OrgID:       orgID,
		ActorUserID: logger.GetUserID(ctx),
		Action:      action,
		TargetType:  targetType,
		TargetID:    targetID,
		Before:      beforeJSON,
		After:       afterJSON,
		Metadata:    metaJSON,
		CreatedAt:   time.Now().UTC(),
	}
	if err := db.WithContext(ctx).Create(row).Error; err != nil {
		return fmt.Errorf("write audit: %w", err)
	}
	return nil
}

// marshalNullable nil → SQL NULL(空 datatypes.JSON),非 nil → json.Marshal。
func marshalNullable(v any) (datatypes.JSON, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return datatypes.JSON(b), nil
}
