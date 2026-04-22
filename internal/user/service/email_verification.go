// email_verification.go M1.1 邮箱激活流程:发送激活邮件 / 验证 token / 重发 / 状态查询。
//
// 激活 token 与 M1.3 的密码重置 token 走同一套 TTL+Redis 模式(见 email_verify_store.go);
// 本文件只管业务编排,store 层抽象在 login_guard.go。
//
// 触发点:
//   - OAuth 新建用户且 IdP 返 email_verified=false:oauth_login.go 建完 user 后立即调
//   - 已登录用户手动重发:Handler → ResendEmailVerification
//
// 本地注册(走 6 位 email_code)和 OAuth email_verified=true 场景不经本文件 ——
// 注册那一步已经证明了邮箱所有权,service.go 里直接给 EmailVerifiedAt=now()。
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/user"
	"github.com/eyrihe999-stack/Synapse/internal/user/model"
	"github.com/eyrihe999-stack/Synapse/internal/common/email"
	"github.com/eyrihe999-stack/Synapse/internal/common/verification"
	"gorm.io/gorm"
)

// defaultEmailVerifyTTL cfg.Email.VerificationTTL 解析失败或为空时使用。
const defaultEmailVerifyTTL = 24 * time.Hour

// resendVerifyCooldownKeyPrefix per-user 重发激活邮件的 cooldown Redis key 前缀。
// 完整 key: synapse:resend_verify_cooldown:{user_id};TTL 即 cooldown 时间。
// 登记位置:internal/common/database/redis.go 的 Key Registry。
const resendVerifyCooldownKeyPrefix = "synapse:resend_verify_cooldown"

// resendVerifyCooldown per-user 重发激活邮件间隔,60s。
// 防止被盗 session 的攻击者猛调 resend 接口把 user 的 email 日限配额吃完,
// 导致真用户收不到登录/注册码。
const resendVerifyCooldown = 60 * time.Second

// safeTokenPrefix 日志脱敏:一次性 token(reset / email verify)不允许在日志里打完整值。
// 只保留前 6 字节用于排障关联,其余用 "..." 遮掉。
// 调用方需自行确保输入是 base64url / hex 字串(没短到 <6 字节)。
func safeTokenPrefix(token string) string {
	const keep = 6
	if len(token) <= keep {
		return "***"
	}
	return token[:keep] + "..."
}

// verifyTTL 解析 cfg.Email.VerificationTTL,失败回退默认 24h。
func (s *userService) verifyTTL() time.Duration {
	if s.emailCfg == nil || s.emailCfg.VerificationTTL == "" {
		return defaultEmailVerifyTTL
	}
	d, err := time.ParseDuration(s.emailCfg.VerificationTTL)
	if err != nil || d <= 0 {
		return defaultEmailVerifyTTL
	}
	return d
}

