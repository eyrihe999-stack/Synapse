package repository

import (
	"context"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/user/model"
	"gorm.io/gorm"
)

// Repository user 模块数据访问入口。
type Repository interface {
	WithTx(ctx context.Context, fn func(tx Repository) error) error
	CreateUser(ctx context.Context, user *model.User) error
	FindByID(ctx context.Context, id uint64) (*model.User, error)
	FindByEmail(ctx context.Context, email string) (*model.User, error)
	UpdateFields(ctx context.Context, id uint64, updates map[string]any) error
}

type gormRepository struct {
	db *gorm.DB
}

func New(db *gorm.DB) Repository {
	return &gormRepository{db: db}
}

func (r *gormRepository) WithTx(ctx context.Context, fn func(tx Repository) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&gormRepository{db: tx})
	})
}

func (r *gormRepository) CreateUser(ctx context.Context, user *model.User) error {
	if err := r.db.WithContext(ctx).Create(user).Error; err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}

func (r *gormRepository) FindByID(ctx context.Context, id uint64) (*model.User, error) {
	var user model.User
	if err := r.db.WithContext(ctx).Where("id = ? AND status = ?", id, model.StatusActive).First(&user).Error; err != nil {
		return nil, fmt.Errorf("find user by id: %w", err)
	}
	return &user, nil
}

func (r *gormRepository) FindByEmail(ctx context.Context, email string) (*model.User, error) {
	var user model.User
	if err := r.db.WithContext(ctx).Where("email = ? AND status = ?", email, model.StatusActive).First(&user).Error; err != nil {
		return nil, fmt.Errorf("find user by email: %w", err)
	}
	return &user, nil
}

func (r *gormRepository) UpdateFields(ctx context.Context, id uint64, updates map[string]any) error {
	if err := r.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return fmt.Errorf("update user fields: %w", err)
	}
	return nil
}
