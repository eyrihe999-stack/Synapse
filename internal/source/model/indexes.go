// indexes.go source 模块数据库索引定义。
package model

import (
	"github.com/eyrihe999-stack/Synapse/internal/common/database"
	"gorm.io/gorm"
)

// sourceIndexSpecs source 模块的索引定义。
//
// uk_sources_full 是核心:复合唯一保证 (org_id, kind, owner_user_id, external_ref) 不重复。
// 对 manual_upload(external_ref='')退化为 (org_id, kind, owner_user_id) 唯一,
// 给 lazy create 提供"幂等 upsert"基础(repo 用 INSERT IGNORE / FindOrCreate 模式)。
var sourceIndexSpecs = []database.IndexSpec{
	{Table: "sources", Name: "uk_sources_full", Columns: []string{"org_id", "kind", "owner_user_id", "external_ref"}, Unique: true},
	// uk_sources_owner_name 保证同一 owner 在同一 org 下 name 不重。
	// 同一 (org_id, owner_user_id) 下 manual_upload 天然最多一条,所以 migration 前的空 name 不会自撞;
	// CreateCustomSource 强制 name 非空,空串也不会和 custom 撞。
	{Table: "sources", Name: "uk_sources_owner_name", Columns: []string{"org_id", "owner_user_id", "name"}, Unique: true},
	{Table: "sources", Name: "idx_sources_org", Columns: []string{"org_id"}, Unique: false},
	{Table: "sources", Name: "idx_sources_kind", Columns: []string{"kind"}, Unique: false},
	{Table: "sources", Name: "idx_sources_owner", Columns: []string{"owner_user_id"}, Unique: false},
	{Table: "sources", Name: "idx_sources_visibility", Columns: []string{"visibility"}, Unique: false},
}

// EnsureSourceIndexes 幂等创建 source 模块所有索引。
func EnsureSourceIndexes(db *gorm.DB) error {
	//sayso-lint:ignore log-coverage
	return database.EnsureIndexes(db, sourceIndexSpecs)
}
