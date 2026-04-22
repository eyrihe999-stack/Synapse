// normalize.go user 模块入口字段净化:邮箱大小写归一、device_name 长度截断。
package service

import "strings"

// maxDeviceNameLen device_name 允许的最大字节长度。
// 超过会在 service 层截断 —— 防用户 / 客户端塞 MB 级字符串撑爆 Redis session JSON,
// 也顺带防前端展示侧 XSS 放大面(前端仍需做 escape,这里做长度最小防线)。
const maxDeviceNameLen = 128

// normalizeEmail 统一 email 输入:去首尾空白 + 全转小写。
// 用途:避免用户大小写混用导致"Alice@x 注册、alice@x 登不进"的 UX 问题,
// 以及 collation 变更时的兼容风险。入口包括 Register/Login/SendEmailCode/
// RequestPasswordReset/ConfirmPasswordReset/ChangeEmail/OAuth 合并。
func normalizeEmail(e string) string {
	return strings.ToLower(strings.TrimSpace(e))
}

// truncDeviceName 截断 device_name 到 maxDeviceNameLen 字节(按 byte 而非 rune,
// 因为限存储 / 出站体积,按字节最直接);rune 截在多字节字符中间不会崩溃,
// 后端只是存 + 回显,前端渲染时会按 UTF-8 解码显示为 "?" 占位,不影响安全。
func truncDeviceName(name string) string {
	if len(name) <= maxDeviceNameLen {
		return name
	}
	return name[:maxDeviceNameLen]
}
