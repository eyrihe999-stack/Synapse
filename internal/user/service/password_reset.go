// password_reset.go M1.3 密码重置流程(request / confirm)。
//
//   POST /auth/password-reset/request  → 生成一次性 token 写 Redis,发邮件
//   POST /auth/password-reset/confirm  → 凭 token 改密,Del token,LogoutAll
//
// 安全原则:
//   - Request 不论邮箱是否存在都返 nil(handler 层统一返成功消息),防账户枚举
//   - Token 是 43 字符 base64url random,猜不到,不做 attempt 防爆破(防爆破对无法枚举的 token 是多余)
//   - Confirm 成功后立即 LogoutAll —— 新密码必须让所有设备重新登录(合规 / 被盗号场景)
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/user"
	"github.com/eyrihe999-stack/Synapse/internal/common/email"
	"github.com/eyrihe999-stack/Synapse/internal/common/verification"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// defaultPasswordResetTTL EmailConfig.PasswordResetTTL 解析失败或为空时使用。
const defaultPasswordResetTTL = 15 * time.Minute

// pwdResetCooldownKeyPrefix per-email 密码重置 cooldown Redis key 前缀 (P1)。
// 完整 key: synapse:pwd_reset_cd:{email};登记位置:internal/common/database/redis.go。
const pwdResetCooldownKeyPrefix = "synapse:pwd_reset_cd"

// pwdResetCooldown per-email 密码重置请求 cooldown,60s。
// 防止攻击者刷受害者邮箱(token 虽然 15min 轮换,邮件会被连续轰炸)。
const pwdResetCooldown = 60 * time.Second

// RequestPasswordResetRequest 请求体 —— 只需邮箱。
type RequestPasswordResetRequest struct {
	Email string `json:"email" binding:"required"`
}

// ConfirmPasswordResetRequest 提交新密码 + 一次性 token。
type ConfirmPasswordResetRequest struct {
	Token       string `json:"token" binding:"required"`
	NewPassword string `json:"new_password" binding:"required"`
}

// resetTTL 解析 cfg.Email.PasswordResetTTL,失败回退默认 15m。
func (s *userService) resetTTL() time.Duration {
	if s.emailCfg == nil || s.emailCfg.PasswordResetTTL == "" {
		return defaultPasswordResetTTL
	}
	d, err := time.ParseDuration(s.emailCfg.PasswordResetTTL)
	if err != nil || d <= 0 {
		return defaultPasswordResetTTL
	}
	return d
}

