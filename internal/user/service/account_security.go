// account_security.go 已登录用户的账号安全自助操作:改密 / 改邮箱。
//
// 与 M1.3 的 "忘记密码" 流程正交:
//   - PasswordReset 是"丢了密码走邮件 token"流程,无需当前会话
//   - ChangePassword 是"记得旧密码、想换个新密码",需要已登录 + 旧密码二次确认
//
// 改邮箱的所有权证明:
//   - 老邮箱:靠当前 session + 密码二次确认
//   - 新邮箱:要求用户先通过 /auth/email/send-code 向新邮箱发 6 位 code,本接口消费该 code 完成切换
package service

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/user"
	"github.com/eyrihe999-stack/Synapse/internal/common/email"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// emailChangedTimestampFormat 邮箱变更通知里"When"字段的渲染格式。
const emailChangedTimestampFormat = "2006-01-02 15:04:05 UTC"

// changeEmailCooldownKeyPrefix per-user 改邮箱 cooldown Redis key 前缀 (P1)。
// 完整 key: synapse:change_email_cd:{user_id};登记在 internal/common/database/redis.go。
const changeEmailCooldownKeyPrefix = "synapse:change_email_cd"

// changeEmailCooldown per-user 改邮箱 cooldown,60s。
// 防被盗 session 攻击者循环触发改邮箱造成用户混乱(虽然他被 `PasswordHash != ""` 的密码校验挡住,
// 但循环试错仍可做 timing 探测等扰动)。
const changeEmailCooldown = 60 * time.Second

// emailChangedNoticeDailyKeyPrefix per-old-email 每日变更通知配额 key 前缀。
// 完整 key: synapse:email_changed_notice:{old_email}:{YYYY-MM-DD};登记在 internal/common/database/redis.go。
// 独立池,不污染 email_rl(注册/激活邮件配额)。
const emailChangedNoticeDailyKeyPrefix = "synapse:email_changed_notice"

// emailChangedNoticeDailyCap 单个旧邮箱每日收到"邮箱已变更"告警的上限。
// 正常用户一天不会改超过几次邮箱;给到 10 封给测试/脚本留余量,超过就 skip 告警。
// ChangeEmail 入口已有 per-user 60s cooldown 兜底,多数情况下触不到这里。
const emailChangedNoticeDailyCap = 10

// ChangePasswordRequest 已登录改密请求。
//
// 两种账号形态:
//   - 有本地密码:必须带 OldPassword,OldPassword 通过 bcrypt 校验;Code 字段留空
//   - OAuth-only (PasswordHash 空):必须带 Code,从当前邮箱发出的 6 位验证码消费成功才能设密码
//
// 防 session 盗用:session 被盗的 OAuth-only 用户,盗用者拿不到用户邮箱中的 code,无法接管。
type ChangePasswordRequest struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password" binding:"required"`
	Code        string `json:"code"` // OAuth-only 账号必填,本地密码账号可省略
}

// ChangeEmailRequest 改邮箱请求。
//
// Code 必填:用户需要先调 /auth/email/send-code 发给 NewEmail 一个 6 位码,本接口消费。
// Password 对有本地密码的账号强制,OAuth-only 可省略(session 已证账号归属)。
type ChangeEmailRequest struct {
	NewEmail string `json:"new_email" binding:"required"`
	Password string `json:"password"`
	Code     string `json:"code" binding:"required"`
}

