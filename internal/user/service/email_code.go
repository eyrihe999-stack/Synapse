// email_code.go 邮箱验证码的发送 / 消费业务逻辑。
//
// 两条链路(都与 sayso-server 的 V2 auth 对齐):
//
//   SendEmailCode:
//     邮箱格式 → 日限计数 → 生成 6 位码 → 写 Redis(覆盖旧码) → 发邮件(失败不阻塞)
//
//   consumeEmailCode(内部):
//     取 Redis 条目 → 比对 → 成功一次性清理;失败递增 attempt,
//     attempt ≥ max 时删码 + 删 attempt(强制用户重新发送,共用日限预算)
//
//   被以下路径调用:
//     - Register(注册流:email 未占用检查通过后再消费码)
//     - Login   (登录流:密码校验通过后再消费码)
package service

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/user"
	"github.com/eyrihe999-stack/Synapse/internal/common/email"
	"github.com/eyrihe999-stack/Synapse/internal/common/verification"
)

// defaultCodeTTL 当 EmailConfig.CodeTTL 解析失败或为空时使用。
const defaultCodeTTL = 10 * time.Minute

// sendCodeCooldownKeyPrefix per-email 发码 cooldown 的 Redis key 前缀 (P1)。
// 完整 key: synapse:send_code_cd:{email};TTL 即 cooldown 时长。
// 登记位置:internal/common/database/redis.go 的 Key Registry。
const sendCodeCooldownKeyPrefix = "synapse:send_code_cd"

// sendCodeCooldown per-email 发码 cooldown,60s。防攻击者刷受害者邮箱 daily quota 拒服务。
const sendCodeCooldown = 60 * time.Second

func (s *userService) codeTTL() time.Duration {
	if s.emailCfg == nil || s.emailCfg.CodeTTL == "" {
		return defaultCodeTTL
	}
	d, err := time.ParseDuration(s.emailCfg.CodeTTL)
	if err != nil || d <= 0 {
		return defaultCodeTTL
	}
	return d
}

// SendEmailCode 发送邮箱验证码。
//
// 返回 ErrInvalidEmail / ErrDailyLimitReached / ErrUserInternal。
// 邮件发送失败(provider 挂掉/配置不全)**不会**让整个请求失败 —— 码已落 Redis,
// 开发环境可通过 log 拿到,生产侧可选是否把发送失败上报告警。
func (s *userService) SendEmailCode(ctx context.Context, req SendEmailCodeRequest) (*SendEmailCodeResponse, error) {
	if s.codeStore == nil || s.emailCfg == nil {
		s.log.ErrorCtx(ctx, "email code 模块未初始化", nil, nil)
		return nil, fmt.Errorf("email code not initialized: %w", user.ErrUserInternal)
	}
	req.Email = normalizeEmail(req.Email)
	//sayso-lint:ignore err-swallow
	if _, err := mail.ParseAddress(req.Email); err != nil { // 丢弃 *Address,只看 err
		s.log.WarnCtx(ctx, "邮箱格式非法", map[string]interface{}{"email": req.Email})
		return nil, fmt.Errorf("invalid email: %w", user.ErrInvalidEmail)
	}

	// P1 per-email 60s cooldown:防止攻击者刷受害者邮箱的 daily quota。
	// TouchCounter 首次 = 1 放过,后续同 email 在 TTL 内 count > 1 直接拒;
	// Redis 故障走 fail-open(宁发错也别挡用户)。
	if s.loginGuard != nil {
		key := fmt.Sprintf("%s:%s", sendCodeCooldownKeyPrefix, req.Email)
		count, cErr := s.loginGuard.TouchCounter(ctx, key, sendCodeCooldown)
		if cErr == nil && count > 1 {
			s.log.WarnCtx(ctx, "SendEmailCode 触发 cooldown", map[string]interface{}{"email": req.Email, "count": count})
			return nil, fmt.Errorf("send code too frequent: %w", user.ErrRequestTooFrequent)
		}
	}

	// 日限:INCR 已经 +1,超限也让计数累积,防止攻击者多次触发"白嫖"。
	count, err := s.codeStore.IncrDailyCount(ctx, req.Email)
	if err != nil {
		return nil, fmt.Errorf("incr daily count: %w: %w", err, user.ErrUserInternal)
	}
	if count > int64(s.emailCfg.DailyVerificationLimit) {
		s.log.WarnCtx(ctx, "邮箱超过单日发送上限", map[string]interface{}{"email": req.Email, "count": count})
		return nil, fmt.Errorf("daily limit reached: %w", user.ErrDailyLimitReached)
	}

	code, err := verification.GenerateVerificationCode()
	if err != nil {
		s.log.ErrorCtx(ctx, "生成验证码失败", err, map[string]interface{}{"email": req.Email})
		return nil, fmt.Errorf("gen code: %w: %w", err, user.ErrUserInternal)
	}

	ttl := s.codeTTL()
	entry := EmailCodeEntry{Code: code, IP: req.LoginIP, CreatedAt: time.Now().UTC()}
	if err := s.codeStore.Store(ctx, req.Email, entry, ttl); err != nil {
		return nil, fmt.Errorf("store code: %w: %w", err, user.ErrUserInternal)
	}

	if s.emailSender != nil {
		subject, body := email.BuildVerificationEmail(s.emailCfg.Locale, code, int(ttl.Minutes()))
		if sendErr := s.emailSender.SendVerificationEmail(ctx, req.Email, subject, body); sendErr != nil {
			if errors.Is(sendErr, email.ErrProviderDisabled) {
				s.log.InfoCtx(ctx, "email provider 未启用,仅写 Redis", map[string]interface{}{"email": req.Email})
			} else {
				s.log.ErrorCtx(ctx, "邮件发送失败,码已写 Redis", sendErr, map[string]interface{}{"email": req.Email})
			}
		}
	}

	s.log.InfoCtx(ctx, "邮箱验证码已生成", map[string]interface{}{"email": req.Email, "daily_count": count})
	return &SendEmailCodeResponse{
		Email:     req.Email,
		ExpiresIn: int(ttl.Seconds()),
		SentAt:    time.Now().UTC(),
	}, nil
}