// RequestPasswordReset 生成一次性 token,存 Redis,发邮件。
//
// 邮箱不存在时仍然返 nil(由 handler 统一以"若存在将发邮件"口径返回),防账户枚举。
// 邮件发送失败不阻塞 —— token 已落 Redis,dev 环境从日志拿 link。
//
// 限流:per-email 60s cooldown + 复用 email_rl 日限(与 SendEmailCode / email_verify 共享配额)。
// 日限只对"真实存在的 email"扣减,避免攻击者拿陌生 email 污染他人配额。
// 返回 ErrRequestTooFrequent / ErrDailyLimitReached / ErrUserInternal 或 nil。
func (s *userService) RequestPasswordReset(ctx context.Context, req RequestPasswordResetRequest) error {
	if s.pwdResetStore == nil || s.emailCfg == nil {
		s.log.ErrorCtx(ctx, "password reset 模块未初始化", nil, nil)
		return fmt.Errorf("pwd reset not initialized: %w", user.ErrUserInternal)
	}
	req.Email = normalizeEmail(req.Email)

	// P1 per-email 60s cooldown,同 SendEmailCode 的口径。
	// 放在 FindActiveByEmail 之前 —— 防攻击者根据响应耗时差做账户枚举(cooldown 命中时,
	// 无论用户是否存在都走同一条 "too frequent" 路径,耗时一致)。
	if s.loginGuard != nil {
		key := fmt.Sprintf("%s:%s", pwdResetCooldownKeyPrefix, req.Email)
		count, cErr := s.loginGuard.TouchCounter(ctx, key, pwdResetCooldown)
		if cErr == nil && count > 1 {
			s.log.WarnCtx(ctx, "密码重置请求过于频繁", map[string]interface{}{"email": req.Email, "count": count})
			return fmt.Errorf("pwd reset too frequent: %w", user.ErrRequestTooFrequent)
		}
	}

	// 仅 active 用户可重置密码:pending_verify 先走邮箱激活,banned/deleted 不给续命。
	// 非 active 命中统一走 ErrRecordNotFound 分支静默返回,继续防枚举。
	u, err := s.repo.FindActiveByEmail(ctx, req.Email)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 邮箱不存在或非 active —— 静默返成功,防枚举
			s.log.InfoCtx(ctx, "password reset 请求:邮箱不存在或非 active,静默忽略", map[string]interface{}{"email": req.Email})
			return nil
		}
		s.log.ErrorCtx(ctx, "查询用户失败", err, map[string]interface{}{"email": req.Email})
		return fmt.Errorf("find user: %w: %w", err, user.ErrUserInternal)
	}

	// 日限:复用 SendEmailCode / email_verify 共享的 email_rl:{email}:{date} 池,
	// 一个邮箱每日所有通道合计不超过 DailyVerificationLimit。
	// 放在 FindActiveByEmail 之后 —— 不存在的邮箱已在上面静默返 nil,不会污染真实用户的配额。
	if s.codeStore != nil {
		count, cErr := s.codeStore.IncrDailyCount(ctx, u.Email)
		if cErr != nil {
			s.log.ErrorCtx(ctx, "密码重置:日限计数失败", cErr, map[string]interface{}{"email": u.Email})
			return fmt.Errorf("incr daily count: %w: %w", cErr, user.ErrUserInternal)
		}
		if count > int64(s.emailCfg.DailyVerificationLimit) {
			s.log.WarnCtx(ctx, "密码重置:超单日发送上限", map[string]interface{}{"email": u.Email, "count": count})
			return fmt.Errorf("daily limit reached: %w", user.ErrDailyLimitReached)
		}
	}

	token, err := verification.GenerateResetToken()
	if err != nil {
		s.log.ErrorCtx(ctx, "生成 reset token 失败", err, map[string]interface{}{"user_id": u.ID})
		return fmt.Errorf("gen token: %w: %w", err, user.ErrUserInternal)
	}

	ttl := s.resetTTL()
	entry := PasswordResetEntry{Email: u.Email, CreatedAt: time.Now().UTC()}
	if err := s.pwdResetStore.Store(ctx, token, entry, ttl); err != nil {
		return fmt.Errorf("store pwd reset: %w: %w", err, user.ErrUserInternal)
	}

	link := fmt.Sprintf("%s/reset-password?token=%s", s.emailCfg.PasswordResetLinkBase, token)
	// 日志只打 token 前缀 —— 完整 link 含一次性凭证,线上日志被读即可被重置他人密码。
	// dev / 调试需要完整 token 时,走 email 收件箱或 Redis key 查。
	//sayso-lint:ignore sensitive-log
	s.log.InfoCtx(ctx, "password reset token 已生成", map[string]interface{}{
		"user_id": u.ID, "email": u.Email, "token_prefix": safeTokenPrefix(token), // 已截断为前 6 字节
	})

	if s.emailSender != nil {
		subject, body := email.BuildPasswordResetEmail(s.emailCfg.Locale, link, int(ttl.Minutes()))
		if sendErr := s.emailSender.SendVerificationEmail(ctx, u.Email, subject, body); sendErr != nil {
			if errors.Is(sendErr, email.ErrProviderDisabled) {
				s.log.InfoCtx(ctx, "email provider 未启用,仅写 Redis", map[string]interface{}{"email": u.Email})
			} else {
				s.log.ErrorCtx(ctx, "重置邮件发送失败,token 已写 Redis", sendErr, map[string]interface{}{"email": u.Email})
			}
		}
	}
	return nil
}