// sendEmailVerification 生成 token、写 Redis、发邮件。
//
// 设计要点:
//   - 复用 M1.1 邮箱日限计数器(synapse:email_rl:{email}:{date}),和 SendEmailCode 共享配额,
//     单一邮箱每日发邮件总数不会因为多个通道叠加而超;codeStore 没装时跳过限流。
//   - 邮件发送失败不回滚 token(token 已落 Redis,dev 环境从日志拿 link)。
//   - 返回 error 只在"写 Redis 失败 / token 生成失败"等内部错误上冒泡;邮件发送失败只 log。
//
// 调用方(OAuth pending_verify 分支 / ResendEmailVerification)应当在这之前
// 确认 user.Status 是可激活态 —— 本函数不做业务前置校验。
func (s *userService) sendEmailVerification(ctx context.Context, u *model.User) error {
	if s.emailVerifyStore == nil || s.emailCfg == nil {
		s.log.ErrorCtx(ctx, "email verify 模块未初始化", nil, map[string]interface{}{"user_id": u.ID})
		return fmt.Errorf("email verify not initialized: %w", user.ErrUserInternal)
	}
	if u.Email == "" {
		s.log.ErrorCtx(ctx, "email verify:user.email 为空,无法发送", nil, map[string]interface{}{"user_id": u.ID})
		return fmt.Errorf("empty email: %w", user.ErrUserInternal)
	}

	// 日限:复用 email_code 的 daily counter,防止一个邮箱被人刷无限激活邮件
	if s.codeStore != nil {
		//sayso-lint:ignore err-shadow
		count, err := s.codeStore.IncrDailyCount(ctx, u.Email) // 外层无 err,非真正 shadow
		if err != nil {
			s.log.ErrorCtx(ctx, "email verify:日限计数失败", err, map[string]interface{}{"email": u.Email})
			return fmt.Errorf("incr daily count: %w: %w", err, user.ErrUserInternal)
		}
		if count > int64(s.emailCfg.DailyVerificationLimit) {
			s.log.WarnCtx(ctx, "email verify:超单日发送上限", map[string]interface{}{"email": u.Email, "count": count})
			return fmt.Errorf("daily limit reached: %w", user.ErrDailyLimitReached)
		}
	}

	token, err := verification.GenerateResetToken()
	if err != nil {
		s.log.ErrorCtx(ctx, "email verify:生成 token 失败", err, map[string]interface{}{"user_id": u.ID})
		return fmt.Errorf("gen token: %w: %w", err, user.ErrUserInternal)
	}

	ttl := s.verifyTTL()
	entry := EmailVerifyEntry{UserID: u.ID, Email: u.Email, CreatedAt: time.Now().UTC()}
	if err := s.emailVerifyStore.Store(ctx, token, entry, ttl); err != nil {
		return fmt.Errorf("store email verify: %w: %w", err, user.ErrUserInternal)
	}

	// link base 空时仍写 token 到 Redis,dev 侧可通过 log 取 token 手工激活
	linkBase := s.emailCfg.VerificationLinkBase
	if linkBase == "" {
		linkBase = s.emailCfg.PasswordResetLinkBase // 二次兜底,单一 FE host 场景
	}
	link := fmt.Sprintf("%s/auth/email/verify?token=%s", linkBase, token)
	// 完整 link 只进邮件;日志里只留 token 前缀用于排障,避免日志读取权限放大为"任意账号激活权限"。
	//sayso-lint:ignore sensitive-log
	s.log.InfoCtx(ctx, "email verify token 已生成", map[string]interface{}{
		"user_id": u.ID, "email": u.Email, "token_prefix": safeTokenPrefix(token), // 已截断为前 6 字节
	})

	if s.emailSender != nil {
		subject, body := email.BuildEmailVerificationEmail(s.emailCfg.Locale, link, int(ttl.Minutes()))
		if sendErr := s.emailSender.SendVerificationEmail(ctx, u.Email, subject, body); sendErr != nil {
			if errors.Is(sendErr, email.ErrProviderDisabled) {
				s.log.InfoCtx(ctx, "email provider 未启用,仅写 Redis", map[string]interface{}{"email": u.Email})
			} else {
				s.log.ErrorCtx(ctx, "激活邮件发送失败,token 已写 Redis", sendErr, map[string]interface{}{"email": u.Email})
			}
		}
	}
	return nil
}

// VerifyEmail 凭一次性 token 激活邮箱。
//
// 流程:
//  1. Get(token) —— miss/err 统一映 ErrVerifyTokenInvalid(防枚举)
//  2. 按 entry.UserID 取 living user(pending_verify/active/banned);deleted 的直接失败
//  3. 已是 active + EmailVerifiedAt 非空 → 返 ErrEmailAlreadyVerified;同时清 token 防二次消费
//  4. 更新 status=active + email_verified_at=now()
//  5. Delete(token) 一次性
//
// 返回 ErrVerifyTokenInvalid / ErrEmailAlreadyVerified / ErrUserNotFound / ErrUserInternal。
func (s *userService) VerifyEmail(ctx context.Context, token string) error {
	if s.emailVerifyStore == nil {
		s.log.ErrorCtx(ctx, "email verify 模块未初始化", nil, nil)
		return fmt.Errorf("email verify not initialized: %w", user.ErrUserInternal)
	}
	if token == "" {
		return fmt.Errorf("empty token: %w", user.ErrVerifyTokenInvalid)
	}

	entry, err := s.emailVerifyStore.Get(ctx, token)
	if err != nil || entry == nil {
		s.log.WarnCtx(ctx, "email verify token 不存在或已过期", nil)
		return fmt.Errorf("token invalid: %w", user.ErrVerifyTokenInvalid)
	}

	u, err := s.repo.FindLivingByID(ctx, entry.UserID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.log.WarnCtx(ctx, "email verify token 对应用户已不存在或已 deleted", map[string]interface{}{"user_id": entry.UserID})
			//sayso-lint:ignore err-swallow
			_ = s.emailVerifyStore.Delete(ctx, token) // 清孤儿 token,best-effort
			return fmt.Errorf("user gone: %w", user.ErrUserNotFound)
		}
		s.log.ErrorCtx(ctx, "email verify:查询用户失败", err, map[string]interface{}{"user_id": entry.UserID})
		return fmt.Errorf("find user: %w: %w", err, user.ErrUserInternal)
	}

	if u.EmailVerifiedAt != nil && u.Status == model.StatusActive {
		s.log.InfoCtx(ctx, "email verify:已验证过,忽略本次激活", map[string]interface{}{"user_id": u.ID})
		//sayso-lint:ignore err-swallow
		_ = s.emailVerifyStore.Delete(ctx, token) // 清旧 token,防二次消费
		return fmt.Errorf("already verified: %w", user.ErrEmailAlreadyVerified)
	}

	now := time.Now().UTC()
	updates := map[string]any{
		"status":            model.StatusActive,
		"email_verified_at": now,
	}
	if err := s.repo.UpdateFields(ctx, u.ID, updates); err != nil {
		s.log.ErrorCtx(ctx, "email verify:落库失败", err, map[string]interface{}{"user_id": u.ID})
		return fmt.Errorf("update user: %w: %w", err, user.ErrUserInternal)
	}

	//sayso-lint:ignore err-swallow
	_ = s.emailVerifyStore.Delete(ctx, token) // best-effort,TTL 兜底
	s.log.InfoCtx(ctx, "邮箱已激活", map[string]interface{}{"user_id": u.ID, "email": u.Email})
	return nil
}

