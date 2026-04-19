// gitlab_config.go OrgGitLabConfig 的持久化层。
//
// 和 FeishuConfigRepository 对齐 —— 一张"per org 实例配置"表,admin handler 写,
// GitLabService 读。UserIntegration 的 repo 保持独立(语义正交:实例 vs 令牌)。
package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/integration/model"
)

// GitLabConfigRepository org_gitlab_configs 表的数据访问接口。
type GitLabConfigRepository interface {
	// GetByOrg 按 org_id 取配置。找不到返 (nil, nil) —— 表示该 org 未配置 GitLab 实例。
	GetByOrg(ctx context.Context, orgID uint64) (*model.OrgGitLabConfig, error)

	// Upsert 写入或更新。按 org_id 作唯一键定位。
	Upsert(ctx context.Context, in *model.OrgGitLabConfig) (*model.OrgGitLabConfig, error)

	// Delete 按 org_id 删除配置。幂等(不存在也返 nil)。
	// 删除后该 org 的成员无法再 Connect;已有 UserIntegration 行不联动清理 —— admin 显式处理。
	Delete(ctx context.Context, orgID uint64) error
}

// NewGitLabConfigRepository 构造。
func NewGitLabConfigRepository(db *gorm.DB) GitLabConfigRepository {
	return &gormGitLabConfigRepo{db: db}
}

type gormGitLabConfigRepo struct{ db *gorm.DB }

func (r *gormGitLabConfigRepo) GetByOrg(ctx context.Context, orgID uint64) (*model.OrgGitLabConfig, error) {
	if orgID == 0 {
		return nil, fmt.Errorf("gitlab config get: org_id required")
	}
	var row model.OrgGitLabConfig
	err := r.db.WithContext(ctx).Where("org_id = ?", orgID).Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("gitlab config get: %w", err)
	}
	return &row, nil
}

func (r *gormGitLabConfigRepo) Upsert(ctx context.Context, in *model.OrgGitLabConfig) (*model.OrgGitLabConfig, error) {
	if in == nil || in.OrgID == 0 || in.BaseURL == "" {
		return nil, fmt.Errorf("gitlab config upsert: org_id + base_url required")
	}
	existing, err := r.GetByOrg(ctx, in.OrgID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		if err := r.db.WithContext(ctx).Create(in).Error; err != nil {
			return nil, fmt.Errorf("gitlab config create: %w", err)
		}
		return in, nil
	}
	// 用 map 确保 InsecureSkipVerify=false 这种零值也能写入(Updates 默认忽略零值,Select 逃逸也要写字段名)。
	updates := map[string]any{
		"base_url":             in.BaseURL,
		"insecure_skip_verify": in.InsecureSkipVerify,
	}
	if err := r.db.WithContext(ctx).Model(&model.OrgGitLabConfig{}).
		Where("id = ?", existing.ID).
		Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("gitlab config update: %w", err)
	}
	existing.BaseURL = in.BaseURL
	existing.InsecureSkipVerify = in.InsecureSkipVerify
	return existing, nil
}

func (r *gormGitLabConfigRepo) Delete(ctx context.Context, orgID uint64) error {
	if orgID == 0 {
		return fmt.Errorf("gitlab config delete: org_id required")
	}
	if err := r.db.WithContext(ctx).
		Where("org_id = ?", orgID).
		Delete(&model.OrgGitLabConfig{}).Error; err != nil {
		return fmt.Errorf("gitlab config delete: %w", err)
	}
	return nil
}