// consumeEmailCode 校验并消费(删除)邮箱验证码。
//
// 返回的错误都是 user 模块的 sentinel:
//   - ErrEmailCodeNotFound:码不存在或已过期(用户需要重新发码)
//   - ErrInvalidEmailCode :码不匹配;attempt 命中上限时额外删除码,强制下次走发送
//   - ErrUserInternal     :模块未初始化等致命错误
//
// 成功时一次性清理 code 和 attempt 计数器。
func (s *userService) consumeEmailCode(ctx context.Context, emailAddr, code string) error {
	if s.codeStore == nil || s.emailCfg == nil {
		s.log.ErrorCtx(ctx, "email code 模块未初始化", nil, nil)
		return fmt.Errorf("email code not initialized: %w", user.ErrUserInternal)
	}

	entry, err := s.codeStore.Get(ctx, emailAddr)
	if err != nil || entry == nil {
		s.log.WarnCtx(ctx, "验证码不存在或已过期", map[string]interface{}{"email": emailAddr})
		return fmt.Errorf("code not found: %w", user.ErrEmailCodeNotFound)
	}

	if entry.Code != code {
		maxAttempts := s.emailCfg.MaxAttempts
		count, incrErr := s.codeStore.IncrAttempt(ctx, emailAddr, s.codeTTL())
		if incrErr == nil && maxAttempts > 0 && count >= int64(maxAttempts) {
			// 上限命中:码作废,attempt 归零,下一次必须重新发送(日限在那儿等着)
			//sayso-lint:ignore err-swallow
			_ = s.codeStore.Delete(ctx, emailAddr) // 作废路径 best-effort,失败不影响返回
			//sayso-lint:ignore err-swallow
			_ = s.codeStore.DeleteAttempt(ctx, emailAddr) // 同上
			s.log.WarnCtx(ctx, "验证码连续错误达上限,码已作废", map[string]interface{}{"email": emailAddr, "count": count})
		}
		return fmt.Errorf("code mismatch: %w", user.ErrInvalidEmailCode)
	}

	// 验证成功:一次性清理
	//sayso-lint:ignore err-swallow
	_ = s.codeStore.Delete(ctx, emailAddr) // 已验过,清理失败下一次 TTL 到也会过期
	//sayso-lint:ignore err-swallow
	_ = s.codeStore.DeleteAttempt(ctx, emailAddr) // 同上
	return nil
}
