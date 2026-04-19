// client.go oauth_clients 表数据访问。
package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/oauth/model"
)

// ClientRepo oauth_clients CRUD。
type ClientRepo interface {
	// Create 注册新客户端。ClientID 必须由调用方生成好(opaque 随机串)。
	Create(ctx context.Context, c *model.OAuthClient) error

	// GetByClientID 按 client_id 查。not-found 返 (nil, nil),调用方按 nil 判。
	// 只返 active 的(status=suspended 视同不存在,避免 client 拿着旧 id 继续走 flow)。
	GetByClientID(ctx context.Context, clientID string) (*model.OAuthClient, error)

	// UpdateStatus 改状态(suspend / re-activate)。admin 用。
	UpdateStatus(ctx context.Context, clientID, status string) error
}

type gormClientRepo struct {
	db *gorm.DB
}

func NewClientRepo(db *gorm.DB) ClientRepo { return &gormClientRepo{db: db} }

func (r *gormClientRepo) Create(ctx context.Context, c *model.OAuthClient) error {
	if err := r.db.WithContext(ctx).Create(c).Error; err != nil {
		return fmt.Errorf("create oauth client: %w", err)
	}
	return nil
}

func (r *gormClientRepo) GetByClientID(ctx context.Context, clientID string) (*model.OAuthClient, error) {
	var c model.OAuthClient
	err := r.db.WithContext(ctx).
		Where("client_id = ? AND status = ?", clientID, "active").
		First(&c).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("get oauth client: %w", err)
	}
	return &c, nil
}

func (r *gormClientRepo) UpdateStatus(ctx context.Context, clientID, status string) error {
	res := r.db.WithContext(ctx).
		Model(&model.OAuthClient{}).
		Where("client_id = ?", clientID).
		Update("status", status)
	if res.Error != nil {
		return fmt.Errorf("update oauth client status: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("update oauth client status: not found: %s", clientID)
	}
	return nil
}
