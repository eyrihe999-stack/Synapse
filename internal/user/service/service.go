// service.go user 模块服务契约与核心装配:interface、userService 结构、
// 构造函数、密码策略与反爆破 / 注册限流参数。具体业务(注册登录 / 资料 /
// session 管理 / OAuth / 账号安全 / 邮箱激活 等)按职责拆到同包其它文件。
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/config"
	"github.com/eyrihe999-stack/Synapse/internal/user"
	"github.com/eyrihe999-stack/Synapse/internal/user/repository"
	"github.com/eyrihe999-stack/Synapse/internal/common/email"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/common/pwdpolicy"
	"github.com/eyrihe999-stack/Synapse/internal/common/jwt"
)

// UserService 定义用户模块的业务操作接口。
type UserService interface {
	Register(ctx context.Context, req RegisterRequest) (*AuthResponse, error)
	Login(ctx context.Context, req LoginRequest) (*AuthResponse, error)
	GetProfile(ctx context.Context, userID uint64) (*UserProfile, error)
	UpdateProfile(ctx context.Context, userID uint64, req UpdateProfileRequest) (*UserProfile, error)
	RefreshToken(ctx context.Context, req RefreshRequest) (*AuthResponse, error)
	ListSessions(ctx context.Context, userID uint64) ([]user.SessionEntry, error)
	KickSession(ctx context.Context, userID uint64, deviceID string) error
	LogoutAll(ctx context.Context, userID uint64) error
	SendEmailCode(ctx context.Context, req SendEmailCodeRequest) (*SendEmailCodeResponse, error)
	RequestPasswordReset(ctx context.Context, req RequestPasswordResetRequest) error
	ConfirmPasswordReset(ctx context.Context, req ConfirmPasswordResetRequest) error

	// ── 生命周期 (M1.7) ───────────────────────────────────────────────────────
	// DeleteAccount 自助注销当前账号:pseudo 化 PII + 删第三方绑定 + 踢全部 session。
	// 实现见 account_lifecycle.go。
	// GDPR 物理抹除(硬删 users 行 + 跨模块级联清理)暂不实现,等系统成熟统一规划。
	DeleteAccount(ctx context.Context, userID uint64, req DeleteAccountRequest) error
	// ExpireStalePendingVerifyAccounts 清理长期未激活的 pending_verify 账号(走 pseudo 化释放 email 占位)。
	// 由 cmd/synapse-cleanup CLI 调用;web 进程不挂。
	ExpireStalePendingVerifyAccounts(ctx context.Context, staleDuration time.Duration, batchLimit int) (ExpireStats, error)

	// ── 邮箱激活 (M1.1) ───────────────────────────────────────────────────────
	// VerifyEmail 凭一次性 token 激活邮箱,成功后 status → active 且 email_verified_at 落当前时间。
	VerifyEmail(ctx context.Context, token string) error
	// ResendEmailVerification 已登录用户重发激活邮件;已验证返 ErrEmailAlreadyVerified。
	ResendEmailVerification(ctx context.Context, userID uint64) error
	// IsUserVerified 跨模块前置 guard(CreateOrg / PublishAgent 等)用的口径判断。
	// 非 active / 未验证均返 (false, nil);内部错误走第二个返回值。
	IsUserVerified(ctx context.Context, userID uint64) (bool, error)

	// ── 账号安全自助(已登录) ────────────────────────────────────────────────
	// ChangePassword 已登录用户改密;有本地密码的账号必须带旧密码二次确认,成功后 LogoutAll。
	ChangePassword(ctx context.Context, userID uint64, req ChangePasswordRequest) error
	// ChangeEmail 已登录用户改邮箱;通过消费发到新邮箱的 6 位 code 证明所有权,成功后 LogoutAll。
	ChangeEmail(ctx context.Context, userID uint64, req ChangeEmailRequest) error

	// ── OAuth login (M1.6) ────────────────────────────────────────────────────
	// LoginWithOAuth 根据 IdP callback 解出的 (provider, subject, email, ...) 完成登录:
	// 命中 identity → 登录对应 user;未命中但 email_verified + users.email 命中 → 自动合并;
	// 全不中 → 建新 user + identity。
	LoginWithOAuth(ctx context.Context, req OAuthLoginRequest) (*AuthResponse, error)
	// StoreOAuthExchange 把 AuthResponse 以一次性 exchange code 存 Redis(60s),
	// 返回 code 供 handler 302 到前端。前端再调 PickupOAuthExchange 兑换。
	StoreOAuthExchange(ctx context.Context, auth *AuthResponse) (string, error)
	// PickupOAuthExchange GET+DEL exchange code,返回先前存的 AuthResponse。
	// 不存在 / 过期返 ErrOAuthExchangeExpired,防止重放。
	PickupOAuthExchange(ctx context.Context, code string) (*AuthResponse, error)

	// VerifyPasswordOnly 纯密码校验 + 反爆破 + 审计,不消费 email code、不签 session / JWT。
	// 专供 Synapse 自带 OAuth AS 的 /oauth/login 表单用,让 OAuth 登录和 web 登录共享同一套
	// per-email 锁定 + per-IP spray 防御,避免 /oauth/login 成为绕过 M1.4 的爆破通道。
	// 失败返 ErrInvalidCredentials / ErrAccountLocked / ErrLoginIPRateLimited。
	VerifyPasswordOnly(ctx context.Context, email, password, ip, userAgent string) (userID uint64, err error)

	// SetOwnerChecker 晚绑定注入 M3.7 owner 孤儿态 guard。
	// main.go 构造完 org service 后调用一次;nil 表示关掉该 guard。
	SetOwnerChecker(checker OwnerChecker)
}

