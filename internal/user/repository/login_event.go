// login_event.go 登录事件审计的只写 + 少量读查询。
//
// 设计:
//   - 写路径走主表 + 普通 Create(事件不参与事务,best-effort 写入)
//   - 读路径仅用于新设备检测(HasDeviceSeen),其他查询由未来 admin dashboard 再加
package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/user/model"
	"gorm.io/gorm"
)

// CreateLoginEvent 写一条登录审计。失败已返回 wrapped error;上层 best-effort 忽略。
func (r *gormRepository) CreateLoginEvent(ctx context.Context, event *model.LoginEvent) error {
	if err := r.db.WithContext(ctx).Create(event).Error; err != nil {
		return fmt.Errorf("create login event: %w", err)
	}
	return nil
}

// HasDeviceSeen 查该 (user_id, device_id) 组合是否出现过 success 类事件。
// 用途:首次在此 device 登录成功 → 发"新设备登录"通知邮件。
//
// 只认 success 类型(password_success / oauth_success),失败尝试不算"用过的设备",
// 否则攻击者在某 device 上失败一次就把该 device 标记为"已见",用户收不到告警。
func (r *gormRepository) HasDeviceSeen(ctx context.Context, userID uint64, deviceID string) (bool, error) {
	if userID == 0 || deviceID == "" {
		return false, nil
	}
	var ev model.LoginEvent
	err := r.db.WithContext(ctx).
		Select("id").
		Where("user_id = ? AND device_id = ? AND event_type IN (?)", userID, deviceID,
			[]string{model.LoginEventPasswordSuccess, model.LoginEventOAuthSuccess}).
		Limit(1).
		First(&ev).Error
	if err == nil {
		return true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return false, fmt.Errorf("has device seen: %w", err)
}
