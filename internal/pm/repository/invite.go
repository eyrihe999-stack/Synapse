package repository

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// AddMembersToWorkstreamChannel 实现见 repository.go interface 注释。
//
// 流程:
//  1. 查 workstream 当前 channel_id;不存在 / channel_id 为空(lazy-create 未跑)
//     → 返错(invite_to_workstream tool handler 应保证 workstream 已挂 channel)
//  2. 对 principal 集合做 INSERT IGNORE —— 已是成员的行被 UNIQUE 兜底跳过,不重复 INSERT
//  3. 反查"刚刚被加进来"的 principal 列表(对比 INSERT IGNORE 前后差集)— 简化:
//     直接返 principalIDs 全部,调用方按需自行 dedup。这避免一次额外的 SELECT
//     往返;调用方 LLM 看到"已加入"的统一返回即可。
func (r *gormRepository) AddMembersToWorkstreamChannel(
	ctx context.Context,
	workstreamID uint64,
	principalIDs []uint64,
) ([]uint64, uint64, error) {
	if len(principalIDs) == 0 {
		return nil, 0, nil
	}

	// 查 workstream.channel_id
	var channelID uint64
	err := r.db.WithContext(ctx).Raw(
		"SELECT channel_id FROM workstreams WHERE id = ?", workstreamID,
	).Scan(&channelID).Error
	if err != nil {
		return nil, 0, fmt.Errorf("query workstream.channel_id: %w", err)
	}
	if channelID == 0 {
		return nil, 0, errors.New("workstream channel not yet created")
	}

	// 批量 INSERT IGNORE channel_members
	now := time.Now()
	for _, pid := range principalIDs {
		if pid == 0 {
			continue
		}
		if err := r.db.WithContext(ctx).Exec(
			"INSERT IGNORE INTO channel_members (channel_id, principal_id, role, joined_at) VALUES (?, ?, ?, ?)",
			channelID, pid, "member", now,
		).Error; err != nil {
			return nil, channelID, fmt.Errorf("insert channel member %d: %w", pid, err)
		}
	}

	added := make([]uint64, 0, len(principalIDs))
	for _, pid := range principalIDs {
		if pid != 0 {
			added = append(added, pid)
		}
	}
	return added, channelID, nil
}
