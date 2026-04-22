// auth.go user 模块核心认证:注册、密码登录、OAuth 纯密码校验、access/refresh token 生成。
package service

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/user"
	"github.com/eyrihe999-stack/Synapse/internal/user/model"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// Register 注册新用户并返回认证凭证。
//
// 校验邮箱格式和密码长度后创建用户,返回 access/refresh token。
// 进入前先走 per-IP 滑动窗口限流,防注册轰炸(loginGuard 可用时生效)。
// 返回 ErrInvalidEmail / ErrPasswordTooShort / ErrEmailAlreadyRegistered /
//      ErrRegisterRateExceeded / ErrUserInternal。
func (s *userService) Register(ctx context.Context, req RegisterRequest) (*AuthResponse, error) {
	req.Email = normalizeEmail(req.Email)
	req.DeviceName = truncDeviceName(req.DeviceName)
	// per-IP 注册限流:每 window 内 > max 直接拒(ZSET 会把本次也计进去)
	if s.loginGuard != nil && s.registerRateMax() > 0 && req.LoginIP != "" {
		//sayso-lint:ignore err-shadow
		count, err := s.loginGuard.AddRegisterHit(ctx, req.LoginIP, s.registerRateWindow()) // 作用域仅在本 if 块内
		if err == nil && count > int64(s.registerRateMax()) {
			s.log.WarnCtx(ctx, "注册请求过于频繁", map[string]interface{}{
				"ip": req.LoginIP, "count": count, "max": s.registerRateMax(),
			})
			return nil, fmt.Errorf("register rate exceeded: %w", user.ErrRegisterRateExceeded)
		}
	}

	//sayso-lint:ignore err-swallow
	if _, err := mail.ParseAddress(req.Email); err != nil { // 丢弃解析结果,仅校验格式
		s.log.WarnCtx(ctx, "邮箱格式非法", map[string]interface{}{"email": req.Email})
		return nil, fmt.Errorf("invalid email: %w", user.ErrInvalidEmail)
	}
	if err := s.checkPassword(req.Password); err != nil {
		s.log.WarnCtx(ctx, "密码未通过策略校验", map[string]interface{}{"email": req.Email, "err": err.Error()})
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}

	// 检查邮箱是否已注册(先于消费验证码,避免用户误把登录当注册时白烧码)。
	// 用 FindLivingByEmail —— banned / pending_verify 账号也算占用,禁止抢注;
	// 仅已 pseudo 化的 deleted 账号会放行(email 已被改成 deleted+<uid>@synapse.invalid)。
	//sayso-lint:ignore err-swallow
	_, err := s.repo.FindLivingByEmail(ctx, req.Email) // 丢弃 user 记录,仅检查是否存在
	if err == nil {
		s.log.WarnCtx(ctx, "邮箱已注册", map[string]interface{}{"email": req.Email})
		return nil, fmt.Errorf("email taken: %w", user.ErrEmailAlreadyRegistered)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		s.log.ErrorCtx(ctx, "查询邮箱失败", err, map[string]interface{}{"email": req.Email})
		return nil, fmt.Errorf("check email: %w: %w", err, user.ErrUserInternal)
	}

	// 消费邮箱验证码 —— 失败即返回,不写任何库
	if err := s.consumeEmailCode(ctx, req.Email, req.Code); err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		s.log.ErrorCtx(ctx, "密码哈希失败", err, nil)
		return nil, fmt.Errorf("hash password: %w: %w", err, user.ErrUserInternal)
	}

	displayName := req.DisplayName
	if displayName == "" {
		displayName = req.Email
	}

	// 本地注册必须消费 email_code(上面已经做过 consumeEmailCode),
	// 这一步等价于已证明邮箱所有权 → 直接写 EmailVerifiedAt,状态 active。
	// 不再走 pending_verify 流程,否则用户还要再点一次激活链接,体验冗余。
	verifiedAt := time.Now().UTC()
	u := &model.User{
		Email:           req.Email,
		PasswordHash:    string(hash),
		DisplayName:     displayName,
		Status:          model.StatusActive,
		EmailVerifiedAt: &verifiedAt,
	}

	if err := s.repo.CreateUser(ctx, u); err != nil {
		// 兜底:FindByEmail 与 CreateUser 之间有 TOCTOU 窗口,
		// 并发注册同邮箱时这里会撞 unique 索引,需要把 sentinel 映射回去。
		if isDupEntryErr(err) {
			s.log.WarnCtx(ctx, "邮箱已注册(并发竞争)", map[string]interface{}{"email": req.Email})
			return nil, fmt.Errorf("email taken: %w", user.ErrEmailAlreadyRegistered)
		}
		s.log.ErrorCtx(ctx, "创建用户失败", err, map[string]interface{}{"email": req.Email})
		return nil, fmt.Errorf("create user: %w: %w", err, user.ErrUserInternal)
	}

	deviceID := req.DeviceID
	if deviceID == "" {
		deviceID = "default"
	}

	// 审计:注册事件。不走"新设备"通知 —— 账号刚建出来根本没有"旧设备"概念。
	s.recordRegister(ctx, u.ID, u.Email, deviceID, req.LoginIP, req.UserAgent)

	//sayso-lint:ignore sentinel-wrap
	return s.generateAuthResponse(ctx, u, deviceID, req.DeviceName, req.LoginIP)
}

