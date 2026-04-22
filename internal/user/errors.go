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
	// CodeInvalidEmailCode 邮箱验证码错误或已被作废
	CodeInvalidEmailCode = 400010012
	// CodeEmailCodeNotFound 邮箱验证码不存在或已过期
	CodeEmailCodeNotFound = 400010013
	// CodeDailyLimitReached 单日发送次数达到上限
	CodeDailyLimitReached = 429010010
	// CodeResetTokenInvalid 密码重置 token 无效或已过期
	CodeResetTokenInvalid = 400010014
	// CodeAccountLocked 账号因连续登录失败被临时锁定
	CodeAccountLocked = 423010010
	// CodeRegisterRateExceeded 注册请求过于频繁(per-IP 滑动窗口)
	CodeRegisterRateExceeded = 429010011
	// CodePasswordTooCommon 密码命中常用密码名单(top-N 弱密 bloom filter)
	CodePasswordTooCommon = 400010015
	// CodeOAuthStateInvalid OAuth 登录状态 cookie 校验失败 / 过期
	CodeOAuthStateInvalid = 400010016
	// CodeOAuthEmailUnverified IdP 返回的邮箱未验证,拒绝自动合并
	CodeOAuthEmailUnverified = 400010017
	// CodeOAuthProviderDisabled 指定 provider 未启用
	CodeOAuthProviderDisabled = 400010018
	// CodeOAuthExchangeExpired 前端 exchange code 不存在或已用过
	CodeOAuthExchangeExpired = 400010019
	// CodeAccountAlreadyDeleted 账号已经处于注销态,重复调用 DeleteAccount
	CodeAccountAlreadyDeleted = 400010020
	// CodeDeletePasswordRequired 本地账号注销必须带密码二次确认
	CodeDeletePasswordRequired = 400010021
	// CodeVerifyTokenInvalid M1.1 激活 token 无效或已过期
	CodeVerifyTokenInvalid = 400010022
	// CodeEmailAlreadyVerified M1.1 邮箱已验证,无需再次激活
	CodeEmailAlreadyVerified = 400010023
	// CodeAccountPendingVerify M1.1 账号邮箱尚未验证,被前置 guard 拦下
	CodeAccountPendingVerify = 403010011
	// CodeEmailSameAsCurrent 改邮箱时新邮箱和当前邮箱相同
	CodeEmailSameAsCurrent = 400010024
	// CodeChangePasswordCodeRequired OAuth-only 账号改密必须带当前邮箱 code 二次确认
	CodeChangePasswordCodeRequired = 400010025
	// CodeLocalPasswordRequired OAuth-only 账号改邮箱前需先绑本地密码
	CodeLocalPasswordRequired = 400010026
	// CodeResendTooFrequent 重发激活邮件过于频繁,前一次不足 cooldown 时间
	CodeResendTooFrequent = 429010012
	// CodeRequestTooFrequent 通用 per-email / per-user 请求过于频繁
	// (SendEmailCode / RequestPasswordReset / ChangeEmail 等入口共用)
	CodeRequestTooFrequent = 429010013
	// CodeSessionExpired 会话绝对 TTL 到期,需要重新登录
	CodeSessionExpired = 401010013
	// CodeLoginIPRateLimited 同一 IP 登录失败次数过多,被 per-IP 临时锁定
	CodeLoginIPRateLimited = 429010014
)

// ─── 401 段:鉴权 ─────────────────────────────────────────────────────────────

const (
	// CodeInvalidCredentials 邮箱或密码错误
	CodeInvalidCredentials = 401010010
	// CodeInvalidRefreshToken refresh token 无效或已过期
	CodeInvalidRefreshToken = 401010011
	// CodeSessionLimitReached 设备数量已达上限
	CodeSessionLimitReached = 403010010
	// CodeSessionRevoked session 已被吊销(设备被踢下线)
	CodeSessionRevoked = 401010012
)

// ─── 404 段:资源不存在 ──────────────────────────────────────────────────────

const (
	// CodeUserNotFound 用户不存在
	CodeUserNotFound = 404010010
	// CodeSessionNotFound session 不存在
	CodeSessionNotFound = 404010011
)

// ─── 409 段:冲突 ─────────────────────────────────────────────────────────────

