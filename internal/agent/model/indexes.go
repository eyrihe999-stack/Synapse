// indexes.go agent 模块索引确保。
package model

import (
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/dbutil"
	"gorm.io/gorm"
)

// agent_publishes.active_marker 是 generated column,status IN ('approved','pending') 时为 1,否则 NULL。
// 配合 (agent_id, org_id, active_marker) 唯一索引,在 DB 层兜底"同一 agent 在同一 org 同一时刻
// 最多一个 active publish",作为 service 层 LockAgentByID 行锁之外的 defense-in-depth。
// 用 VIRTUAL 而非 STORED,ALTER 时不重写已有行,大表迁移更安全。
// MySQL 唯一索引视 NULL 为不重复,所以 revoked/rejected 行(active_marker=NULL)不冲突。
const (
	publishActiveMarkerColumn = "active_marker"
	publishActiveMarkerDDL    = "ALTER TABLE `agent_publishes` ADD COLUMN `active_marker` TINYINT GENERATED ALWAYS AS (CASE WHEN `status` IN ('approved','pending') THEN 1 ELSE NULL END) VIRTUAL"
)

// columnExists 用 information_schema 查询某表的列是否存在。
func columnExists(db *gorm.DB, table, column string) (bool, error) {
	var n int
	err := db.Raw(
		"SELECT 1 FROM information_schema.COLUMNS WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ? LIMIT 1",
		table, column,
	).Scan(&n).Error
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// EnsureAgentIndexes 确保需要手动创建的复合索引和列。
// 注意:若历史数据已存在 (agent_id, org_id) 双 active 记录,创建唯一索引会失败,
// 此时需先按 LockAgentByID 的语义清理重复行再重启。
func EnsureAgentIndexes(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	exists, err := columnExists(db, "agent_publishes", publishActiveMarkerColumn)
	if err != nil {
		return fmt.Errorf("check %s column: %w", publishActiveMarkerColumn, err)
	}
	if !exists {
		if err := db.Exec(publishActiveMarkerDDL).Error; err != nil {
			return fmt.Errorf("add %s column: %w", publishActiveMarkerColumn, err)
		}
	}
	return dbutil.EnsureIndexes(db, []dbutil.IndexSpec{
		{
			Table:   "agent_publishes",
			Name:    "uk_publishes_agent_org_active",
			Columns: []string{"agent_id", "org_id", "active_marker"},
			Unique:  true,
		},
	})
}
