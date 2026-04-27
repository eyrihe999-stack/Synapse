// Package principal principal 身份抽象层。见 model/models.go 顶部 doc。
package principal

import "errors"

// ErrPrincipalInternal principal 模块内部错误哨兵。
var ErrPrincipalInternal = errors.New("principal: internal error")