// ChangePassword 已登录用户改密码。
//
// 流程:
//  1. 取 living user(pending_verify/active/banned 均可走,banned 只是不给登录,改密不挡)
//  2. 有 PasswordHash 则必须带 OldPassword 且 bcrypt 匹配
//  3. NewPassword 过密码策略
//  4. bcrypt(NewPassword) → UpdateFields
//  5. LogoutAll(踢所有设备,含当前设备 —— 强制用新密码重登,跟 M1.3 的 ConfirmPasswordReset 口径一致)
//
// 返回 ErrUserNotFound / ErrInvalidCredentials / ErrPasswordTooShort / ErrPasswordTooCommon / ErrUserInternal。
func (s *userService) ChangePassword(ctx context.Context, userID uint64, req ChangePasswordRequest) error {
	u, err := s.repo.FindLivingByID(ctx, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.log.WarnCtx(ctx, "change password:用户不存在", nil)
			return fmt.Errorf("find user: %w", user.ErrUserNotFound)
		}
		s.log.ErrorCtx(ctx, "change password:查询用户失败", err, nil)
		return fmt.Errorf("find user: %w: %w", err, user.ErrUserInternal)
	}

	// 两条独立路径的二次确认,防 session 被盗后悄悄换密:
	//   - 本地密码账号:校验 OldPassword
	//   - OAuth-only 账号:消费发到当前邮箱的 6 位 code(证明对邮箱的所有权)
	if u.PasswordHash != "" {
		if req.OldPassword == "" {
			s.log.WarnCtx(ctx, "change password:未提供旧密码", map[string]interface{}{"user_id": u.ID})
			return fmt.Errorf("old password required: %w", user.ErrInvalidCredentials)
		}
		if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.OldPassword)); err != nil {
			s.log.WarnCtx(ctx, "change password:旧密码不匹配", map[string]interface{}{"user_id": u.ID})
			return fmt.Errorf("old password mismatch: %w", user.ErrInvalidCredentials)
		}
	} else {
		// OAuth-only 账号首次设密码,必须带当前邮箱的 code。
		// 用户在调本接口前应先调 /auth/email/send-code(传 u.Email)拿到验证码。
		if req.Code == "" {
			s.log.WarnCtx(ctx, "change password:oauth-only 账号未提供 email code", map[string]interface{}{"user_id": u.ID})
			return fmt.Errorf("code required: %w", user.ErrChangePasswordCodeRequired)
		}
		if err := s.consumeEmailCode(ctx, u.Email, req.Code); err != nil {
			s.log.WarnCtx(ctx, "change password:email code 校验失败", map[string]interface{}{"user_id": u.ID})
			//sayso-lint:ignore sentinel-wrap
			return err
		}
	}

	if err := s.checkPassword(req.NewPassword); err != nil {
		//sayso-lint:ignore sentinel-wrap
		return err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		s.log.ErrorCtx(ctx, "change password:新密码哈希失败", err, map[string]interface{}{"user_id": u.ID})
		return fmt.Errorf("hash password: %w: %w", err, user.ErrUserInternal)
	}

	if err := s.repo.UpdateFields(ctx, u.ID, map[string]interface{}{"password_hash": string(hash)}); err != nil {
		s.log.ErrorCtx(ctx, "change password:落库失败", err, map[string]interface{}{"user_id": u.ID})
		return fmt.Errorf("update password: %w: %w", err, user.ErrUserInternal)
	}

	// 改密成功 → 清零 login_fail + 踢所有 session(含当前)。
	// 踢当前 session 会让本次 API 响应后,客户端下一次请求就被 401,强制回登录页,体验上显而易见但安全性最高。
	if s.loginGuard != nil {
		//sayso-lint:ignore err-swallow
		_ = s.loginGuard.ResetLoginFail(ctx, u.Email) // best-effort
	}
	if err := s.sessionStore.DeleteAll(ctx, u.ID); err != nil {
		s.log.ErrorCtx(ctx, "change password 后 LogoutAll 失败", err, map[string]interface{}{"user_id": u.ID})
		return fmt.Errorf("logout all: %w: %w", err, user.ErrUserInternal)
	}

	s.log.InfoCtx(ctx, "密码已变更", map[string]interface{}{"user_id": u.ID})
	return nil
}

