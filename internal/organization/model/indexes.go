// indexes.go 组织模块数据库索引定义。
//
// 大部分索引通过 GORM struct tag 声明,但某些复合索引或命名约定需要手写显式 DDL
// 以保证在不同 MySQL 版本下的一致性。EnsureOrgIndexes 负责把 dbutil.IndexSpec
// 切片中声明的索引幂等创建出来。
package model

import (
	"github.com/eyrihe999-stack/Synapse/internal/dbutil"
	"gorm.io/gorm"
)

// orgIndexSpecs 组织模块的索引定义。
//
// 说明:
//   - 主键和单列唯一索引已通过 gorm tag 声明,不在此重复;
//   - 此处只列出需要显式命名/保证跨版本一致的索引;
//   - 如果 gorm AutoMigrate 已经创建了同名索引,EnsureIndex 会跳过(幂等)。
var orgIndexSpecs = []dbutil.IndexSpec{
	// orgs
	{Table: "orgs", Name: "uk_orgs_slug", Columns: []string{"slug"}, Unique: true},
	{Table: "orgs", Name: "idx_orgs_owner", Columns: []string{"owner_user_id"}, Unique: false},
	{Table: "orgs", Name: "idx_orgs_status", Columns: []string{"status"}, Unique: false},

	// org_members
	{Table: "org_members", Name: "uk_members_org_user", Columns: []string{"org_id", "user_id"}, Unique: true},
	{Table: "org_members", Name: "idx_members_user", Columns: []string{"user_id"}, Unique: false},
	{Table: "org_members", Name: "idx_members_org", Columns: []string{"org_id"}, Unique: false},
	{Table: "org_members", Name: "idx_members_role", Columns: []string{"role_id"}, Unique: false},

	// org_roles
	{Table: "org_roles", Name: "uk_roles_org_name", Columns: []string{"org_id", "name"}, Unique: true},
	{Table: "org_roles", Name: "idx_roles_org", Columns: []string{"org_id"}, Unique: false},

	// org_invitations
	{Table: "org_invitations", Name: "idx_invitations_invitee_status", Columns: []string{"invitee_user_id", "status"}, Unique: false},
	{Table: "org_invitations", Name: "idx_invitations_org_status", Columns: []string{"org_id", "status"}, Unique: false},
	{Table: "org_invitations", Name: "idx_invitations_expires", Columns: []string{"expires_at"}, Unique: false},

	// org_member_role_history
	{Table: "org_member_role_history", Name: "idx_history_org_user", Columns: []string{"org_id", "user_id", "created_at"}, Unique: false},
}

// EnsureOrgIndexes 幂等创建组织模块所有索引。
// 任一索引创建失败时返回 error。调用方(migration.go 的 RunMigrations)会记日志。
func EnsureOrgIndexes(db *gorm.DB) error {
	//sayso-lint:ignore log-coverage
	return dbutil.EnsureIndexes(db, orgIndexSpecs)
}
