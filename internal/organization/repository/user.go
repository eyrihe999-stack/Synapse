// user.go 用户只读查询,用于邀请候选人查找。
//
// organization 模块需要按 user_id / 昵称 / 邮箱 查找已注册用户,
// 这里直接对 users 表做只读查询,返回一个局部的 UserProfile 轻量结构。
package repository

import (
	"context"
	"fmt"
)

// UserProfile 邀请场景下需要的用户公开信息。
type UserProfile struct {
	UserID      uint64
	DisplayName string
	AvatarURL   string
	MaskedEmail string // 脱敏邮箱,如 "a****e@example.com"
	Status      int32  // 1=active
}

// FindUserProfileByID 按 user_id 查找用户,不存在返回 nil。
func (r *gormRepository) FindUserProfileByID(ctx context.Context, userID uint64) (*UserProfile, error) {
	type row struct {
		ID          uint64
		DisplayName *string
		AvatarURL   *string
		Email       *string
		Status      int32
	}
	var rr row
	err := r.db.WithContext(ctx).Raw(`
		SELECT id, display_name, avatar_url, email, status
		FROM users
		WHERE id = ? AND status = 1 AND deleted_at IS NULL
		LIMIT 1
	`, userID).Scan(&rr).Error
	if err != nil {
		return nil, fmt.Errorf("find user by id: %w", err)
	}
	if rr.ID == 0 {
		return nil, nil
	}
	return buildUserProfile(rr.ID, rr.DisplayName, rr.AvatarURL, rr.Email, rr.Status), nil
}

// FindUserProfilesByEmail 按邮箱模糊查找 active 用户,返回候选列表。
func (r *gormRepository) FindUserProfilesByEmail(ctx context.Context, email string, limit int) ([]*UserProfile, error) {
	if limit <= 0 {
		limit = 20
	}
	type row struct {
		ID          uint64
		DisplayName *string
		AvatarURL   *string
		Email       *string
		Status      int32
	}
	var rows []row
	err := r.db.WithContext(ctx).Raw(`
		SELECT id, display_name, avatar_url, email, status
		FROM users
		WHERE email LIKE ? AND status = 1 AND deleted_at IS NULL
		ORDER BY id ASC
		LIMIT ?
	`, "%"+email+"%", limit).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("search users by email: %w", err)
	}
	out := make([]*UserProfile, 0, len(rows))
	for _, rr := range rows {
		out = append(out, buildUserProfile(rr.ID, rr.DisplayName, rr.AvatarURL, rr.Email, rr.Status))
	}
	return out, nil
}

// SearchUserProfilesByDisplayName 按昵称模糊查找 active 用户,返回候选列表。
func (r *gormRepository) SearchUserProfilesByDisplayName(ctx context.Context, name string, limit int) ([]*UserProfile, error) {
	if limit <= 0 {
		limit = 20
	}
	type row struct {
		ID          uint64
		DisplayName *string
		AvatarURL   *string
		Email       *string
		Status      int32
	}
	var rows []row
	err := r.db.WithContext(ctx).Raw(`
		SELECT id, display_name, avatar_url, email, status
		FROM users
		WHERE display_name LIKE ? AND status = 1 AND deleted_at IS NULL
		ORDER BY id ASC
		LIMIT ?
	`, "%"+name+"%", limit).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("search users by name: %w", err)
	}
	out := make([]*UserProfile, 0, len(rows))
	for _, rr := range rows {
		out = append(out, buildUserProfile(rr.ID, rr.DisplayName, rr.AvatarURL, rr.Email, rr.Status))
	}
	return out, nil
}

func buildUserProfile(id uint64, displayName, avatarURL, email *string, status int32) *UserProfile {
	p := &UserProfile{
		UserID: id,
		Status: status,
	}
	if displayName != nil {
		p.DisplayName = *displayName
	}
	if avatarURL != nil {
		p.AvatarURL = *avatarURL
	}
	if email != nil {
		p.MaskedEmail = maskEmail(*email)
	}
	return p
}

func maskEmail(email string) string {
	atIdx := -1
	for i, c := range email {
		if c == '@' {
			atIdx = i
			break
		}
	}
	if atIdx <= 1 {
		return email
	}
	local := email[:atIdx]
	domain := email[atIdx:]
	if len(local) <= 2 {
		return string(local[0]) + "***" + domain
	}
	return string(local[0]) + "****" + string(local[len(local)-1]) + domain
}
