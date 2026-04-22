// errors.go pwdpolicy 的 sentinel errors。
//
// 包自身不知道 user 模块错误码;user 层通过 errors.Is 映射到自己的错误码。
package pwdpolicy

import "errors"

var (
	// ErrTooShort 密码长度不足策略要求。
	ErrTooShort = errors.New("pwdpolicy: password too short")
	// ErrTooCommon 密码命中弱密名单。
	ErrTooCommon = errors.New("pwdpolicy: password too common")
)