type userService struct {
	repo               repository.Repository
	jwtManager         *jwt.JWTManager
	sessionStore       user.SessionStore
	maxSessionsPerUser int
	log                logger.LoggerInterface
	codeStore          EmailCodeStore
	emailSender        email.Sender
	emailCfg           *config.EmailConfig
	loginGuard         LoginGuard
	userCfg            *config.UserConfig
	pwdResetStore      PasswordResetStore
	pwdPolicy          *pwdpolicy.Policy
	oauthExchangeStore OAuthExchangeStore
	emailVerifyStore   EmailVerifyStore // M1.1 邮箱激活 token 存取;nil 时激活接口返 ErrUserInternal
	ownerChecker       OwnerChecker     // M3.7 注销前 org owner 孤儿态 guard;nil 时跳过检查
}

// OwnerChecker M3.7 跨模块 guard:判断某用户是否为任一 active org 的 owner。
// 主进程在 main.go 用 organization 模块适配器实现后注入;
// 为 nil 时 DeleteAccount 跳过检查(单测 / 旧部署兼容)。
type OwnerChecker interface {
	// ListActiveOrgsOwnedBy 返回该 user 当前持有的 active org 列表。
	// 空列表 + nil error = 无 owner 身份,可安全注销。
	ListActiveOrgsOwnedBy(ctx context.Context, userID uint64) ([]user.OwnedOrgSummary, error)
}

// SetOwnerChecker 见 UserService.SetOwnerChecker 注释。
func (s *userService) SetOwnerChecker(checker OwnerChecker) {
	s.ownerChecker = checker
}

