package model

import "time"

// UserIdentity 第三方身份源(Google / Feishu / ...)与 users 的一对多绑定。
//
// 设计要点:
//   - (Provider, Subject) 为唯一键,即使邮箱改了也能稳定认人
//   - 一个 user 可绑多个 identity(同一人既有 google 登录又有飞书登录),反向一对多
//   - Email / EmailVerified 冗余存 callback 当时拿到的值,便于审计 + 减少对 IdP 反查
//   - 软删除通过 users.deleted_at 级联(identity 表不独立软删,避免悬空)
//
// 合并策略见 service/oauth_login.go:HandleOAuthCallback,核心口径:
//   - 凭 (provider, subject) 命中 identity → 直接登录对应 user
//   - 未命中但 email_verified=true 且 email 在 users 里存在 → 自动绑定到该 user
//   - 均未命中 → 创建新 user + identity
type UserIdentity struct {
	ID            uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID        uint64    `gorm:"not null;index:idx_identities_user" json:"user_id"`
	Provider      string    `gorm:"size:32;not null;uniqueIndex:uk_identities_provider_subject,priority:1" json:"provider"`
	Subject       string    `gorm:"size:191;not null;uniqueIndex:uk_identities_provider_subject,priority:2" json:"subject"`
	Email         string    `gorm:"size:255" json:"email"`
	EmailVerified bool      `gorm:"not null;default:false" json:"email_verified"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (UserIdentity) TableName() string { return "user_identities" }
