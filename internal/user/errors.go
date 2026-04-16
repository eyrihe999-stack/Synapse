// errors.go user 模块错误码与哨兵错误变量定义。
//
// 错误码格式 HHHSSCCCC:
//   - HHH:HTTP 状态码 (400/401/404/409/500)
//   - SS:模块号 01 = user
//   - CCCC:业务码
//
// 业务错误统一以 HTTP 200 + body 业务码返回。
// 仅 ErrUserInternal 使用 500。
package user

import "errors"

// ─── 400 段:请求/业务校验 ────────────────────────────────────────────────────

const (
	// CodeInvalidEmail 邮箱格式非法
	CodeInvalidEmail = 400010010
	// CodePasswordTooShort 密码长度不足
	CodePasswordTooShort = 400010011
)

// ─── 401 段:鉴权 ─────────────────────────────────────────────────────────────

const (
	// CodeInvalidCredentials 邮箱或密码错误
	CodeInvalidCredentials = 401010010
	// CodeInvalidRefreshToken refresh token 无效或已过期
	CodeInvalidRefreshToken = 401010011
)

// ─── 404 段:资源不存在 ──────────────────────────────────────────────────────

const (
	// CodeUserNotFound 用户不存在
	CodeUserNotFound = 404010010
)

// ─── 409 段:冲突 ─────────────────────────────────────────────────────────────

const (
	// CodeEmailAlreadyRegistered 邮箱已注册
	CodeEmailAlreadyRegistered = 409010010
)

// ─── 500 段:内部错误 ────────────────────────────────────────────────────────

const (
	// CodeUserInternal 内部错误
	CodeUserInternal = 500010000
)

// ─── Sentinel Errors ────────────────────────────────────────────────────────

var (
	// ErrInvalidEmail 邮箱格式非法
	ErrInvalidEmail = errors.New("user: invalid email format")
	// ErrPasswordTooShort 密码长度不足(至少 8 位)
	ErrPasswordTooShort = errors.New("user: password too short")
	// ErrInvalidCredentials 邮箱或密码错误
	ErrInvalidCredentials = errors.New("user: invalid credentials")
	// ErrInvalidRefreshToken refresh token 无效或已过期
	ErrInvalidRefreshToken = errors.New("user: invalid refresh token")
	// ErrUserNotFound 用户不存在
	ErrUserNotFound = errors.New("user: not found")
	// ErrEmailAlreadyRegistered 邮箱已注册
	ErrEmailAlreadyRegistered = errors.New("user: email already registered")
	// ErrUserInternal 内部错误
	ErrUserInternal = errors.New("user: internal error")
)
