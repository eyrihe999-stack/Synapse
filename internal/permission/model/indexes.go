// indexes.go 权限模块数据库索引定义。
package model

import (
	"github.com/eyrihe999-stack/Synapse/internal/common/database"
	"gorm.io/gorm"
)

// permIndexSpecs 权限模块的索引定义。
//
// 设计意图:
//   - perm_groups:常查"某 org 下所有组"和"某用户建的组"
//   - perm_group_members:除主键 (group_id, user_id) 外,还要按 user 反查"我加入了哪些组"
//   - permission_audit_log:三个常见查询路径(按 org+时间、按目标对象、按 actor)各一个索引
var permIndexSpecs = []database.IndexSpec{
	// perm_groups
	{Table: "perm_groups", Name: "uk_perm_groups_org_name", Columns: []string{"org_id", "name"}, Unique: true},
	{Table: "perm_groups", Name: "idx_perm_groups_org", Columns: []string{"org_id"}, Unique: false},
	{Table: "perm_groups", Name: "idx_perm_groups_owner", Columns: []string{"owner_user_id"}, Unique: false},

	// perm_group_members
	{Table: "perm_group_members", Name: "idx_perm_group_members_user", Columns: []string{"user_id"}, Unique: false},

	// permission_audit_log
	{Table: "permission_audit_log", Name: "idx_perm_audit_org_created", Columns: []string{"org_id", "created_at DESC"}, Unique: false},
	{Table: "permission_audit_log", Name: "idx_perm_audit_target", Columns: []string{"target_type", "target_id"}, Unique: false},
	{Table: "permission_audit_log", Name: "idx_perm_audit_actor", Columns: []string{"actor_user_id"}, Unique: false},
	{Table: "permission_audit_log", Name: "idx_perm_audit_action", Columns: []string{"action"}, Unique: false},

	// resource_acl
	// 唯一约束:同一 (resource, subject) 对至多一条 ACL —— update permission 走 UPDATE 而非 INSERT
	{Table: "resource_acl", Name: "uk_acl_resource_subject", Columns: []string{"resource_type", "resource_id", "subject_type", "subject_id"}, Unique: true},
	// "我能看哪些资源":按 (org, subject) 反查 ACL 命中的所有 resource
	{Table: "resource_acl", Name: "idx_acl_org_subject", Columns: []string{"org_id", "subject_type", "subject_id"}, Unique: false},
	// "这资源谁能看":按 resource 列出所有 ACL
	{Table: "resource_acl", Name: "idx_acl_resource", Columns: []string{"resource_type", "resource_id"}, Unique: false},
}

// EnsurePermIndexes 幂等创建权限模块所有索引。
//
// 可能的错误:
//   - DDL 执行失败时返回 database.EnsureIndexes 的底层错误(未包装 sentinel)
func EnsurePermIndexes(db *gorm.DB) error {
	//sayso-lint:ignore log-coverage
	return database.EnsureIndexes(db, permIndexSpecs)
}
