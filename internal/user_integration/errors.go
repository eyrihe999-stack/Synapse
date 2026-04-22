// errors.go user_integration 模块 sentinel 错误。
// 错误码段 SS=17(user_integration 模块)。
package user_integration

import "errors"

const (
	CodeIntegrationNotFound = 404170020
	CodeIntegrationInternal = 500170000
)

var (
	// ErrIntegrationNotFound 指定 (user_id, provider, external_account_id) 没记录。
	ErrIntegrationNotFound = errors.New("user_integration: not found")

	// ErrIntegrationInternal 底层 DB / IO 错。
	ErrIntegrationInternal = errors.New("user_integration: internal error")
)