// ChangeEmail 已登录用户改邮箱。
//
// 流程:
//  1. 取 living user
//  2. 校验 NewEmail 格式,和旧 email 不同
//  3. 有 PasswordHash 必须带 Password 二次确认
//  4. 占用检查:FindLivingByEmail(deleted 用户的 pseudo email 不会撞,因为 pseudo 带 @synapse.invalid)
//  5. 消费 NewEmail 上的 6 位 code —— 证明新邮箱所有权
//  6. 事务内更新 users.email + email_verified_at=now();撞 unique 索引时映射回 ErrEmailAlreadyRegistered
//  7. LogoutAll —— email 是登录名,改完必须用新 email 重登
//
// 返回 ErrUserNotFound / ErrInvalidEmail / ErrEmailSameAsCurrent / ErrEmailAlreadyRegistered /
//
//	ErrInvalidCredentials / ErrEmailCodeNotFound / ErrInvalidEmailCode / ErrUserInternal。
func (s *userService) ChangeEmail(ctx context.Context, userID uint64, req ChangeEmailRequest) error {
	req.NewEmail = normalizeEmail(req.NewEmail)

	// P1 per-user 60s cooldown,防被盗 session 循环触发改邮箱扰动用户。
	if s.loginGuard != nil {
		key := fmt.Sprintf("%s:%d", changeEmailCooldownKeyPrefix, userID)
		count, cErr := s.loginGuard.TouchCounter(ctx, key, changeEmailCooldown)
		if cErr == nil && count > 1 {
			s.log.WarnCtx(ctx, "change email 触发 cooldown", map[string]interface{}{"user_id": userID, "count": count})
			return fmt.Errorf("change email too frequent: %w", user.ErrRequestTooFrequent)
		}
	}

	u, err := s.repo.FindLivingByID(ctx, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.log.WarnCtx(ctx, "change email:用户不存在", nil)
			return fmt.Errorf("find user: %w", user.ErrUserNotFound)
		}
		s.log.ErrorCtx(ctx, "change email:查询用户失败", err, nil)
		return fmt.Errorf("find user: %w: %w", err, user.ErrUserInternal)
	}

	//sayso-lint:ignore err-swallow
	if _, err := mail.ParseAddress(req.NewEmail); err != nil { // 丢弃 parsed 只看 err
		return fmt.Errorf("invalid email: %w", user.ErrInvalidEmail)
	}
	if req.NewEmail == u.Email {
		return fmt.Errorf("same as current: %w", user.ErrEmailSameAsCurrent)
	}

	// OAuth-only 账号禁止直接改邮箱 —— 新邮箱 code 只证明"对新邮箱的所有权",
	// 不证明"对本账号的所有权"。若允许 PasswordHash=="" 绕过本地密码校验,
	// session 被盗的 OAuth-only 用户会被把 email 换到攻击者掌控的邮箱完成接管。
	// 强制先走 ChangePassword(要当前邮箱 code 确认)绑一个本地密码,再改邮箱。
	if u.PasswordHash == "" {
		s.log.WarnCtx(ctx, "change email:oauth-only 账号未绑本地密码,拒绝直接改邮箱", map[string]interface{}{"user_id": u.ID})
		return fmt.Errorf("local password required: %w", user.ErrLocalPasswordRequired)
	}
	if req.Password == "" {
		s.log.WarnCtx(ctx, "change email:未提供密码", map[string]interface{}{"user_id": u.ID})
		return fmt.Errorf("password required: %w", user.ErrInvalidCredentials)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)); err != nil {
		s.log.WarnCtx(ctx, "change email:密码不匹配", map[string]interface{}{"user_id": u.ID})
		return fmt.Errorf("password mismatch: %w", user.ErrInvalidCredentials)
	}

	// 新邮箱占用预检:有友好错误 + 省一次 code 消费。unique 索引是最终兜底。
	//sayso-lint:ignore err-swallow
	if _, err := s.repo.FindLivingByEmail(ctx, req.NewEmail); err == nil {
		return fmt.Errorf("email taken: %w", user.ErrEmailAlreadyRegistered)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		s.log.ErrorCtx(ctx, "change email:查新邮箱占用失败", err, nil)
		return fmt.Errorf("check email: %w: %w", err, user.ErrUserInternal)
	}

	// 消费 NewEmail 的 code —— 证明"用户对新邮箱有收件权"。
	// consumeEmailCode 的错误 sentinel 已在 error_map 覆盖,直接透传。
	if err := s.consumeEmailCode(ctx, req.NewEmail, req.Code); err != nil {
		//sayso-lint:ignore sentinel-wrap
		return err
	}

	now := time.Now().UTC()
	oldEmail := u.Email
	updates := map[string]interface{}{
		"email":             req.NewEmail,
		"email_verified_at": now,
	}
	if err := s.repo.UpdateFields(ctx, u.ID, updates); err != nil {
		// 竞争场景:另一个请求刚好把此 email 占了,或 NewEmail 被第三个账号抢先占用
		if isDupEntryErr(err) {
			s.log.WarnCtx(ctx, "change email:unique 冲突(竞争)", map[string]interface{}{"user_id": u.ID, "email": req.NewEmail})
			return fmt.Errorf("email taken: %w", user.ErrEmailAlreadyRegistered)
		}
		s.log.ErrorCtx(ctx, "change email:落库失败", err, map[string]interface{}{"user_id": u.ID})
		return fmt.Errorf("update email: %w: %w", err, user.ErrUserInternal)
	}

	// 通知旧邮箱:标准防账号盗用最佳实践 —— 让被盗用户在"攻击者已改走 email"时也能收到告警。
	// best-effort,发失败不阻塞主流程(DB 已落库)。
	s.notifyEmailChanged(ctx, oldEmail, req.NewEmail, now)

	// email 是登录名,改完踢全部 session 强制用新 email 重登。
	if err := s.sessionStore.DeleteAll(ctx, u.ID); err != nil {
		s.log.ErrorCtx(ctx, "change email 后 LogoutAll 失败", err, map[string]interface{}{"user_id": u.ID})
		return fmt.Errorf("logout all: %w: %w", err, user.ErrUserInternal)
	}

	s.log.InfoCtx(ctx, "邮箱已变更", map[string]interface{}{"user_id": u.ID, "new_email": req.NewEmail})
	return nil
}

