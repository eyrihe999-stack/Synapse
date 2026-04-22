// builder.go 验证码邮件主题 + 正文渲染。
//
// 模板走 embed FS,zh/en 两套 HTML。新增语言:加一份 templates/login_{xx}.html
// 并在 subjects 表里加对应条目即可。
package email

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"time"
)

//go:embed templates/login_zh.html templates/login_en.html
//go:embed templates/password_reset_zh.html templates/password_reset_en.html
//go:embed templates/email_verify_zh.html templates/email_verify_en.html
//go:embed templates/new_device_login_zh.html templates/new_device_login_en.html
//go:embed templates/email_changed_zh.html templates/email_changed_en.html
//go:embed templates/invitation_zh.html templates/invitation_en.html
var templatesFS embed.FS

// subjects 按 locale 选邮件主题。缺失时回退 en。
var subjects = map[string]string{
	"zh": "Synapse 登录验证码",
	"en": "Synapse Sign-in Verification Code",
}

// passwordResetSubjects 按 locale 选密码重置邮件主题。
var passwordResetSubjects = map[string]string{
	"zh": "Synapse 密码重置",
	"en": "Synapse Password Reset",
}

// emailVerifySubjects 按 locale 选激活邮件主题 (M1.1)。
var emailVerifySubjects = map[string]string{
	"zh": "Synapse 邮箱验证",
	"en": "Verify your Synapse email",
}

// newDeviceLoginSubjects 按 locale 选新设备登录通知主题。
var newDeviceLoginSubjects = map[string]string{
	"zh": "Synapse 安全提醒:新设备登录",
	"en": "Synapse security alert: new sign-in",
}

// emailChangedSubjects 按 locale 选"邮箱已变更"通知主题。
var emailChangedSubjects = map[string]string{
	"zh": "Synapse 安全提醒:邮箱已变更",
	"en": "Synapse security alert: email changed",
}

// invitationSubjects 按 locale 选组织邀请邮件主题。
// %s 会被 orgName 替换,让收件人在列表里一眼看出是哪个组织。
var invitationSubjects = map[string]string{
	"zh": "Synapse 组织邀请:%s",
	"en": "Synapse invitation: %s",
}

// InvitationType 指明邀请邮件的业务语义,模板据此切换文案。
type InvitationType string

const (
	// InvitationTypeMember 普通成员邀请
	InvitationTypeMember InvitationType = "member"
	// InvitationTypeOwnershipTransfer 所有权转让邀请
	InvitationTypeOwnershipTransfer InvitationType = "ownership_transfer"
)

// BuildVerificationEmail 渲染验证码邮件,返回 (subject, htmlBody)。
//
// locale:
//   - "zh" → 中文模板 + 中文主题
//   - 其他值(包括空串) → 英文模板 + 英文主题
//
// 任何模板错误都回退到纯文本 body,确保永远能发出去。
func BuildVerificationEmail(locale, code string, expiresMinutes int) (string, string) {
	tplFile := "templates/login_en.html"
	subject := subjects["en"]
	if locale == "zh" {
		tplFile = "templates/login_zh.html"
		subject = subjects["zh"]
	}

	fallback := fmt.Sprintf("Your verification code is: %s (expires in %d minutes)", code, expiresMinutes)

	raw, err := templatesFS.ReadFile(tplFile)
	if err != nil {
		return subject, fallback
	}
	tpl, err := template.New("email").Parse(string(raw))
	if err != nil {
		return subject, fallback
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, map[string]any{
		"Code":           code,
		"ExpiresMinutes": expiresMinutes,
	}); err != nil {
		return subject, fallback
	}
	return subject, buf.String()
}

