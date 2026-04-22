// Package model user_integration 模块 gorm 映射。
package model

import (
	"time"

	"gorm.io/datatypes"
)

// tableUserIntegrations 表名本地常量。
// 不 import 根包避免 migration → model → 根包的 import cycle;
// 根包 user_integration.TableUserIntegrations 的值必须和这里保持一致。
const tableUserIntegrations = "user_integrations"

// UserIntegration 一条用户 × provider × 外部账号的凭据记录。
//
// 不变量(migration 的 UNIQUE 兜住):
//
//	(user_id, provider, external_account_id) 唯一
//
// 同一用户可以在同一 provider 连多个外部账号(工作号 + 个人号),靠 external_account_id 区分。
//
// 字段说明:
//
//   - OrgID 可空 —— 账号级连接时 0 值;ingestion runner 构造时把 OrgID 作为 "落库到哪个 org"
//     的选择从 header / 参数读入,不用 UserIntegration 自身绑。
//   - AccessToken / RefreshToken 明文存,size=2048 对主流 provider 的 JWT access token 足够。
//   - Scopes 逗号分隔字符串("docs.read,docs.write"),service 层自己 Split。
//   - ProviderMeta 源端特有字段(飞书 tenant_key / Notion workspace_id 等),jsonb 挂这里,
//     不为每个 provider 开专属表。
type UserIntegration struct {
	// 索引定义全放 migration.go 的 indexSpecs(single source of truth)。
	// struct tag 不带 index/uniqueIndex,避免 AutoMigrate 按 struct tag 抢先建出 shape
	// 与 spec 不一致的索引导致 EnsureIndexes 按名跳过(决策过程详见 commit 说明)。
	ID                uint64 `gorm:"column:id;primaryKey;autoIncrement"`
	UserID            uint64 `gorm:"column:user_id;not null"`
	OrgID             uint64 `gorm:"column:org_id"`
	Provider          string `gorm:"column:provider;size:32;not null"`
	ExternalAccountID string `gorm:"column:external_account_id;size:128;not null"`

	AccountName  string `gorm:"column:account_name;size:255;not null;default:''"`
	AccountEmail string `gorm:"column:account_email;size:255;not null;default:''"`

	AccessToken  string `gorm:"column:access_token;size:2048;not null"`
	RefreshToken string `gorm:"column:refresh_token;size:2048;not null;default:''"`
	TokenType    string `gorm:"column:token_type;size:32;not null;default:''"`
	// Scopes 逗号分隔;MySQL TEXT 不支持 DEFAULT,用 varchar(2048) 即可容纳各 provider 的 scope 列表。
	Scopes string `gorm:"column:scopes;size:2048;not null;default:''"`
	ExpiresAt    *time.Time `gorm:"column:expires_at"`

	ProviderMeta datatypes.JSON `gorm:"column:provider_meta;type:json"`

	Status     string     `gorm:"column:status;size:16;not null;default:'active'"`
	LastUsedAt *time.Time `gorm:"column:last_used_at"`

	CreatedAt time.Time `gorm:"column:created_at;not null"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null"`
}

// TableName 固定表名。
func (UserIntegration) TableName() string { return tableUserIntegrations }