// notifyEmailChanged 给旧邮箱发"你的邮箱被改了"安全告警。best-effort,发不动就算。
// 触发在 users.email 已落新值之后,即使此函数失败也不回滚 email 变更。
//
// per-old-email 日限:防止被盗 session 或脚本反复改 email 把同一受害邮箱打爆。
// ChangeEmail 入口已有 per-user 60s cooldown 兜底,这里是第二道防护,超限 skip 告警。
func (s *userService) notifyEmailChanged(ctx context.Context, oldEmail, newEmail string, when time.Time) {
	if s.emailSender == nil || s.emailCfg == nil || oldEmail == "" {
		return
	}
	if s.loginGuard != nil {
		key := fmt.Sprintf("%s:%s:%s", emailChangedNoticeDailyKeyPrefix, oldEmail, when.Format("2006-01-02"))
		count, cErr := s.loginGuard.TouchCounter(ctx, key, 24*time.Hour)
		if cErr == nil && count > emailChangedNoticeDailyCap {
			s.log.WarnCtx(ctx, "邮箱变更通知触发日限,本次跳过", map[string]interface{}{
				"old_email": oldEmail, "count": count, "cap": emailChangedNoticeDailyCap,
			})
			return
		}
	}
	whenStr := when.Format(emailChangedTimestampFormat)
	subject, body := email.BuildEmailChangedNotice(s.emailCfg.Locale, whenStr, oldEmail, newEmail)
	if sendErr := s.emailSender.SendVerificationEmail(ctx, oldEmail, subject, body); sendErr != nil {
		s.log.WarnCtx(ctx, "邮箱变更通知发送失败", map[string]interface{}{
			"old_email": oldEmail, "new_email": newEmail, "err": sendErr.Error(),
		})
		return
	}
	s.log.InfoCtx(ctx, "邮箱变更通知已发送到旧邮箱", map[string]interface{}{
		"old_email": oldEmail, "new_email": newEmail,
	})
}
