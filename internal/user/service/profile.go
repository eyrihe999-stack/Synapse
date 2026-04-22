// profile.go user 模块 profile 读取 / 更新与 DTO 映射。
package service

import (
	"context"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/user"
	"github.com/eyrihe999-stack/Synapse/internal/user/model"
)

// GetProfile 按用户 ID 查询公开资料。
//
// 返回 ErrUserNotFound。
func (s *userService) GetProfile(ctx context.Context, userID uint64) (*UserProfile, error) {
	// FindLivingByID:允许 pending_verify 用户看自己的资料(走完注册但未点激活链接时,
	// 前端仍需要拿到用户信息);banned 用户也能看到自己被封禁的账号状态,便于客诉。
	u, err := s.repo.FindLivingByID(ctx, userID)
	if err != nil {
		// user_id 由 auth middleware 自动注入到 ctx,这里不再手填。
		s.log.WarnCtx(ctx, "用户不存在", nil)
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
			s.log.ErrorCtx(ctx, "更新用户信息失败", err, nil)
			return nil, fmt.Errorf("update profile: %w: %w", err, user.ErrUserInternal)
		}
	}

	//sayso-lint:ignore sentinel-wrap
	return s.GetProfile(ctx, userID) // GetProfile 内部已返回 ErrUserNotFound sentinel
}

// toUserProfile 将 model.User 转为 UserProfile DTO。
func toUserProfile(u *model.User) *UserProfile {
	return &UserProfile{
		ID:              u.ID,
		Email:           u.Email,
		DisplayName:     u.DisplayName,
		AvatarURL:       u.AvatarURL,
		Status:          u.Status,
		EmailVerifiedAt: u.EmailVerifiedAt,
		LastLoginAt:     u.LastLoginAt,
		CreatedAt:       u.CreatedAt,
	}
}
