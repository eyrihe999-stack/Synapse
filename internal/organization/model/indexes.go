// indexes.go 组织模块数据库索引定义。
package model

import (
	"github.com/eyrihe999-stack/Synapse/internal/common/database"
	"gorm.io/gorm"
)

// orgIndexSpecs 组织模块的索引定义。
var orgIndexSpecs = []database.IndexSpec{
	// orgs
	{Table: "orgs", Name: "uk_orgs_slug", Columns: []string{"slug"}, Unique: true},
	{Table: "orgs", Name: "idx_orgs_owner", Columns: []string{"owner_user_id"}, Unique: false},
	{Table: "orgs", Name: "idx_orgs_status", Columns: []string{"status"}, Unique: false},

	// org_roles
	{Table: "org_roles", Name: "uk_roles_org_slug", Columns: []string{"org_id", "slug"}, Unique: true},
	{Table: "org_roles", Name: "idx_roles_org", Columns: []string{"org_id"}, Unique: false},

	// org_members
	{Table: "org_members", Name: "uk_members_org_user", Columns: []string{"org_id", "user_id"}, Unique: true},
	{Table: "org_members", Name: "idx_members_user", Columns: []string{"user_id"}, Unique: false},
	{Table: "org_members", Name: "idx_members_org", Columns: []string{"org_id"}, Unique: false},
	{Table: "org_members", Name: "idx_members_role", Columns: []string{"role_id"}, Unique: false},

	// org_invitations
	{Table: "org_invitations", Name: "uk_invites_token_hash", Columns: []string{"token_hash"}, Unique: true},
	{Table: "org_invitations", Name: "idx_invites_org_status", Columns: []string{"org_id", "status"}, Unique: false},
	{Table: "org_invitations", Name: "idx_invites_org_email", Columns: []string{"org_id", "email"}, Unique: false},
	{Table: "org_invitations", Name: "idx_invites_email", Columns: []string{"email"}, Unique: false},
	{Table: "org_invitations", Name: "idx_invites_inviter", Columns: []string{"inviter_user_id"}, Unique: false},
	{Table: "org_invitations", Name: "idx_invites_role", Columns: []string{"role_id"}, Unique: false},
	{Table: "org_invitations", Name: "idx_invites_expires", Columns: []string{"expires_at"}, Unique: false},
}

// EnsureOrgIndexes 幂等创建组织模块所有索引。
//
// 可能的错误:
//   - DDL 执行失败时返回 database.EnsureIndexes 的底层错误(未包装 sentinel)
func EnsureOrgIndexes(db *gorm.DB) error {
	//sayso-lint:ignore log-coverage
	return database.EnsureIndexes(db, orgIndexSpecs)
}
