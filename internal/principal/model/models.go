// Package model principal 模块数据模型 —— 只含 principals 身份根表。
//
// Principal 是身份根,统一承载 user 和 agent 两类身份,为 channel_members /
// step_assignees / audit 等 cross-cutting 关系提供单一 FK 目标。详见
// docs/collaboration-design.md §3.5。
//
// 角色 / 权限不在本包:RBAC 走已有的 organization.org_roles +
// org_members.role_id(见 collaboration-design.md §3.5.4 说明)。
package model

import "time"

// Kind 枚举:principal 的类型。
const (
	KindUser  = "user"
	KindAgent = "agent"
)

// Status 对齐现有 users.status 语义,复用枚举值避免回填时做映射。
const (
	StatusPendingVerify = int32(0)
	StatusActive        = int32(1)
	StatusDisabled      = int32(2) // agent 的 enabled=false 或 user 的 banned 都映射到这
	StatusDeleted       = int32(3)
)

// Principal 身份根。
//
// 字段语义:
//   - Kind         "user" | "agent",决定子表在哪里
//   - DisplayName  统一的显示名,从子表冗余上来(子表列 PR #1 暂不删)
//   - AvatarURL    同上
//   - Status       对齐 users.status 数值语义(agent enabled=true → 1,false → 2)
//
// 没有 OrgID —— user 可属多 org,agent org 归属放 agents 子表,见 §3.5.1 说明。
type Principal struct {
	ID          uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	Kind        string    `gorm:"size:16;not null;index:idx_principals_kind_status,priority:1" json:"kind"`
	DisplayName string    `gorm:"size:128;not null" json:"display_name"`
	AvatarURL   string    `gorm:"size:512" json:"avatar_url,omitempty"`
	Status      int32     `gorm:"not null;default:1;index:idx_principals_kind_status,priority:2" json:"status"`
	CreatedAt   time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt   time.Time `gorm:"not null" json:"updated_at"`
}

// TableName 固定表名。
func (Principal) TableName() string { return "principals" }