// NewUserService 构造一个 UserService 实例。
//
// codeStore / emailSender / emailCfg 可传 nil → 邮箱验证码接口返回 ErrUserInternal,
// 其余功能不受影响。main.go 装配时只要装了 Email 配置就应该全部传入。
//
// loginGuard / userCfg 可传 nil → 登录失败锁 + 注册滑动窗口限流全部关闭(开发/单测友好)。
// 生产环境必须都传,否则反爆破防线就没有。
// pwdResetStore 可传 nil → 密码重置接口返 ErrUserInternal。
// pwdPolicy 可传 nil → 退化到仅长度 8 位的旧行为(仅用于单测和过渡);
// 生产必须传,main.go 装配时 pwdpolicy.New 失败应直接 fatal。
//
// ownerChecker(M3.7)有环形依赖风险(org service 依赖 user.UserVerifier,
// user service 反过来依赖 org 侧 owner 查询),因此留晚绑定,见 SetOwnerChecker。
func NewUserService(
	repo repository.Repository,
	jwtManager *jwt.JWTManager,
	sessionStore user.SessionStore,
	maxSessionsPerUser int,
	log logger.LoggerInterface,
	codeStore EmailCodeStore,
	emailSender email.Sender,
	emailCfg *config.EmailConfig,
	loginGuard LoginGuard,
	userCfg *config.UserConfig,
	pwdResetStore PasswordResetStore,
	pwdPolicy *pwdpolicy.Policy,
	oauthExchangeStore OAuthExchangeStore,
	emailVerifyStore EmailVerifyStore,
) UserService {
	return &userService{
		repo:               repo,
		jwtManager:         jwtManager,
		sessionStore:       sessionStore,
		maxSessionsPerUser: maxSessionsPerUser,
		log:                log,
		codeStore:          codeStore,
		emailSender:        emailSender,
		emailCfg:           emailCfg,
		loginGuard:         loginGuard,
		userCfg:            userCfg,
		pwdResetStore:      pwdResetStore,
		pwdPolicy:          pwdPolicy,
		oauthExchangeStore: oauthExchangeStore,
		emailVerifyStore:   emailVerifyStore,
	}
}

// checkPassword 统一密码策略校验,Register 和 ConfirmPasswordReset 共用。
// pwdPolicy 为 nil 时仅校验 8 位最短(过渡行为,生产绝对不会走这分支)。
//
// 纯验证器:无 ctx 无 logger,调用方按上下文(注册/改密)统一打业务日志;
// 在此打日志反而会重复记录。
func (s *userService) checkPassword(pw string) error {
	if s.pwdPolicy == nil {
		if len(pw) < 8 {
			//sayso-lint:ignore log-coverage
			return fmt.Errorf("password too short: %w", user.ErrPasswordTooShort)
		}
		return nil
	}
	switch err := s.pwdPolicy.Validate(pw); {
	case errors.Is(err, pwdpolicy.ErrTooShort):
		//sayso-lint:ignore log-coverage
		return fmt.Errorf("password too short: %w", user.ErrPasswordTooShort)
	case errors.Is(err, pwdpolicy.ErrTooCommon):
		//sayso-lint:ignore log-coverage
		return fmt.Errorf("password too common: %w", user.ErrPasswordTooCommon)
	case err != nil:
		//sayso-lint:ignore log-coverage
		return fmt.Errorf("pwd policy: %w: %w", err, user.ErrUserInternal)
	}
	return nil
}

// loginLockTTL 解析 cfg.User.LoginFail.LockTTL,失败回退 15m。
func (s *userService) loginLockTTL() time.Duration {
	const fallback = 15 * time.Minute
	if s.userCfg == nil || s.userCfg.LoginFail.LockTTL == "" {
		return fallback
	}
	d, err := time.ParseDuration(s.userCfg.LoginFail.LockTTL)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

// loginFailMax 连续失败触发锁定的阈值,userCfg 为 nil 返回 0(不锁)。
func (s *userService) loginFailMax() int {
	if s.userCfg == nil {
		return 0
	}
	return s.userCfg.LoginFail.Max
}

// registerRateMax / registerRateWindow 返回注册滑动窗口参数,userCfg 为 nil 时返 0(不限流)。
func (s *userService) registerRateMax() int {
	if s.userCfg == nil {
		return 0
	}
	return s.userCfg.RegisterRate.Max
}

func (s *userService) registerRateWindow() time.Duration {
	const fallback = 60 * time.Second
	if s.userCfg == nil || s.userCfg.RegisterRate.WindowSec == 0 {
		return fallback
	}
	return time.Duration(s.userCfg.RegisterRate.WindowSec) * time.Second
}
