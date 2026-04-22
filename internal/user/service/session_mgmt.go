// session_mgmt.go user 模块 refresh / session 列表 / 踢下线 / 全登出。
package service

import (
	"context"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/user"
)

// RefreshToken 用 refresh token 换取新的认证凭证。
//
// 校验 refresh token 签名后,检查 Redis 中 JTI 是否匹配,
// 匹配则签发新 token 对并更新 session。
// 返回 ErrInvalidRefreshToken / ErrSessionRevoked / ErrUserNotFound / ErrUserInternal。
func (s *userService) RefreshToken(ctx context.Context, req RefreshRequest) (*AuthResponse, error) {
	claims, err := s.jwtManager.ValidateRefreshToken(req.RefreshToken)
	if err != nil {
		s.log.WarnCtx(ctx, "refresh token 无效", map[string]interface{}{"error": err.Error()})
		return nil, fmt.Errorf("invalid refresh token: %w: %w", err, user.ErrInvalidRefreshToken)
	}

	deviceID := claims.DeviceID
	if deviceID == "" {
		deviceID = "default"
	}

	// 校验 Redis session 中的 JTI 是否匹配
	session, err := s.sessionStore.Get(ctx, claims.UserID, deviceID)
	if err != nil {
		s.log.WarnCtx(ctx, "session 不存在,可能已被踢下线", map[string]interface{}{
			"user_id": claims.UserID, "device_id": deviceID,
		})
		return nil, fmt.Errorf("session revoked: %w", user.ErrSessionRevoked)
	}
	if session.JTI != claims.ID {
		s.log.WarnCtx(ctx, "refresh token JTI 不匹配,可能已被替换", map[string]interface{}{
			"user_id": claims.UserID, "device_id": deviceID,
		})
		return nil, fmt.Errorf("jti mismatch: %w", user.ErrSessionRevoked)
	}

	// refresh token 换 access token:用户必须是 active;
	// banned 或已注销的账号就算手里有 refresh token 也不能续命。
	u, err := s.repo.FindActiveByID(ctx, claims.UserID)
	if err != nil {
		s.log.WarnCtx(ctx, "refresh token 对应用户不存在或非 active", map[string]interface{}{"user_id": claims.UserID})
		return nil, fmt.Errorf("find user: %w", user.ErrUserNotFound)
	}

	// 使用请求中的 device_name(如果有),否则保留原 session 的
	deviceName := req.DeviceName
	if deviceName == "" {
		deviceName = session.DeviceName
	}
	loginIP := req.LoginIP
	if loginIP == "" {
		loginIP = session.LoginIP
	}

	//sayso-lint:ignore sentinel-wrap
	return s.generateAuthResponse(ctx, u, deviceID, deviceName, loginIP)
}

// ListSessions 返回用户的所有活跃设备 session。
func (s *userService) ListSessions(ctx context.Context, userID uint64) ([]user.SessionEntry, error) {
	entries, err := s.sessionStore.List(ctx, userID)
	if err != nil {
		s.log.ErrorCtx(ctx, "查询 session 列表失败", err, nil)
		return nil, fmt.Errorf("list sessions: %w: %w", err, user.ErrUserInternal)
	}
	return entries, nil
}

// KickSession 踢掉指定设备的 session。
//
// 返回 ErrSessionNotFound / ErrUserInternal。
func (s *userService) KickSession(ctx context.Context, userID uint64, deviceID string) error {
	// 先检查 session 是否存在
	//sayso-lint:ignore err-swallow
	if _, err := s.sessionStore.Get(ctx, userID, deviceID); err != nil { // 丢弃 session 信息,仅检查是否存在
		// user_id 由 auth middleware 注入到 ctx;device_id 是被踢的目标设备,和 ctx 里的 session_id(当前请求的 device)可能不同,保留。
		s.log.WarnCtx(ctx, "session 不存在", map[string]interface{}{"device_id": deviceID})
		return fmt.Errorf("session not found: %w", user.ErrSessionNotFound)
	}
	if err := s.sessionStore.Delete(ctx, userID, deviceID); err != nil {
		s.log.ErrorCtx(ctx, "踢设备失败", err, map[string]interface{}{"device_id": deviceID})
		return fmt.Errorf("kick session: %w: %w", err, user.ErrUserInternal)
	}
	return nil
}

// LogoutAll 退出用户的所有设备。
//
// 返回 ErrUserInternal。
func (s *userService) LogoutAll(ctx context.Context, userID uint64) error {
	if err := s.sessionStore.DeleteAll(ctx, userID); err != nil {
		s.log.ErrorCtx(ctx, "退出所有设备失败", err, nil)
		return fmt.Errorf("logout all: %w: %w", err, user.ErrUserInternal)
	}
	return nil
}