const (
	// CodeEmailAlreadyRegistered 邮箱已注册
	CodeEmailAlreadyRegistered = 409010010
	// CodeOwnerOfActiveOrgs M3.7 用户是某 active org 的 owner,不允许直接自注销;
	// 响应体附 orgs 列表,前端引导转让(M3.3)或解散。
	CodeOwnerOfActiveOrgs = 409010011
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
	// ErrPasswordTooShort 密码长度不足(M1.5 默认 10 位,可配)
	ErrPasswordTooShort = errors.New("user: password too short")
	// ErrPasswordTooCommon 密码命中常用密码名单(top-N 弱密)
	ErrPasswordTooCommon = errors.New("user: password too common")
	// ErrInvalidCredentials 邮箱或密码错误
	ErrInvalidCredentials = errors.New("user: invalid credentials")
	// ErrInvalidRefreshToken refresh token 无效或已过期
	ErrInvalidRefreshToken = errors.New("user: invalid refresh token")
	// ErrUserNotFound 用户不存在
	ErrUserNotFound = errors.New("user: not found")
	// ErrEmailAlreadyRegistered 邮箱已注册
	ErrEmailAlreadyRegistered = errors.New("user: email already registered")
	// ErrSessionLimitReached 设备数量已达上限
	ErrSessionLimitReached = errors.New("user: session limit reached")
	// ErrSessionRevoked session 已被吊销
	ErrSessionRevoked = errors.New("user: session revoked")
	// ErrSessionNotFound session 不存在
	ErrSessionNotFound = errors.New("user: session not found")
	// ErrUserInternal 内部错误
	ErrUserInternal = errors.New("user: internal error")
	// ErrInvalidEmailCode 邮箱验证码错误
	ErrInvalidEmailCode = errors.New("user: invalid email code")
	// ErrEmailCodeNotFound 邮箱验证码不存在或已过期
	ErrEmailCodeNotFound = errors.New("user: email code not found")
	// ErrDailyLimitReached 发送次数达到单日上限
	ErrDailyLimitReached = errors.New("user: daily verification limit reached")
	// ErrResetTokenInvalid 密码重置 token 无效或已过期
	ErrResetTokenInvalid = errors.New("user: reset token invalid")
	// ErrAccountLocked 账号因连续登录失败被临时锁定
	ErrAccountLocked = errors.New("user: account locked")
	// ErrRegisterRateExceeded 注册请求过于频繁
	ErrRegisterRateExceeded = errors.New("user: register rate exceeded")
	// ErrOAuthStateInvalid OAuth 登录 state cookie 校验失败 / 过期
	ErrOAuthStateInvalid = errors.New("user: oauth state invalid")
	// ErrOAuthEmailUnverified IdP 返回 email 未验证,拒绝自动合并到既有账号
	ErrOAuthEmailUnverified = errors.New("user: oauth email unverified")
	// ErrOAuthProviderDisabled 请求的 provider 未启用
	ErrOAuthProviderDisabled = errors.New("user: oauth provider disabled")
	// ErrOAuthExchangeExpired 前端交换 token 的 exchange code 不存在或已过期
	ErrOAuthExchangeExpired = errors.New("user: oauth exchange expired")
	// ErrAccountAlreadyDeleted 账号已处于注销态,不允许再次注销
	ErrAccountAlreadyDeleted = errors.New("user: account already deleted")
	// ErrDeletePasswordRequired 有密码的本地账号注销需二次输入密码确认
	ErrDeletePasswordRequired = errors.New("user: password required for account deletion")
	// ErrVerifyTokenInvalid M1.1 邮箱激活 token 不存在/过期/已用过,不区分具体原因防枚举
	ErrVerifyTokenInvalid = errors.New("user: email verification token invalid")
	// ErrEmailAlreadyVerified M1.1 邮箱已经完成验证,重复激活/重发被拦
	ErrEmailAlreadyVerified = errors.New("user: email already verified")
	// ErrAccountPendingVerify M1.1 账号邮箱未验证,被跨模块前置 guard 拦下
	ErrAccountPendingVerify = errors.New("user: account pending email verification")
	// ErrEmailSameAsCurrent 改邮箱时新邮箱和当前邮箱相同
	ErrEmailSameAsCurrent = errors.New("user: new email same as current")
	// ErrChangePasswordCodeRequired OAuth-only 账号改密时必须带当前邮箱的 6 位 code 二次确认
	ErrChangePasswordCodeRequired = errors.New("user: email code required for oauth-only change password")
	// ErrLocalPasswordRequired OAuth-only 账号改邮箱前需先通过 ChangePassword 绑本地密码
	ErrLocalPasswordRequired = errors.New("user: local password required to change email")
	// ErrResendTooFrequent 重发激活邮件间隔不足 per-user cooldown,拒绝以防轰炸/消耗 daily quota
	ErrResendTooFrequent = errors.New("user: resend verification too frequent")
	// ErrRequestTooFrequent 通用 per-email / per-user 请求过于频繁
	// 多处入口复用(SendEmailCode / RequestPasswordReset / ChangeEmail 等)
	ErrRequestTooFrequent = errors.New("user: request too frequent, please wait")
	// ErrSessionExpired 会话绝对 TTL 到期,中间件强制返 401 要求重登
	ErrSessionExpired = errors.New("user: session expired, please login again")
	// ErrLoginIPRateLimited 同一 IP 登录失败次数超限被临时锁
	ErrLoginIPRateLimited = errors.New("user: too many login failures from this IP")
	// ErrOwnerOfActiveOrgs M3.7 用户是某 active org 的 owner,不允许直接自注销。
	// 具体 org 列表通过 OwnerOfActiveOrgsError 结构体携带(errors.As 提取)。
	ErrOwnerOfActiveOrgs = errors.New("user: cannot delete account while owning active organizations")
)

// OwnedOrgSummary 注销 guard 响应里展示给前端的 org 摘要。
type OwnedOrgSummary struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}

// OwnerOfActiveOrgsError M3.7 注销 guard 错误。包装 ErrOwnerOfActiveOrgs 以便 errors.Is,
// 并携带实际阻塞注销的 org 列表,handler 用 errors.As 取出塞进响应体引导前端操作。
type OwnerOfActiveOrgsError struct {
	Orgs []OwnedOrgSummary
}

// Error 实现 error 接口。文案借 sentinel,便于日志统一。
func (e *OwnerOfActiveOrgsError) Error() string {
	return ErrOwnerOfActiveOrgs.Error()
}

// Unwrap 让 errors.Is(err, ErrOwnerOfActiveOrgs) 对本结构体生效。
func (e *OwnerOfActiveOrgsError) Unwrap() error {
	//sayso-lint:ignore log-coverage
	return ErrOwnerOfActiveOrgs
}
