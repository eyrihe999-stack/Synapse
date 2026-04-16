// partitions.go 手写 MySQL RANGE 分区表 DDL。
//
// agent_invocations 与 agent_invocation_payloads 是大写入量表,
// 使用 PARTITION BY RANGE (TO_DAYS(started_at)) 按月切片,便于 DROP 过期分区。
//
// started_at 使用 DATETIME(3) 而不是 TIMESTAMP(3):MySQL 禁止时区依赖的
// 表达式做分区函数,TO_DAYS(TIMESTAMP) 会因 session tz 而变,被拒;
// DATETIME 无时区转换,TO_DAYS(DATETIME) 是确定性的。Go 层统一用 UTC 写入。
//
// EnsurePartitionTables 在启动时幂等创建表 + 预留未来 3 个月的分区。
// 分区维护任务(创建新月份、DROP 过期月份)在 service 层的定时任务里触发。
package model

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// EnsurePartitionTables 幂等创建两张分区表,并确保未来 3 个月的分区存在。
//
// 步骤:
//  1. CREATE TABLE IF NOT EXISTS(含初始分区 p_min + 当前月 + 未来 3 月)
//  2. 对每张表调用 EnsureFuturePartitions 补齐缺失的月度分区(用于热启动)
//
// 分区策略:
//   - p_min: LESS THAN (0),兜底
//   - p_YYYYMM: LESS THAN (TO_DAYS('YYYY-MM-01') + 1 month) 按月切
//   - 未来窗口默认 3 个月,保障写入永远落在已存在的分区内
//
// 错误:建表或补分区的任一步骤失败都会返回 fmt.Errorf 包装的原因,由调用方上浮为 ErrAgentInternal。
func EnsurePartitionTables(ctx context.Context, db *gorm.DB) error {
	if err := createInvocationsTable(ctx, db); err != nil {
		//sayso-lint:ignore log-coverage
		return fmt.Errorf("create agent_invocations: %w", err)
	}
	if err := createPayloadsTable(ctx, db); err != nil {
		//sayso-lint:ignore log-coverage
		return fmt.Errorf("create agent_invocation_payloads: %w", err)
	}
	// 启动时再补未来 3 个月(若重启点恰好跨月)
	if err := EnsureFuturePartitions(ctx, db, 3); err != nil {
		//sayso-lint:ignore log-coverage
		return fmt.Errorf("ensure future partitions: %w", err)
	}
	return nil
}

