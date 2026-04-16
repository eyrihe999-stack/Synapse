// service.go user 模块业务逻辑层。
package service

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/user"
	"github.com/eyrihe999-stack/Synapse/internal/user/model"
	"github.com/eyrihe999-stack/Synapse/internal/user/repository"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"github.com/eyrihe999-stack/Synapse/pkg/utils"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// RegisterRequest 用户注册请求。
type RegisterRequest struct {
	Email       string `json:"email" binding:"required"`
	Password    string `json:"password" binding:"required"`
	DisplayName string `json:"display_name"`
}

// LoginRequest 用户登录请求。
type LoginRequest struct {
	Email    string `json:"email" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// RefreshRequest 刷新 token 请求。
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

// UpdateProfileRequest 更新个人信息请求。
type UpdateProfileRequest struct {
	DisplayName *string `json:"display_name"`
	AvatarURL   *string `json:"avatar_url"`
}

// AuthResponse 登录/注册成功后的认证响应。
type AuthResponse struct {
	AccessToken  string      `json:"access_token"`
	RefreshToken string      `json:"refresh_token"`
	ExpiresIn    int         `json:"expires_in"`
	User         UserProfile `json:"user"`
}

// UserProfile 用户公开资料。
type UserProfile struct {
	ID          uint64    `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	AvatarURL   string    `json:"avatar_url"`
	CreatedAt   time.Time `json:"created_at"`
}

// UserService 定义用户模块的业务操作接口。
type UserService interface {
	Register(ctx context.Context, req RegisterRequest) (*AuthResponse, error)
	Login(ctx context.Context, req LoginRequest) (*AuthResponse, error)
	GetProfile(ctx context.Context, userID uint64) (*UserProfile, error)
	UpdateProfile(ctx context.Context, userID uint64, req UpdateProfileRequest) (*UserProfile, error)
	RefreshToken(ctx context.Context, refreshToken string) (*AuthResponse, error)
}

type userService struct {
	repo       repository.Repository
	jwtManager *utils.JWTManager
	log        logger.LoggerInterface
}

// NewUserService 构造一个 UserService 实例。
func NewUserService(repo repository.Repository, jwtManager *utils.JWTManager, log logger.LoggerInterface) UserService {
	return &userService{repo: repo, jwtManager: jwtManager, log: log}
}

// Register 注册新用户并返回认证凭证。
//
// 校验邮箱格式和密码长度后创建用户,返回 access/refresh token。
// 返回 ErrInvalidEmail / ErrPasswordTooShort / ErrEmailAlreadyRegistered / ErrUserInternal。
func (s *userService) Register(ctx context.Context, req RegisterRequest) (*AuthResponse, error) {
	//sayso-lint:ignore err-swallow
	if _, err := mail.ParseAddress(req.Email); err != nil { // 丢弃解析结果,仅校验格式
		s.log.WarnCtx(ctx, "邮箱格式非法", map[string]any{"email": req.Email})
		return nil, fmt.Errorf("invalid email: %w", user.ErrInvalidEmail)
	}
	if len(req.Password) < 8 {
		s.log.WarnCtx(ctx, "密码长度不足", map[string]any{"email": req.Email})
		return nil, fmt.Errorf("password too short: %w", user.ErrPasswordTooShort)
	}

	// 检查邮箱是否已注册
	//sayso-lint:ignore err-swallow
	_, err := s.repo.FindByEmail(ctx, req.Email) // 丢弃 user 记录,仅检查是否存在
	if err == nil {
		s.log.WarnCtx(ctx, "邮箱已注册", map[string]any{"email": req.Email})
		return nil, fmt.Errorf("email taken: %w", user.ErrEmailAlreadyRegistered)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		s.log.ErrorCtx(ctx, "查询邮箱失败", err, map[string]any{"email": req.Email})
		return nil, fmt.Errorf("check email: %w: %w", err, user.ErrUserInternal)
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

	u := &model.User{
		Email:        req.Email,
		PasswordHash: string(hash),
		DisplayName:  displayName,
		Status:       model.StatusActive,
	}

	if err := s.repo.CreateUser(ctx, u); err != nil {
		s.log.ErrorCtx(ctx, "创建用户失败", err, map[string]any{"email": req.Email})
		return nil, fmt.Errorf("create user: %w: %w", err, user.ErrUserInternal)
	}

	//sayso-lint:ignore sentinel-wrap
	return s.generateAuthResponse(ctx, u) // generateAuthResponse 内部已包装 ErrUserInternal
}

// Login 用户登录,校验邮箱密码后返回认证凭证。
//
// 返回 ErrInvalidCredentials / ErrUserInternal。
func (s *userService) Login(ctx context.Context, req LoginRequest) (*AuthResponse, error) {
	u, err := s.repo.FindByEmail(ctx, req.Email)
	if err != nil {
		s.log.WarnCtx(ctx, "登录用户不存在", map[string]any{"email": req.Email})
		return nil, fmt.Errorf("find user: %w", user.ErrInvalidCredentials)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)); err != nil {
		s.log.WarnCtx(ctx, "密码不匹配", map[string]any{"email": req.Email})
		return nil, fmt.Errorf("password mismatch: %w", user.ErrInvalidCredentials)
	}

	now := time.Now().UTC()
	//sayso-lint:ignore err-swallow
	_ = s.repo.UpdateFields(ctx, u.ID, map[string]any{"last_login_at": now}) // best-effort 更新登录时间,失败不阻塞

	//sayso-lint:ignore sentinel-wrap
	return s.generateAuthResponse(ctx, u) // generateAuthResponse 内部已包装 ErrUserInternal
}