// BuildNewDeviceLoginEmail 渲染"新设备登录"通知邮件,返回 (subject, htmlBody)。
//
// 调用方应在"该 user_id + device_id 组合首次成功登录"时触发;重复登录不发。
// when 建议用 "2006-01-02 15:04:05 MST" 格式,service 层已按用户 locale 区本地化。
// ip / userAgent 是审计用,模板会做 HTML escape 防 UA 里的脏字符破坏排版。
func BuildNewDeviceLoginEmail(locale, when, ip, userAgent string) (string, string) {
	tplFile := "templates/new_device_login_en.html"
	subject := newDeviceLoginSubjects["en"]
	if locale == "zh" {
		tplFile = "templates/new_device_login_zh.html"
		subject = newDeviceLoginSubjects["zh"]
	}

	fallback := fmt.Sprintf("New sign-in at %s from %s (%s). If this wasn't you, reset your password.", when, ip, userAgent)

	raw, err := templatesFS.ReadFile(tplFile)
	if err != nil {
		return subject, fallback
	}
	tpl, err := template.New("new_device_login").Parse(string(raw))
	if err != nil {
		return subject, fallback
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, map[string]any{
		"When":      when,
		"IP":        ip,
		"UserAgent": userAgent,
	}); err != nil {
		return subject, fallback
	}
	return subject, buf.String()
}

// BuildEmailChangedNotice 渲染"邮箱已变更"通知邮件,返回 (subject, htmlBody)。
//
// 发送对象是**旧邮箱**,告知其账号邮箱已被改走。这是防"账号被盗后改邮箱接管"的标准告警。
// when 建议 "2006-01-02 15:04:05 UTC";oldEmail/newEmail 会在模板里做 HTML escape。
func BuildEmailChangedNotice(locale, when, oldEmail, newEmail string) (string, string) {
	tplFile := "templates/email_changed_en.html"
	subject := emailChangedSubjects["en"]
	if locale == "zh" {
		tplFile = "templates/email_changed_zh.html"
		subject = emailChangedSubjects["zh"]
	}

	fallback := fmt.Sprintf("Your Synapse email was changed from %s to %s at %s. If this wasn't you, contact support.", oldEmail, newEmail, when)

	raw, err := templatesFS.ReadFile(tplFile)
	if err != nil {
		return subject, fallback
	}
	tpl, err := template.New("email_changed").Parse(string(raw))
	if err != nil {
		return subject, fallback
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, map[string]any{
		"When":     when,
		"OldEmail": oldEmail,
		"NewEmail": newEmail,
	}); err != nil {
		return subject, fallback
	}
	return subject, buf.String()
}

// BuildEmailVerificationEmail 渲染邮箱激活邮件 (M1.1),返回 (subject, htmlBody)。
//
// link 是后端拼好的一次性激活链接 (例如 https://app.example.com/auth/email/verify?token=xxx),
// 模板复用 password_reset 的排版框架,仅文案和 CTA 不同。
// 渲染错误回退到纯文本,确保邮件仍可寄出。
func BuildEmailVerificationEmail(locale, link string, expiresMinutes int) (string, string) {
	tplFile := "templates/email_verify_en.html"
	subject := emailVerifySubjects["en"]
	if locale == "zh" {
		tplFile = "templates/email_verify_zh.html"
		subject = emailVerifySubjects["zh"]
	}

	fallback := fmt.Sprintf("Verify your email: %s (expires in %d minutes)", link, expiresMinutes)

	raw, err := templatesFS.ReadFile(tplFile)
	if err != nil {
		return subject, fallback
	}
	tpl, err := template.New("email_verify").Parse(string(raw))
	if err != nil {
		return subject, fallback
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, map[string]any{
		"Link":           link,
		"ExpiresMinutes": expiresMinutes,
	}); err != nil {
		return subject, fallback
	}
	return subject, buf.String()
}