// createInvocationsTable 创建 agent_invocations 表(首次建表)。
// 含初始分区 p_min 和当月分区。
func createInvocationsTable(ctx context.Context, db *gorm.DB) error {
	now := time.Now().UTC()
	nextMonth := firstOfMonth(now).AddDate(0, 1, 0)
	ddl := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS agent_invocations (
	id                   BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
	invocation_id        VARCHAR(64)     NOT NULL,
	trace_id             VARCHAR(64)     DEFAULT NULL,
	org_id               BIGINT UNSIGNED NOT NULL,
	caller_user_id       BIGINT UNSIGNED NOT NULL,
	caller_role_name     VARCHAR(32)     DEFAULT NULL,
	agent_id             BIGINT UNSIGNED NOT NULL,
	agent_owner_user_id  BIGINT UNSIGNED NOT NULL,
	method_name          VARCHAR(64)     NOT NULL,
	transport            VARCHAR(16)     NOT NULL,
	started_at           DATETIME(3)     NOT NULL,
	finished_at          DATETIME(3)     NULL,
	latency_ms           INT             DEFAULT NULL,
	status               VARCHAR(32)     NOT NULL,
	error_code           VARCHAR(32)     DEFAULT NULL,
	error_message        VARCHAR(500)    DEFAULT NULL,
	request_size_bytes   INT             DEFAULT NULL,
	response_size_bytes  INT             DEFAULT NULL,
	client_ip            VARCHAR(45)     DEFAULT NULL,
	user_agent           VARCHAR(255)    DEFAULT NULL,
	created_at           TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY (id, started_at),
	KEY idx_inv_org_started      (org_id, started_at),
	KEY idx_inv_caller_started   (caller_user_id, started_at),
	KEY idx_inv_agent_started    (agent_id, started_at),
	KEY idx_inv_owner_started    (agent_owner_user_id, started_at),
	KEY idx_inv_invocation_id    (invocation_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
PARTITION BY RANGE (TO_DAYS(started_at)) (
	PARTITION p_min VALUES LESS THAN (0),
	PARTITION %s VALUES LESS THAN (TO_DAYS('%s'))
);`, partitionName(now), nextMonth.Format("2006-01-02"))
	//sayso-lint:ignore log-coverage
	return db.WithContext(ctx).Exec(ddl).Error
}

// createPayloadsTable 创建 agent_invocation_payloads 表。
func createPayloadsTable(ctx context.Context, db *gorm.DB) error {
	now := time.Now().UTC()
	nextMonth := firstOfMonth(now).AddDate(0, 1, 0)
	ddl := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS agent_invocation_payloads (
	id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
	invocation_id   VARCHAR(64)     NOT NULL,
	request_body    MEDIUMBLOB      DEFAULT NULL,
	response_body   MEDIUMBLOB      DEFAULT NULL,
	started_at      DATETIME(3)     NOT NULL,
	created_at      TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY (id, started_at),
	KEY idx_payloads_invocation_id (invocation_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
PARTITION BY RANGE (TO_DAYS(started_at)) (
	PARTITION p_min VALUES LESS THAN (0),
	PARTITION %s VALUES LESS THAN (TO_DAYS('%s'))
);`, partitionName(now), nextMonth.Format("2006-01-02"))
	//sayso-lint:ignore log-coverage
	return db.WithContext(ctx).Exec(ddl).Error
}

// EnsureFuturePartitions 确保未来 months 个月的分区都已存在(幂等)。
//
// 对每张分区表,查询 INFORMATION_SCHEMA.PARTITIONS 拿到现有分区名,
// 对缺失的月份执行 ALTER TABLE ... ADD PARTITION。
//
// 调用时机:
//   - 启动时 EnsurePartitionTables 会调用一次
//   - 运行期每天定时任务调用一次(保持未来 3 个月窗口)
//
// 错误:查询现有分区或 ALTER TABLE 失败时返回原因,由调用方上浮为 ErrAgentInternal。
func EnsureFuturePartitions(ctx context.Context, db *gorm.DB, months int) error {
	if months <= 0 {
		months = 3
	}
	tables := []string{"agent_invocations", "agent_invocation_payloads"}
	for _, t := range tables {
		if err := ensureFuturePartitionsForTable(ctx, db, t, months); err != nil {
			//sayso-lint:ignore log-coverage
			return fmt.Errorf("ensure %s partitions: %w", t, err)
		}
	}
	return nil
}

// ensureFuturePartitionsForTable 单表的未来分区补齐逻辑。
func ensureFuturePartitionsForTable(ctx context.Context, db *gorm.DB, table string, months int) error {
	now := time.Now().UTC()
	existing, err := listPartitions(ctx, db, table)
	if err != nil {
		//sayso-lint:ignore log-coverage
		return err
	}
	// 对当前月 + 未来 months 个月逐个检查
	for i := 0; i <= months; i++ {
		target := firstOfMonth(now).AddDate(0, i, 0)
		name := partitionName(target)
		if _, ok := existing[name]; ok {
			continue
		}
		// 上界 = 下一个月第一天
		upper := target.AddDate(0, 1, 0).Format("2006-01-02")
		ddl := fmt.Sprintf(
			"ALTER TABLE %s ADD PARTITION (PARTITION %s VALUES LESS THAN (TO_DAYS('%s')))",
			table, name, upper,
		)
		if err := db.WithContext(ctx).Exec(ddl).Error; err != nil {
			//sayso-lint:ignore log-coverage
			return fmt.Errorf("add partition %s: %w", name, err)
		}
	}
	return nil
}

// DropExpiredPartitions 删除早于保留窗口的月度分区(按 started_at 计算)。
// retentionDays 来自配置,例如 invocations=90、payloads=30。
//
// 错误:列出分区或 ALTER TABLE DROP PARTITION 失败时返回原因,由调用方上浮为 ErrAgentInternal。
func DropExpiredPartitions(ctx context.Context, db *gorm.DB, table string, retentionDays int) error {
	if retentionDays <= 0 {
		return nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays)
	existing, err := listPartitions(ctx, db, table)
	if err != nil {
		//sayso-lint:ignore log-coverage
		return fmt.Errorf("list partitions: %w", err)
	}
	for name := range existing {
		if name == "p_min" {
			continue
		}
		t, ok := parsePartitionName(name)
		if !ok {
			continue
		}
		// 若分区涵盖的月份末尾仍早于 cutoff,则整月可以删
		monthEnd := firstOfMonth(t).AddDate(0, 1, 0)
		if monthEnd.Before(cutoff) {
			ddl := fmt.Sprintf("ALTER TABLE %s DROP PARTITION %s", table, name)
			if err := db.WithContext(ctx).Exec(ddl).Error; err != nil {
				//sayso-lint:ignore log-coverage
				return fmt.Errorf("drop partition %s: %w", name, err)
			}
		}
	}
	return nil
}

// listPartitions 返回某张表当前已存在的所有分区名集合。
func listPartitions(ctx context.Context, db *gorm.DB, table string) (map[string]struct{}, error) {
	var rows []struct {
		PartitionName string `gorm:"column:PARTITION_NAME"`
	}
	q := `
		SELECT PARTITION_NAME
		FROM INFORMATION_SCHEMA.PARTITIONS
		WHERE TABLE_SCHEMA = DATABASE()
		  AND TABLE_NAME = ?
		  AND PARTITION_NAME IS NOT NULL
	`
	if err := db.WithContext(ctx).Raw(q, table).Scan(&rows).Error; err != nil {
		//sayso-lint:ignore log-coverage
		return nil, err
	}
	out := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		out[r.PartitionName] = struct{}{}
	}
	return out, nil
}

// partitionName 按 "p_YYYYMM" 格式返回 t 所在月份的分区名。
func partitionName(t time.Time) string {
	return fmt.Sprintf("p_%04d%02d", t.Year(), int(t.Month()))
}

// parsePartitionName 反解 "p_YYYYMM" → time.Time(月首日 UTC)。
// 格式错误返回 (_, false)。
func parsePartitionName(name string) (time.Time, bool) {
	if len(name) != 8 || name[0] != 'p' || name[1] != '_' {
		return time.Time{}, false
	}
	var y, m int
	//sayso-lint:ignore err-swallow
	if _, err := fmt.Sscanf(name[2:], "%04d%02d", &y, &m); err != nil { // 无需关心扫描个数
		return time.Time{}, false
	}
	if m < 1 || m > 12 {
		return time.Time{}, false
	}
	return time.Date(y, time.Month(m), 1, 0, 0, 0, 0, time.UTC), true
}

// firstOfMonth 返回 t 所在月份的第一天(UTC)。
func firstOfMonth(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}