// Login 用户登录,校验邮箱密码后返回认证凭证。
//
// 反爆破链路(loginGuard 可用时生效):
//  1. 进入即先查 login_fail 计数,≥ max 直接返 ErrAccountLocked(不消耗 bcrypt / 验证码预算)
//  2. 密码错 / 验证码错 → IncrLoginFail(首次失败 ⇒ 设 lockTTL)
//  3. 全部校验通过 → ResetLoginFail(成功登录清零)
//
// 返回 ErrInvalidCredentials / ErrAccountLocked / ErrUserInternal。
func (s *userService) Login(ctx context.Context, req LoginRequest) (*AuthResponse, error) {
	req.Email = normalizeEmail(req.Email)
	req.DeviceName = truncDeviceName(req.DeviceName)
	deviceID := req.DeviceID
	if deviceID == "" {
		deviceID = "default"
	}

	// P3 per-IP 登录失败限流:跨 email spray 的兜底防御。per-email 限流只挡单一 email 爆破,
	// 分散到多个 email 的 spray 需要这层拦住。和 per-email 同 TTL(15min 自解锁)。
	if s.isLoginIPRateLimited(ctx, req.LoginIP) {
		s.log.WarnCtx(ctx, "per-IP 登录失败次数超限,拒绝", map[string]interface{}{"ip": req.LoginIP})
		return nil, fmt.Errorf("login ip rate limited: %w", user.ErrLoginIPRateLimited)
	}

	// 先看是否已锁 —— 攻击者拿错密码连续轰炸时,到阈值直接省掉 bcrypt 计算
	if s.loginGuard != nil && s.loginFailMax() > 0 {
		//sayso-lint:ignore err-shadow
		count, err := s.loginGuard.GetLoginFail(ctx, req.Email) // 作用域仅在本 if 块内
		if err == nil && count >= int64(s.loginFailMax()) {
			s.log.WarnCtx(ctx, "账号已锁,拒绝登录", map[string]interface{}{"email": req.Email, "count": count})
			s.recordAccountLocked(ctx, req.Email, deviceID, req.LoginIP, req.UserAgent)
			return nil, fmt.Errorf("account locked: %w", user.ErrAccountLocked)
		}
	}

	u, err := s.repo.FindActiveByEmail(ctx, req.Email)
	if err != nil {
		s.log.WarnCtx(ctx, "登录用户不存在或非 active", map[string]interface{}{"email": req.Email})
		s.incrLoginFailIfEnabled(ctx, req.Email)
		s.incrLoginIPFail(ctx, req.LoginIP)
		s.recordLoginFailure(ctx, 0, req.Email, deviceID, req.LoginIP, req.UserAgent, "user_not_found")
		return nil, fmt.Errorf("find user: %w", user.ErrInvalidCredentials)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)); err != nil {
		s.log.WarnCtx(ctx, "密码不匹配", map[string]interface{}{"email": req.Email})
		s.incrLoginFailIfEnabled(ctx, req.Email)
		s.incrLoginIPFail(ctx, req.LoginIP)
		s.recordLoginFailure(ctx, u.ID, req.Email, deviceID, req.LoginIP, req.UserAgent, "password_mismatch")
		return nil, fmt.Errorf("password mismatch: %w", user.ErrInvalidCredentials)
	}

	// 密码过了再消费验证码 —— 拒绝攻击者用错误密码烧用户的 attempt 预算
	if err := s.consumeEmailCode(ctx, req.Email, req.Code); err != nil {
		s.incrLoginFailIfEnabled(ctx, req.Email)
		s.incrLoginIPFail(ctx, req.LoginIP)
		s.recordLoginFailure(ctx, u.ID, req.Email, deviceID, req.LoginIP, req.UserAgent, "code_invalid")
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}

	// 全部校验通过 —— 清零失败计数
	if s.loginGuard != nil {
		//sayso-lint:ignore err-swallow
		_ = s.loginGuard.ResetLoginFail(ctx, req.Email) // best-effort,失败也让用户正常登录
	}

	now := time.Now().UTC()
	//sayso-lint:ignore err-swallow
	_ = s.repo.UpdateFields(ctx, u.ID, map[string]interface{}{"last_login_at": now}) // best-effort 更新登录时间,失败不阻塞

	// 审计 + 新设备通知。必须在 generateAuthResponse 之前做 —— 那里 Save 到 SessionStore 后,
	// 会出现(user_id, device_id)的 Redis key;但审计表的历史是 CreateLoginEvent 之前才查的,
	// 所以顺序上没冲突,两个独立数据面。
	s.recordLoginSuccess(ctx, u.ID, u.Email, deviceID, req.LoginIP, req.UserAgent, model.LoginEventPasswordSuccess)

	//sayso-lint:ignore sentinel-wrap,log-coverage
	return s.generateAuthResponse(ctx, u, deviceID, req.DeviceName, req.LoginIP) // generateAuthResponse 内部已对每个 error 路径打 ErrorCtx
}

