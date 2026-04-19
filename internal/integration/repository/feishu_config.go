// feishu_config.go OrgFeishuConfig 的持久化层。
//
// 为什么和 UserIntegration 的 Repository 分开:
//   - 语义正交:一个是"组织 App 凭证",一个是"用户授权令牌"
//   - 调用路径不同:config 只给 admin handler + FeishuService 内部查;
//     UserIntegration 主要给 sync worker / callback
package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/integration/model"
)

// FeishuConfigRepository org_feishu_configs 表的数据访问接口。
type FeishuConfigRepository interface {
	// GetByOrg 按 org_id 取配置。找不到返 (nil, nil) —— 表示该 org 未配置飞书应用。
	GetByOrg(ctx context.Context, orgID uint64) (*model.OrgFeishuConfig, error)

	// Upsert 写入或更新。按 org_id 作唯一键定位。
	Upsert(ctx context.Context, in *model.OrgFeishuConfig) (*model.OrgFeishuConfig, error)

	// Delete 按 org_id 删除配置。幂等(不存在也返 nil)。
	// 删除后该 org 的成员无法再走新授权;已有 UserIntegration 行不自动清理 —— 让 admin 显式处理。
	Delete(ctx context.Context, orgID uint64) error
}

// NewFeishuConfigRepository 构造。
func NewFeishuConfigRepository(db *gorm.DB) FeishuConfigRepository {
	return &gormFeishuConfigRepo{db: db}
}

type gormFeishuConfigRepo struct{ db *gorm.DB }

func (r *gormFeishuConfigRepo) GetByOrg(ctx context.Context, orgID uint64) (*model.OrgFeishuConfig, error) {
	if orgID == 0 {
		return nil, fmt.Errorf("feishu config get: org_id required")
	}
	var row model.OrgFeishuConfig
	err := r.db.WithContext(ctx).Where("org_id = ?", orgID).Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("feishu config get: %w", err)
	}
	return &row, nil
}

func (r *gormFeishuConfigRepo) Upsert(ctx context.Context, in *model.OrgFeishuConfig) (*model.OrgFeishuConfig, error) {
	if in == nil || in.OrgID == 0 || in.AppID == "" || in.AppSecret == "" {
		return nil, fmt.Errorf("feishu config upsert: org_id + app_id + app_secret required")
	}
	existing, err := r.GetByOrg(ctx, in.OrgID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		if err := r.db.WithContext(ctx).Create(in).Error; err != nil {
			return nil, fmt.Errorf("feishu config create: %w", err)
		}
		return in, nil
	}
	updates := map[string]any{
		"app_id":     in.AppID,
		"app_secret": in.AppSecret,
	}
	if err := r.db.WithContext(ctx).Model(&model.OrgFeishuConfig{}).
		Where("id = ?", existing.ID).
		Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("feishu config update: %w", err)
	}
	existing.AppID = in.AppID
	existing.AppSecret = in.AppSecret
	return existing, nil
}

func (r *gormFeishuConfigRepo) Delete(ctx context.Context, orgID uint64) error {
	if orgID == 0 {
		return fmt.Errorf("feishu config delete: org_id required")
	}
	if err := r.db.WithContext(ctx).
		Where("org_id = ?", orgID).
		Delete(&model.OrgFeishuConfig{}).Error; err != nil {
		return fmt.Errorf("feishu config delete: %w", err)
	}
	return nil
}
