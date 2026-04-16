package model

import (
	"time"

	"gorm.io/gorm"
)

const (
	StatusActive = int32(1)
	StatusBanned = int32(2)
)

// User 用户主表模型。
// Email 唯一索引,Status=2(banned) 时禁止登录。
type User struct {
	ID           uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	Email        string         `gorm:"size:255;not null;uniqueIndex:uk_users_email" json:"email"`
	PasswordHash string         `gorm:"size:255;not null" json:"-"`
	DisplayName  string         `gorm:"size:64" json:"display_name"`
	AvatarURL    string         `gorm:"size:512" json:"avatar_url"`
	Status       int32          `gorm:"not null;default:1" json:"status"`
	LastLoginAt  *time.Time     `json:"last_login_at"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	DeletedAt    gorm.DeletedAt `gorm:"index" json:"-"`
}

func (User) TableName() string { return "users" }