// ConfirmPasswordReset 凭 token 改密。
//
// 流程:
//  1. Get(token) —— miss / err 统一映 ErrResetTokenInvalid(不区分过期 vs 无效,避免枚举)
//  2. 查出 email 对应用户(中间可能 email 改过,重新 FindByEmail)
//  3. 新密码 bcrypt hash → UpdateFields
//  4. Delete(token) 一次性消费
//  5. LogoutAll(user_id) 踢掉所有设备,强制所有已登陆 session 以新密码重登
//
// 返回 ErrResetTokenInvalid / ErrPasswordTooShort / ErrUserNotFound / ErrUserInternal。
func (s *userService) ConfirmPasswordReset(ctx context.Context, req ConfirmPasswordResetRequest) error {
	if s.pwdResetStore == nil {
		s.log.ErrorCtx(ctx, "password reset 模块未初始化", nil, nil)
		return fmt.Errorf("pwd reset not initialized: %w", user.ErrUserInternal)
	}
	if err := s.checkPassword(req.NewPassword); err != nil {
		s.log.WarnCtx(ctx, "新密码未通过策略校验", map[string]interface{}{"err": err.Error()})
		//sayso-lint:ignore sentinel-wrap
		return err
	}

	// token 是一次性的,不 normalize(store 里存的 email 是 Request 时的规范化版本)。
	// 这里 Get token 后拿到的 entry.Email 已是 normalized 形式,下面 FindActiveByEmail 就能命中。
	entry, err := s.pwdResetStore.Get(ctx, req.Token)
	if err != nil || entry == nil {
		s.log.WarnCtx(ctx, "reset token 不存在或已过期", nil)
		return fmt.Errorf("token invalid: %w", user.ErrResetTokenInvalid)
	}

	// 同 RequestPasswordReset:仅 active 用户可改密;banned/deleted 中途发生就让 token 失效。
	u, err := s.repo.FindActiveByEmail(ctx, entry.Email)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.log.WarnCtx(ctx, "reset token 对应用户已不存在或非 active", map[string]interface{}{"email": entry.Email})
			//sayso-lint:ignore err-swallow
			_ = s.pwdResetStore.Delete(ctx, req.Token) // 清理孤儿 token,best-effort
			return fmt.Errorf("user gone: %w", user.ErrUserNotFound)
		}
		s.log.ErrorCtx(ctx, "查询用户失败", err, map[string]interface{}{"email": entry.Email})
		return fmt.Errorf("find user: %w: %w", err, user.ErrUserInternal)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		s.log.ErrorCtx(ctx, "新密码哈希失败", err, map[string]interface{}{"user_id": u.ID})
		return fmt.Errorf("hash password: %w: %w", err, user.ErrUserInternal)
	}

	if err := s.repo.UpdateFields(ctx, u.ID, map[string]interface{}{"password_hash": string(hash)}); err != nil {
		s.log.ErrorCtx(ctx, "更新密码失败", err, map[string]interface{}{"user_id": u.ID})
		return fmt.Errorf("update password: %w: %w", err, user.ErrUserInternal)
	}

	// 一次性消费 —— 即便后续 LogoutAll 失败,token 也不能被复用
	//sayso-lint:ignore err-swallow
	_ = s.pwdResetStore.Delete(ctx, req.Token) // best-effort,TTL 兜底自销
	// 改密成功 → 清零失败计数(防止之前多次失败导致新密码也进不来)
	if s.loginGuard != nil {
		//sayso-lint:ignore err-swallow
		_ = s.loginGuard.ResetLoginFail(ctx, u.Email)
	}

	// 踢所有设备 —— 已有 access token 会在下次校验 session 时被拒
	if err := s.sessionStore.DeleteAll(ctx, u.ID); err != nil {
		s.log.ErrorCtx(ctx, "密码重置后 LogoutAll 失败", err, map[string]interface{}{"user_id": u.ID})
		return fmt.Errorf("logout all: %w: %w", err, user.ErrUserInternal)
	}

	s.log.InfoCtx(ctx, "密码重置成功", map[string]interface{}{"user_id": u.ID})
	return nil
}