// VerifyPasswordOnly 纯密码校验 + 反爆破 + 审计,无 code、无 session、无 JWT —— 供 Synapse 自带 OAuth AS
// 的 /oauth/login 表单使用。**所有反爆破计数器 / 审计写入和 Login() 共享**,攻击者不能用 /oauth/login
// 绕过 M1.4 的爆破防线。
//
// 流程与 Login 完全对齐,差别:
//   - 不要 req.Code(OAuth 登录页没地方填验证码)
//   - 成功后直接返 userID,不签 session / 不建 device,后续授权流程由 flowCookie 接管
func (s *userService) VerifyPasswordOnly(ctx context.Context, emailAddr, password, ip, userAgent string) (uint64, error) {
	emailAddr = normalizeEmail(emailAddr)

	// per-IP spray 防御:入口前置,拒 IP 层整体爆破。
	if s.isLoginIPRateLimited(ctx, ip) {
		s.log.WarnCtx(ctx, "oauth login: per-IP 失败上限,拒绝", map[string]interface{}{"ip": ip})
		return 0, fmt.Errorf("login ip rate limited: %w", user.ErrLoginIPRateLimited)
	}

	// per-email 锁:到阈值直接拒,省 bcrypt 开销 + 不 flood 审计。
	if s.loginGuard != nil && s.loginFailMax() > 0 {
		//sayso-lint:ignore err-shadow
		count, err := s.loginGuard.GetLoginFail(ctx, emailAddr)
		if err == nil && count >= int64(s.loginFailMax()) {
			s.log.WarnCtx(ctx, "oauth login: 账号已锁,拒绝", map[string]interface{}{"email": emailAddr, "count": count})
			s.recordAccountLocked(ctx, emailAddr, "", ip, userAgent)
			return 0, fmt.Errorf("account locked: %w", user.ErrAccountLocked)
		}
	}

	u, err := s.repo.FindActiveByEmail(ctx, emailAddr)
	if err != nil {
		s.log.WarnCtx(ctx, "oauth login: 用户不存在或非 active", map[string]interface{}{"email": emailAddr})
		s.incrLoginFailIfEnabled(ctx, emailAddr)
		s.incrLoginIPFail(ctx, ip)
		s.recordLoginFailure(ctx, 0, emailAddr, "", ip, userAgent, "user_not_found")
		return 0, fmt.Errorf("find user: %w", user.ErrInvalidCredentials)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		s.log.WarnCtx(ctx, "oauth login: 密码不匹配", map[string]interface{}{"email": emailAddr})
		s.incrLoginFailIfEnabled(ctx, emailAddr)
		s.incrLoginIPFail(ctx, ip)
		s.recordLoginFailure(ctx, u.ID, emailAddr, "", ip, userAgent, "password_mismatch")
		return 0, fmt.Errorf("password mismatch: %w", user.ErrInvalidCredentials)
	}

	if s.loginGuard != nil {
		//sayso-lint:ignore err-swallow
		_ = s.loginGuard.ResetLoginFail(ctx, emailAddr) // best-effort
	}

	//sayso-lint:ignore err-swallow
	_ = s.repo.UpdateFields(ctx, u.ID, map[string]interface{}{"last_login_at": time.Now().UTC()}) // best-effort

	s.recordLoginSuccess(ctx, u.ID, u.Email, "", ip, userAgent, model.LoginEventPasswordSuccess)
	return u.ID, nil
}