// GetProfile 按用户 ID 查询公开资料。
//
// 返回 ErrUserNotFound。
func (s *userService) GetProfile(ctx context.Context, userID uint64) (*UserProfile, error) {
	u, err := s.repo.FindByID(ctx, userID)
	if err != nil {
		s.log.WarnCtx(ctx, "用户不存在", map[string]any{"user_id": userID})
		return nil, fmt.Errorf("find user: %w", user.ErrUserNotFound)
	}
	return toUserProfile(u), nil
}

// UpdateProfile 更新当前用户的个人信息(display_name / avatar_url)。
//
// 返回 ErrUserInternal / ErrUserNotFound。
func (s *userService) UpdateProfile(ctx context.Context, userID uint64, req UpdateProfileRequest) (*UserProfile, error) {
	updates := make(map[string]any)
	if req.DisplayName != nil {
		updates["display_name"] = *req.DisplayName
	}
	if req.AvatarURL != nil {
		updates["avatar_url"] = *req.AvatarURL
	}
	if len(updates) > 0 {
		if err := s.repo.UpdateFields(ctx, userID, updates); err != nil {
			s.log.ErrorCtx(ctx, "更新用户信息失败", err, map[string]any{"user_id": userID})
			return nil, fmt.Errorf("update profile: %w: %w", err, user.ErrUserInternal)
		}
	}

	//sayso-lint:ignore sentinel-wrap
	return s.GetProfile(ctx, userID) // GetProfile 内部已返回 ErrUserNotFound sentinel
}

// RefreshToken 用 refresh token 换取新的认证凭证。
//
// 返回 ErrInvalidRefreshToken / ErrUserNotFound / ErrUserInternal。
func (s *userService) RefreshToken(ctx context.Context, refreshToken string) (*AuthResponse, error) {
	claims, err := s.jwtManager.ValidateRefreshToken(refreshToken)
	if err != nil {
		s.log.WarnCtx(ctx, "refresh token 无效", map[string]any{"error": err.Error()})
		return nil, fmt.Errorf("invalid refresh token: %w: %w", err, user.ErrInvalidRefreshToken)
	}

	u, err := s.repo.FindByID(ctx, claims.UserID)
	if err != nil {
		s.log.WarnCtx(ctx, "refresh token 对应用户不存在", map[string]any{"user_id": claims.UserID})
		return nil, fmt.Errorf("find user: %w", user.ErrUserNotFound)
	}

	//sayso-lint:ignore sentinel-wrap
	return s.generateAuthResponse(ctx, u) // generateAuthResponse 内部已包装 ErrUserInternal
}

// generateAuthResponse 生成 access/refresh token 对并组装响应。
func (s *userService) generateAuthResponse(ctx context.Context, u *model.User) (*AuthResponse, error) {
	//sayso-lint:ignore err-swallow
	accessToken, _, err := s.jwtManager.GenerateAccessToken(u.ID, u.Email, "") // 丢弃 expiry,仅需 token 字符串
	if err != nil {
		s.log.ErrorCtx(ctx, "生成 access token 失败", err, map[string]any{"user_id": u.ID})
		return nil, fmt.Errorf("generate access token: %w: %w", err, user.ErrUserInternal)
	}

	//sayso-lint:ignore err-swallow
	refreshToken, _, err := s.jwtManager.GenerateRefreshToken(u.ID, u.Email, "") // 丢弃 expiry,仅需 token 字符串
	if err != nil {
		s.log.ErrorCtx(ctx, "生成 refresh token 失败", err, map[string]any{"user_id": u.ID})
		return nil, fmt.Errorf("generate refresh token: %w: %w", err, user.ErrUserInternal)
	}

	return &AuthResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    s.jwtManager.GetAccessTokenDuration(),
		User:         *toUserProfile(u),
	}, nil
}

// toUserProfile 将 model.User 转为 UserProfile DTO。
func toUserProfile(u *model.User) *UserProfile {
	return &UserProfile{
		ID:          u.ID,
		Email:       u.Email,
		DisplayName: u.DisplayName,
		AvatarURL:   u.AvatarURL,
		CreatedAt:   u.CreatedAt,
	}
}
