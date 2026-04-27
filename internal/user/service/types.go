// types.go user 服务层对外请求 / 响应 DTO 集中定义。
package service

import "time"

// RegisterRequest 用户注册请求。
//
// Code 是 /auth/email/send-code 发到邮箱的 6 位验证码,注册流程的一部分。
// 消费顺序:email-not-taken 检查通过后才消费码,避免用户把登录当注册时白烧码。
type RegisterRequest struct {
	Email       string `json:"email" binding:"required"`
	Password    string `json:"password" binding:"required"`
	Code        string `json:"code" binding:"required"`
	DisplayName string `json:"display_name"`
	DeviceID    string `json:"device_id"`
	DeviceName  string `json:"device_name"`
	LoginIP     string `json:"-"` // handler 层设置,不从 JSON 读取
	UserAgent   string `json:"-"` // handler 层从 Request.UserAgent() 注入,审计用
}

// LoginRequest 用户登录请求。
//
// Code 是 /auth/email/send-code 发到邮箱的 6 位验证码,2FA 式二次校验。
// 消费顺序:密码校验通过后才消费码,避免攻击者拿错密码连续消耗用户的 attempt 预算。
type LoginRequest struct {
	Email      string `json:"email" binding:"required"`
	Password   string `json:"password" binding:"required"`
	Code       string `json:"code" binding:"required"`
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`
	LoginIP    string `json:"-"` // handler 层设置,不从 JSON 读取
	UserAgent  string `json:"-"` // handler 层从 Request.UserAgent() 注入,审计用
}

// RefreshRequest 刷新 token 请求。
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
	DeviceID     string `json:"device_id"`
	DeviceName   string `json:"device_name"`
	LoginIP      string `json:"-"`
}

// UpdateProfileRequest 更新个人信息请求。
type UpdateProfileRequest struct {
	DisplayName *string `json:"display_name"`
	AvatarURL   *string `json:"avatar_url"`
}

// AuthResponse 登录/注册成功后的认证响应。
type AuthResponse struct {
	AccessToken  string      `json:"access_token"`
	RefreshToken string      `json:"refresh_token"`
	ExpiresIn    int         `json:"expires_in"`
	User         UserProfile `json:"user"`
}

// UserProfile 用户公开资料。
//
// PrincipalID 是 user 在 principals 表的身份根 id,channel_members / task.assignee
// / channel_message_reactions 等都存 principal_id。前端很多地方(reaction 自己归属
// 判断 / mention / DM)要用 principal_id 和消息 / 成员行比对,一次冗余到 /users/me
// 避免每处都反查。JSON `string` 保持和 id 同风格(前端用 Number() 转数字)。
type UserProfile struct {
	ID              uint64     `json:"id,string"`
	PrincipalID     uint64     `json:"principal_id,string"`
	Email           string     `json:"email"`
	DisplayName     string     `json:"display_name"`
	AvatarURL       string     `json:"avatar_url"`
	Status          int32      `json:"status"`
	EmailVerifiedAt *time.Time `json:"email_verified_at"`
	LastLoginAt     *time.Time `json:"last_login_at"`
	CreatedAt       time.Time  `json:"created_at"`
}

// SendEmailCodeRequest 发送邮箱验证码请求。
type SendEmailCodeRequest struct {
	Email   string `json:"email" binding:"required"`
	LoginIP string `json:"-"`
}

// SendEmailCodeResponse 发送成功返回体。不回显 code(防止被日志 / 反代抓到)。
type SendEmailCodeResponse struct {
	Email     string    `json:"email"`
	ExpiresIn int       `json:"expires_in"` // 秒
	SentAt    time.Time `json:"sent_at"`
}