// BuildInvitationEmail 渲染组织邀请邮件,返回 (subject, htmlBody)。
//
// invType:
//   - InvitationTypeMember → 普通成员邀请("邀请您加入"/"invited you to join")
//   - InvitationTypeOwnershipTransfer → 所有权转让邀请("邀请您接手"/"invited you to take over")
//
// expiresAt 会按 locale 本地格式化为可读字符串(zh: "2006-01-02 15:04"、en: "Jan 2, 2006 15:04 UTC")。
// link 指向前端邀请列表页(例:http://frontend/invitations/mine),用户点进去登录后可逐条 accept/reject。
// 任何模板错误回退纯文本,保证邮件仍能寄出。
func BuildInvitationEmail(locale, orgName, inviterName, roleName string, invType InvitationType, link string, expiresAt time.Time) (string, string) {
	tplFile := "templates/invitation_en.html"
	subjectFmt := invitationSubjects["en"]
	title, action, typeLabel, expiresStr := invitationCopyEn(invType, expiresAt)
	roleLabel := roleName
	if locale == "zh" {
		tplFile = "templates/invitation_zh.html"
		subjectFmt = invitationSubjects["zh"]
		title, action, typeLabel, expiresStr = invitationCopyZh(invType, expiresAt)
	}

	subject := fmt.Sprintf(subjectFmt, orgName)
	fallback := fmt.Sprintf("%s %s (role: %s, expires %s). Open %s to respond.", inviterName, action+" "+orgName, roleLabel, expiresStr, link)

	raw, err := templatesFS.ReadFile(tplFile)
	if err != nil {
		return subject, fallback
	}
	tpl, err := template.New("invitation").Parse(string(raw))
	if err != nil {
		return subject, fallback
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, map[string]any{
		"Title":       title,
		"InviterName": inviterName,
		"OrgName":     orgName,
		"ActionText":  action,
		"TypeLabel":   typeLabel,
		"RoleLabel":   roleLabel,
		"ExpiresAt":   expiresStr,
		"Link":        link,
	}); err != nil {
		return subject, fallback
	}
	return subject, buf.String()
}

// invitationCopyZh 返回中文邀请邮件的标题/动作/类型标签/过期时间字符串。
func invitationCopyZh(invType InvitationType, expiresAt time.Time) (title, action, typeLabel, expiresStr string) {
	expiresStr = expiresAt.Format("2006-01-02 15:04 UTC")
	if invType == InvitationTypeOwnershipTransfer {
		return "组织所有权转让邀请", "接手", "所有权转让", expiresStr
	}
	return "组织邀请", "加入", "成员邀请", expiresStr
}

// invitationCopyEn 返回英文邀请邮件的标题/动作/类型标签/过期时间字符串。
func invitationCopyEn(invType InvitationType, expiresAt time.Time) (title, action, typeLabel, expiresStr string) {
	expiresStr = expiresAt.Format("Jan 2, 2006 15:04 UTC")
	if invType == InvitationTypeOwnershipTransfer {
		return "Ownership Transfer Invitation", "take over", "Ownership transfer", expiresStr
	}
	return "Organization Invitation", "join", "Member invitation", expiresStr
}

// BuildPasswordResetEmail 渲染密码重置邮件,返回 (subject, htmlBody)。
//
// link 是后端拼好的一次性重置链接(例如 https://app.example.com/reset-password?token=xxx),
// 模板直接塞进 <a href>;html/template 的 URL escape 会兜底防 XSS。
// 渲染错误回退到纯文本,确保邮件仍可寄出。
func BuildPasswordResetEmail(locale, link string, expiresMinutes int) (string, string) {
	tplFile := "templates/password_reset_en.html"
	subject := passwordResetSubjects["en"]
	if locale == "zh" {
		tplFile = "templates/password_reset_zh.html"
		subject = passwordResetSubjects["zh"]
	}

	fallback := fmt.Sprintf("Reset your password: %s (expires in %d minutes)", link, expiresMinutes)

	raw, err := templatesFS.ReadFile(tplFile)
	if err != nil {
		return subject, fallback
	}
	tpl, err := template.New("pwd_reset").Parse(string(raw))
	if err != nil {
		return subject, fallback
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, map[string]any{
		"Link":           link,
		"ExpiresMinutes": expiresMinutes,
	}); err != nil {
		return subject, fallback
	}
	return subject, buf.String()
}
