// indexes.go agent 模块非分区表的索引定义。
//
// 分区表(agent_invocations / agent_invocation_payloads)的索引
// 在 partitions.go 随 CREATE TABLE 一并建立,不走这里。
package model

import (
	"github.com/eyrihe999-stack/Synapse/internal/dbutil"
	"gorm.io/gorm"
)

var agentIndexSpecs = []dbutil.IndexSpec{
	// agents
	{Table: "agents", Name: "uk_agents_owner_slug", Columns: []string{"owner_user_id", "slug"}, Unique: true},
	{Table: "agents", Name: "idx_agents_owner", Columns: []string{"owner_user_id"}, Unique: false},
	{Table: "agents", Name: "idx_agents_health", Columns: []string{"health_status"}, Unique: false},
	{Table: "agents", Name: "idx_agents_status", Columns: []string{"status"}, Unique: false},

	// agent_methods
	{Table: "agent_methods", Name: "uk_methods_agent_name", Columns: []string{"agent_id", "method_name"}, Unique: true},
	{Table: "agent_methods", Name: "idx_methods_agent", Columns: []string{"agent_id"}, Unique: false},

	// agent_secrets
	{Table: "agent_secrets", Name: "uk_secrets_agent", Columns: []string{"agent_id"}, Unique: true},

	// agent_publishes
	{Table: "agent_publishes", Name: "idx_publishes_org_status", Columns: []string{"org_id", "status"}, Unique: false},
	{Table: "agent_publishes", Name: "idx_publishes_agent", Columns: []string{"agent_id"}, Unique: false},
}

// EnsureAgentIndexes 幂等创建 agent 模块非分区表的所有索引。
//
// 错误:底层 DDL 执行失败时透传 dbutil 错误,由调用方包装为 ErrAgentInternal。
func EnsureAgentIndexes(db *gorm.DB) error {
	//sayso-lint:ignore log-coverage
	return dbutil.EnsureIndexes(db, agentIndexSpecs)
}
