// oauth_login.go M1.6 第三方登录业务逻辑(当前支持 Google)。
//
// 外层 handler 已完成 OIDC 握手,拿到 verified subject + email + emailVerified,
// 本文件仅管:
//   1. 按 (provider, subject) 查 identity
//   2. 未命中但 emailVerified 且 users 表有同 email → 合并绑定,不允许未验邮箱合并
//   3. 全不中 → 建 user + identity(password_hash 留空,表示仅 OAuth 登录)
//   4. 拿到 user 后走和密码登录一样的 generateAuthResponse(device 级 session)
//
// 以及 exchange code 的 store / pickup(交 OAuthExchangeStore 实现)。
package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/user"
	"github.com/eyrihe999-stack/Synapse/internal/user/model"
	"gorm.io/gorm"
)

// OAuthProviderGoogle provider 常量,写死到 DB 用,不跟 enum pkg 绕弯。
const OAuthProviderGoogle = "google"

// OAuthLoginRequest handler 组装好的 IdP 校验结果 + device 信息。
//
// handler 在调本函数前必须:
//   - 已校验 state cookie 签名 + 比对 URL state
//   - 已凭 code 换取 id_token 并校验签名 + nonce
//   - 从 id_token claims 解出 Subject / Email / EmailVerified 等
type OAuthLoginRequest struct {
	Provider      string
	Subject       string
	Email         string
	EmailVerified bool
	DisplayName   string
	AvatarURL     string
	DeviceID      string
	DeviceName    string
	LoginIP       string
	UserAgent     string // handler 层注入,登录审计用
}