// ResendEmailVerification 已登录用户重发激活邮件。
//
// 前置:
//   - 用户 living(pending_verify/active/banned/deleted 中仅 pending_verify 有意义)
//   - 已 verified 返 ErrEmailAlreadyVerified,不白烧日限配额
//   - per-user cooldown:60s 内第二次调用直接拒,防被盗 session 刷爆受害者 email 日限
//
// 复用 sendEmailVerification 的日限(synapse:email_rl),攻击者刷不出来。
//
// 返回 ErrUserNotFound / ErrEmailAlreadyVerified / ErrResendTooFrequent /
//
//	ErrDailyLimitReached / ErrUserInternal。
func (s *userService) ResendEmailVerification(ctx context.Context, userID uint64) error {
	u, err := s.repo.FindLivingByID(ctx, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.log.WarnCtx(ctx, "resend verification:用户不存在", nil)
			return fmt.Errorf("find user: %w", user.ErrUserNotFound)
		}
		s.log.ErrorCtx(ctx, "resend verification:查询用户失败", err, nil)
		return fmt.Errorf("find user: %w: %w", err, user.ErrUserInternal)
	}
	if u.EmailVerifiedAt != nil && u.Status == model.StatusActive {
		return fmt.Errorf("already verified: %w", user.ErrEmailAlreadyVerified)
	}

	// per-user cooldown:TouchCounter 原子 INCR + EXPIRE IfNew。
	// 第一次(count==1)放过并种下 TTL,cooldown 内第二次 count==2 直接拒。
	// Redis 故障时 fail-open(loginGuard 内部已返 err 不拦阻业务主流程)。
	if s.loginGuard != nil {
		key := fmt.Sprintf("%s:%d", resendVerifyCooldownKeyPrefix, u.ID)
		count, cErr := s.loginGuard.TouchCounter(ctx, key, resendVerifyCooldown)
		if cErr == nil && count > 1 {
			s.log.WarnCtx(ctx, "resend verification:触发 cooldown", map[string]interface{}{"user_id": u.ID, "count": count})
			return fmt.Errorf("resend too frequent: %w", user.ErrResendTooFrequent)
		}
	}

	//sayso-lint:ignore sentinel-wrap
	return s.sendEmailVerification(ctx, u) // 内部已包装 sentinel
}

// IsUserVerified 跨模块前置 guard 用:判断 user 是否已完成邮箱验证。
//
// 口径:
//   - user 不存在 / deleted / banned / pending_verify → (false, nil)
//   - active 且 EmailVerifiedAt 非空 → (true, nil)
//   - 内部错误 → (false, err)
//
// 设计为不返 sentinel,让调用方(organization/agent service)根据需要包装自家错误。
func (s *userService) IsUserVerified(ctx context.Context, userID uint64) (bool, error) {
	u, err := s.repo.FindActiveByID(ctx, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		//sayso-lint:ignore log-coverage
		return false, fmt.Errorf("find active user: %w", err) // 调用方统一 log + 包装自家 sentinel
	}
	return u.EmailVerifiedAt != nil, nil
}