// incrLoginFailIfEnabled 走 loginGuard +1,guard 为 nil 或 max=0 时 no-op。
// best-effort:Redis 挂了就算了,本次仍按失败返回,不把限流故障放大成登录故障。
func (s *userService) incrLoginFailIfEnabled(ctx context.Context, email string) {
	if s.loginGuard == nil || s.loginFailMax() <= 0 {
		return
	}
	//sayso-lint:ignore err-swallow
	_, _ = s.loginGuard.IncrLoginFail(ctx, email, s.loginLockTTL()) // best-effort
}

// generateAuthResponse 生成 access/refresh token 对,保存 session 到 Redis,并组装响应。
func (s *userService) generateAuthResponse(ctx context.Context, u *model.User, deviceID, deviceName, loginIP string) (*AuthResponse, error) {
	//sayso-lint:ignore err-swallow
	accessToken, _, err := s.jwtManager.GenerateAccessToken(u.ID, u.Email, deviceID)
	if err != nil {
		s.log.ErrorCtx(ctx, "生成 access token 失败", err, map[string]interface{}{"user_id": u.ID})
		return nil, fmt.Errorf("generate access token: %w: %w", err, user.ErrUserInternal)
	}

	//sayso-lint:ignore err-swallow
	refreshToken, _, err := s.jwtManager.GenerateRefreshToken(u.ID, u.Email, deviceID)
	if err != nil {
		s.log.ErrorCtx(ctx, "生成 refresh token 失败", err, map[string]interface{}{"user_id": u.ID})
		return nil, fmt.Errorf("generate refresh token: %w: %w", err, user.ErrUserInternal)
	}

	// 解析 refresh token 取 JTI
	refreshClaims, err := s.jwtManager.ValidateRefreshToken(refreshToken)
	if err != nil {
		s.log.ErrorCtx(ctx, "解析 refresh token JTI 失败", err, map[string]interface{}{"user_id": u.ID})
		return nil, fmt.Errorf("parse refresh jti: %w: %w", err, user.ErrUserInternal)
	}

	// 先取旧 session:一用于设备上限检查(新设备才算),二用于 SessionStartAt 保留(refresh 不重置)
	//sayso-lint:ignore err-swallow
	existing, _ := s.sessionStore.Get(ctx, u.ID, deviceID) // 丢弃 error,Get 失败视为新设备
	if s.maxSessionsPerUser > 0 && existing == nil {
		//sayso-lint:ignore err-shadow
		sessions, err := s.sessionStore.List(ctx, u.ID)
		if err == nil && len(sessions) >= s.maxSessionsPerUser {
			s.log.WarnCtx(ctx, "设备数量已达上限", map[string]interface{}{
				"user_id": u.ID, "limit": s.maxSessionsPerUser, "current": len(sessions),
			})
			return nil, fmt.Errorf("session limit: %w", user.ErrSessionLimitReached)
		}
	}

	// 保存 session 到 Redis。SessionStartAt 只在首次创建时写入,refresh 路径保留旧值 ——
	// 这样绝对 TTL 从首次 Login 起算,不会因持续 refresh 被无限延长。
	now := time.Now().UTC().Unix()
	sessionStartAt := now
	if existing != nil && existing.SessionStartAt > 0 {
		sessionStartAt = existing.SessionStartAt
	}
	sessionInfo := user.SessionInfo{
		JTI:            refreshClaims.ID,
		DeviceName:     deviceName,
		LoginIP:        loginIP,
		LoginAt:        now,
		SessionStartAt: sessionStartAt,
	}
	if err := s.sessionStore.Save(ctx, u.ID, deviceID, sessionInfo, s.jwtManager.RefreshTokenDuration()); err != nil {
		s.log.ErrorCtx(ctx, "保存 session 失败", err, map[string]interface{}{"user_id": u.ID, "device_id": deviceID})
		return nil, fmt.Errorf("save session: %w: %w", err, user.ErrUserInternal)
	}

	return &AuthResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    s.jwtManager.GetAccessTokenDuration(),
		User:         *toUserProfile(u),
	}, nil
}