// LoginWithOAuth 完成身份合并 + 登录,返回 AuthResponse。详见文件头注释。
func (s *userService) LoginWithOAuth(ctx context.Context, req OAuthLoginRequest) (*AuthResponse, error) {
	if req.Provider == "" || req.Subject == "" {
		s.log.ErrorCtx(ctx, "oauth 登录 provider/subject 为空", nil, nil)
		return nil, fmt.Errorf("oauth login: empty provider/subject: %w", user.ErrUserInternal)
	}
	// IdP 返回的 email 走统一 normalize,避免大小写不一致导致 identity/user 无法按 email 合并
	req.Email = normalizeEmail(req.Email)
	req.DeviceName = truncDeviceName(req.DeviceName)
	if req.DeviceID == "" {
		req.DeviceID = "default"
	}

	// 1. 按 (provider, subject) 直接命中 identity —— 同人再次登录的正常路径
	identity, err := s.repo.FindIdentity(ctx, req.Provider, req.Subject)
	if err == nil {
		// identity 命中后要登录该 user:必须 active;
		// banned / deleted 的 user 就算 identity 有效也不放行。
		//sayso-lint:ignore err-shadow
		u, err := s.repo.FindActiveByID(ctx, identity.UserID) // 内层 err 仅在此 if 分支使用
		if err != nil {
			s.log.ErrorCtx(ctx, "identity 指向的 user 不存在或非 active", err, map[string]interface{}{
				"user_id": identity.UserID, "provider": req.Provider, "subject": req.Subject,
			})
			return nil, fmt.Errorf("find user by identity: %w: %w", err, user.ErrUserInternal)
		}
		//sayso-lint:ignore sentinel-wrap
		return s.finishOAuthLogin(ctx, u, req) // finishOAuthLogin → generateAuthResponse 已兜底 sentinel
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		s.log.ErrorCtx(ctx, "查询 identity 失败", err, map[string]interface{}{"provider": req.Provider})
		return nil, fmt.Errorf("find identity: %w: %w", err, user.ErrUserInternal)
	}

	// 2. identity 未命中 —— 尝试按 email 合并到既有账号。
	//    仅在 email_verified=true 时才做,未验邮箱合并 = 允许任何人注册同名 Google 账号接管。
	if req.Email != "" {
		// OAuth 合并只对 active 账号做:banned 的不允许通过第三方悄悄登回,
		// pending_verify 走新建分支(会撞 unique email → dup 分支兜底)。
		//sayso-lint:ignore err-shadow
		existing, err := s.repo.FindActiveByEmail(ctx, req.Email) // 内层 err 仅本 if 用
		if err == nil {
			if !req.EmailVerified {
				s.log.WarnCtx(ctx, "oauth email 未验证,拒绝自动合并", map[string]interface{}{
					"email": req.Email, "provider": req.Provider,
				})
				return nil, fmt.Errorf("unverified email: %w", user.ErrOAuthEmailUnverified)
			}
			// 绑新 identity 到既有 user
			if err := s.repo.CreateIdentity(ctx, &model.UserIdentity{
				UserID: existing.ID, Provider: req.Provider, Subject: req.Subject,
				Email: req.Email, EmailVerified: req.EmailVerified,
			}); err != nil {
				s.log.ErrorCtx(ctx, "合并 identity 到既有 user 失败", err, map[string]interface{}{
					"user_id": existing.ID, "provider": req.Provider,
				})
				return nil, fmt.Errorf("link identity: %w: %w", err, user.ErrUserInternal)
			}
			s.log.InfoCtx(ctx, "oauth 合并到既有账号", map[string]interface{}{
				"user_id": existing.ID, "provider": req.Provider, "email": req.Email,
			})
			//sayso-lint:ignore sentinel-wrap
			return s.finishOAuthLogin(ctx, existing, req) // generateAuthResponse 内兜底 sentinel
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			s.log.ErrorCtx(ctx, "查询 email 失败", err, map[string]interface{}{"email": req.Email})
			return nil, fmt.Errorf("find by email: %w: %w", err, user.ErrUserInternal)
		}
	}

	// 3. 全不中 —— 建新 user + identity。密码为空,后续走"忘记密码"才能设。
	displayName := req.DisplayName
	if displayName == "" {
		displayName = req.Email
	}
	if displayName == "" {
		displayName = fmt.Sprintf("%s user", req.Provider)
	}
	// M1.1 邮箱验证事实源:
	//   - IdP 返 email_verified=true → 直接 active + 落 EmailVerifiedAt
	//   - IdP 返 false              → pending_verify,登录成功后在函数末尾补发激活邮件
	initialStatus := model.StatusPendingVerify
	var verifiedAt *time.Time
	if req.EmailVerified {
		now := time.Now().UTC()
		initialStatus = model.StatusActive
		verifiedAt = &now
	}
	newUser := &model.User{
		Email:           req.Email,
		PasswordHash:    "", // OAuth-only 用户,本地无密码
		DisplayName:     displayName,
		AvatarURL:       req.AvatarURL,
		Status:          initialStatus,
		EmailVerifiedAt: verifiedAt,
	}
	if err := s.repo.CreateUser(ctx, newUser); err != nil {
		// email 空或重复都可能让 unique 索引撞;email 空视为内部错误(IdP 没返)
		if isDupEntryErr(err) {
			s.log.WarnCtx(ctx, "oauth 新建 user 撞邮箱唯一索引(并发)", map[string]interface{}{"email": req.Email})
			// 竞争场景:另一个请求刚好创了 user;重跑 FindByEmail 合并
			existing, findErr := s.repo.FindActiveByEmail(ctx, req.Email)
			if findErr != nil {
				s.log.ErrorCtx(ctx, "并发竞争兜底 FindByEmail 失败", findErr, nil)
				return nil, fmt.Errorf("race find: %w: %w", findErr, user.ErrUserInternal)
			}
			if err := s.repo.CreateIdentity(ctx, &model.UserIdentity{
				UserID: existing.ID, Provider: req.Provider, Subject: req.Subject,
				Email: req.Email, EmailVerified: req.EmailVerified,
			}); err != nil {
				s.log.ErrorCtx(ctx, "并发兜底创建 identity 失败", err, nil)
				return nil, fmt.Errorf("race create identity: %w: %w", err, user.ErrUserInternal)
			}
			//sayso-lint:ignore sentinel-wrap
			return s.finishOAuthLogin(ctx, existing, req) // generateAuthResponse 内兜底 sentinel
		}
		s.log.ErrorCtx(ctx, "oauth 建新 user 失败", err, map[string]interface{}{"email": req.Email})
		return nil, fmt.Errorf("create user: %w: %w", err, user.ErrUserInternal)
	}
	if err := s.repo.CreateIdentity(ctx, &model.UserIdentity{
		UserID: newUser.ID, Provider: req.Provider, Subject: req.Subject,
		Email: req.Email, EmailVerified: req.EmailVerified,
	}); err != nil {
		s.log.ErrorCtx(ctx, "oauth 新建 identity 失败", err, map[string]interface{}{"user_id": newUser.ID})
		return nil, fmt.Errorf("create identity: %w: %w", err, user.ErrUserInternal)
	}
	s.log.InfoCtx(ctx, "oauth 新建账号", map[string]interface{}{
		"user_id": newUser.ID, "provider": req.Provider, "email": req.Email, "verified": req.EmailVerified,
	})
	// OAuth 返回 email_verified=false 的账号落盘后立即发激活邮件。
	// 发送失败不影响登录本身成功(token 已签),下一次进入 CreateOrg/PublishAgent 时
	// 会被跨模块 guard 拦下,用户可以走 ResendEmailVerification 再发一次。
	if !req.EmailVerified {
		//sayso-lint:ignore err-swallow
		_ = s.sendEmailVerification(ctx, newUser) // best-effort
	}
	//sayso-lint:ignore sentinel-wrap
	return s.finishOAuthLogin(ctx, newUser, req) // generateAuthResponse 内兜底 sentinel
}

