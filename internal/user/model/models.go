package model

import (
	"time"
)

// 用户生命周期状态枚举(M1.7)。
//
// 状态机:
//
//	 注册 ─────► pending_verify ─► active ◄────► banned
//	                                │             │
//	                                ▼             ▼
//	                              deleted (pseudo 化,保留壳用于审计/FK)
//	                                │
//	                                ▼
//	                              purge (GDPR 硬删,隔冷却期)
//
// 约束:
//   - 仅 active 可登录 / 发 agent / 调 LLM
//   - pending_verify 禁止登录,但允许自助重发验证码
//   - banned 由管理员置,可恢复为 active
//   - deleted 为逻辑删除,email 已 pseudo 化,原 email 可被新用户复用
//   - deleted → purge 由独立 job 推进,冷却期由 config 控制
const (
	// StatusPendingVerify 邮箱待验证(注册后默认状态)
	StatusPendingVerify = int32(0)
	// StatusActive 正常
	StatusActive = int32(1)
	// StatusBanned 已封禁
	StatusBanned = int32(2)
	// StatusDeleted 已注销(逻辑删除,PII 已 pseudo 化)
	StatusDeleted = int32(3)
)

// User 用户主表模型。
// Email 唯一索引,Status 语义见上方状态机注释。
//
// DeletedAt 字段说明(M1.7):
//   - 原为 gorm.DeletedAt 自动软删列,在引入 status=deleted 后已不再承担"软删除"语义;
//   - 现在仅作为"注销发生时间"记录,由 DeleteAccount 显式写入,给 GDPR purge 冷却期用;
//   - 列名保留 deleted_at 兼容历史数据,但 GORM 不再自动过滤 —— 生命周期完全由 status 表达。
type User struct {
	ID             uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	Email          string     `gorm:"size:255;not null;uniqueIndex:uk_users_email" json:"email"`
	PasswordHash   string     `gorm:"size:255;not null" json:"-"`
	DisplayName    string     `gorm:"size:64" json:"display_name"`
	AvatarURL      string     `gorm:"size:512" json:"avatar_url"`
	Status         int32      `gorm:"not null;default:1;index:idx_users_status" json:"status"`
	// EmailVerifiedAt M1.1 邮箱验证事实源:
	//   - 本地注册:消费 email_code 成功即写 now() —— 6 位码已经证明邮箱所有权
	//   - OAuth 登录:IdP 返 email_verified=true 时写 now();false 则保持 nil + status=pending_verify
	//   - VerifyEmail(token) 成功时写 now() + 把 status 推到 active
	// 非空即视为"已验证",status == active 的用户 EmailVerifiedAt 必非空(不变式)。
	EmailVerifiedAt *time.Time `gorm:"column:email_verified_at" json:"email_verified_at"`
	LastLoginAt    *time.Time `json:"last_login_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	DeletedAt      *time.Time `gorm:"index" json:"-"`                    // 注销时间,非软删标记;GDPR purge 冷却期从此起算
	DeletedReason  string     `gorm:"size:64;column:deleted_reason" json:"-"` // 注销原因(self/admin/policy),给审计用
}

func (User) TableName() string { return "users" }
