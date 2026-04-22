package model

import "time"

// 登录事件类型枚举(M1 安全审计)。
// 新增类型时同步更新 handler.handleServiceError / 邮件通知判定。
const (
	// LoginEventPasswordSuccess 本地邮箱+密码登录成功
	//sayso-lint:ignore hardcoded-secret
	LoginEventPasswordSuccess = "password_success"
	// LoginEventPasswordFailed 密码错或验证码错或账号不存在
	//sayso-lint:ignore hardcoded-secret
	LoginEventPasswordFailed = "password_failed"
	// LoginEventAccountLocked 登录失败次数上限,被临时锁
	LoginEventAccountLocked = "account_locked"
	// LoginEventOAuthSuccess OAuth(Google 等)登录成功
	LoginEventOAuthSuccess = "oauth_success"
	// LoginEventRegister 本地注册
	LoginEventRegister = "register"
)

// LoginEvent 用户登录/注册事件审计行。
//
// 所有字段都允许为空(比如 UserID 在密码错误且账号不存在时为 0,Email 则为请求里填的那个),
// 查询模式:
//   - (user_id, created_at DESC) 查某用户近 N 次登录
//   - (email, created_at DESC) 查某邮箱失败分布,用于发现针对性爆破
//   - (user_id, device_id) 查"这个 device 以前登录过吗",用于新设备检测
type LoginEvent struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID    uint64    `gorm:"not null;default:0;index:idx_login_events_user_time,priority:1;index:idx_login_events_user_device,priority:1" json:"user_id"`
	Email     string    `gorm:"size:255;not null;default:'';index:idx_login_events_email_time,priority:1" json:"email"`
	EventType string    `gorm:"size:32;not null;index:idx_login_events_type_time,priority:1" json:"event_type"`
	DeviceID  string    `gorm:"size:128;not null;default:'';index:idx_login_events_user_device,priority:2" json:"device_id"`
	IP        string    `gorm:"size:64;not null;default:''" json:"ip"`
	UserAgent string    `gorm:"size:512;not null;default:''" json:"user_agent"`
	Reason    string    `gorm:"size:128;not null;default:''" json:"reason"`
	CreatedAt time.Time `gorm:"index:idx_login_events_user_time,priority:2,sort:desc;index:idx_login_events_email_time,priority:2,sort:desc;index:idx_login_events_type_time,priority:2,sort:desc" json:"created_at"`
}

func (LoginEvent) TableName() string { return "login_events" }
