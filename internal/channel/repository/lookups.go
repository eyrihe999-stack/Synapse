package repository

import (
	"context"
	"fmt"
)

// 跨模块 helper(原本放在 kb_ref.go,kb_ref.go 删除时一并迁出)。
//
// 这两个 helper 不依赖 channel_kb_refs 表,只是历史上"恰好"和 KB ref 放一起。
// 现在独立到 lookups.go,让 channel.repository 不再依赖已经退役的 channel_kb_refs。

// LookupAgentOwnerUserPrincipalID 反查 agent.principal_id → owner user 的 principal_id。
//
// 用途:让 caller=agent 的 list_my_mentions / dashboard 能"看到 owner user 收到的 @"。
// 真实场景里 alice 在 web 端 @ 的是 user principal,alice 用 Claude(agent principal)
// 调 MCP 应该能看到 —— 否则 inbox 永远空。
//
// 返 0 的三种情况(都用 (0, nil) 让调用方走单一逻辑):
//   - principal 不是 agent(是 user / 不存在)
//   - agent 是 system kind(owner_user_id NULL)
//   - 历史脏数据(owner_user_id 指向不存在的 user)
func (r *gormRepository) LookupAgentOwnerUserPrincipalID(ctx context.Context, agentPrincipalID uint64) (uint64, error) {
	var pid uint64
	err := r.db.WithContext(ctx).Raw(`
		SELECT u.principal_id
		FROM agents a
		JOIN users u ON u.id = a.owner_user_id
		WHERE a.principal_id = ?
		  AND a.kind = 'user'
		  AND a.owner_user_id IS NOT NULL
		LIMIT 1
	`, agentPrincipalID).Scan(&pid).Error
	if err != nil {
		return 0, fmt.Errorf("lookup agent owner principal: %w", err)
	}
	return pid, nil
}

// LookupAutoIncludeAgentPrincipals 跨模块查 agents 表(不引入 agents 模块依赖)。
// 返 principal_id 列表,用于 channel 创建时自动加成员。
func (r *gormRepository) LookupAutoIncludeAgentPrincipals(ctx context.Context, channelOrgID uint64) ([]uint64, error) {
	var ids []uint64
	err := r.db.WithContext(ctx).
		Table("agents").
		Where("auto_include_in_new_channels = ? AND enabled = ? AND (org_id = 0 OR org_id = ?)", true, true, channelOrgID).
		Pluck("principal_id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("lookup auto-include agent principals: %w", err)
	}
	return ids, nil
}