// finishOAuthLogin 复用密码登录成功后的动作:清 login_fail + 更新 last_login + 生成 tokens。
func (s *userService) finishOAuthLogin(ctx context.Context, u *model.User, req OAuthLoginRequest) (*AuthResponse, error) {
	if s.loginGuard != nil && u.Email != "" {
		//sayso-lint:ignore err-swallow
		_ = s.loginGuard.ResetLoginFail(ctx, u.Email) // best-effort,OAuth 登录也算"合法成功",顺手清零
	}
	now := time.Now().UTC()
	//sayso-lint:ignore err-swallow
	_ = s.repo.UpdateFields(ctx, u.ID, map[string]interface{}{"last_login_at": now}) // best-effort

	// 审计 + 新设备通知(与密码登录同口径,走 oauth_success 类型)
	s.recordLoginSuccess(ctx, u.ID, u.Email, req.DeviceID, req.LoginIP, req.UserAgent, model.LoginEventOAuthSuccess)

	//sayso-lint:ignore sentinel-wrap,log-coverage
	return s.generateAuthResponse(ctx, u, req.DeviceID, req.DeviceName, req.LoginIP) // 内部已打 log
}

// StoreOAuthExchange 生成一次性 code 存 Redis,返回给 handler 拼到前端回调 URL。
func (s *userService) StoreOAuthExchange(ctx context.Context, auth *AuthResponse) (string, error) {
	if s.oauthExchangeStore == nil {
		s.log.ErrorCtx(ctx, "oauth exchange store 未初始化", nil, nil)
		return "", fmt.Errorf("oauth exchange not initialized: %w", user.ErrUserInternal)
	}
	code, err := newOAuthExchangeCode()
	if err != nil {
		s.log.ErrorCtx(ctx, "生成 oauth exchange code 失败", err, nil)
		return "", fmt.Errorf("gen exchange code: %w: %w", err, user.ErrUserInternal)
	}
	if err := s.oauthExchangeStore.Store(ctx, code, auth); err != nil {
		return "", fmt.Errorf("store exchange: %w: %w", err, user.ErrUserInternal)
	}
	return code, nil
}

// PickupOAuthExchange 前端兑换 code,拿到 AuthResponse。失败返 ErrOAuthExchangeExpired,不区分
// "code 不存在" 和 "已被 pickup 过" —— 从攻击者角度两种情况都应当拒。
func (s *userService) PickupOAuthExchange(ctx context.Context, code string) (*AuthResponse, error) {
	if s.oauthExchangeStore == nil {
		s.log.ErrorCtx(ctx, "oauth exchange store 未初始化", nil, nil)
		return nil, fmt.Errorf("oauth exchange not initialized: %w", user.ErrUserInternal)
	}
	auth, err := s.oauthExchangeStore.Take(ctx, code)
	if err != nil || auth == nil {
		s.log.WarnCtx(ctx, "oauth exchange 不存在或已过期", nil)
		return nil, fmt.Errorf("exchange miss: %w", user.ErrOAuthExchangeExpired)
	}
	return auth, nil
}

// newOAuthExchangeCode 32 字节 crypto/rand base64url,同 reset token 风格。
// 调用方(StoreOAuthExchange)已做 ErrorCtx + sentinel 包装,本 helper 里仅原样抛。
func newOAuthExchangeCode() (string, error) {
	var b [32]byte
	//sayso-lint:ignore err-swallow
	if _, err := rand.Read(b[:]); err != nil { // 丢弃 n,仅看 err,由上层统一打 log
		//sayso-lint:ignore log-coverage
		return "", fmt.Errorf("rand: %w", err) // crypto/rand 失败极罕见,交上层打一次日志
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
