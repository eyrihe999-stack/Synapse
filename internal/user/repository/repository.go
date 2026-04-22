package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/user/model"
	"gorm.io/gorm"
)

// Repository user 模块数据访问入口。
//
// 查询方法命名口径(M1.7):
//   - FindActiveByXxx:仅匹配 status=active;登录、鉴权、OAuth 合并等正常路径用
//   - FindLivingByXxx:匹配 status != deleted(含 pending_verify/active/banned);
//     用于"邮箱占用检查""自己看自己资料"等不能放过封禁/未验证账号的场景
//   - 未来若需看审计全量(含 deleted),再引入 FindAnyByXxx
type Repository interface {
	WithTx(ctx context.Context, fn func(tx Repository) error) error
	CreateUser(ctx context.Context, user *model.User) error
	FindActiveByID(ctx context.Context, id uint64) (*model.User, error)
	FindActiveByEmail(ctx context.Context, email string) (*model.User, error)
	FindLivingByID(ctx context.Context, id uint64) (*model.User, error)
	FindLivingByEmail(ctx context.Context, email string) (*model.User, error)
	UpdateFields(ctx context.Context, id uint64, updates map[string]any) error

	// ── UserIdentity (M1.6) ──────────────────────────────────────────────────
	FindIdentity(ctx context.Context, provider, subject string) (*model.UserIdentity, error)
	CreateIdentity(ctx context.Context, identity *model.UserIdentity) error
	DeleteIdentitiesByUserID(ctx context.Context, userID uint64) error

	// ── 生命周期 (M1.7) ───────────────────────────────────────────────────────
	// MarkUserDeleted 把用户切到 deleted 态并 pseudo 化 PII。
	// 入参 updates 由 service 层组装(email/password_hash/display_name/avatar_url/
	// status/deleted_at/deleted_reason 等),repo 层只是透传,避免把业务常量下沉到 repo。
	MarkUserDeleted(ctx context.Context, userID uint64, updates map[string]any) error

	// ListStalePendingVerify 扫 status=pending_verify 且 created_at < cutoff 的 user ID,按 created_at ASC 取最老。
	// 给"清理过期 pending_verify 账号"CLI 用,避免被攻击者恶意占位邮箱。
	ListStalePendingVerify(ctx context.Context, cutoff time.Time, limit int) ([]uint64, error)

	// ── 登录审计 ─────────────────────────────────────────────────────────────
	// CreateLoginEvent best-effort 写一条登录事件;调用方应忽略 error 只打 log,不把审计挂失败放大成登录失败。
	CreateLoginEvent(ctx context.Context, event *model.LoginEvent) error
	// HasDeviceSeen 判定该 (user_id, device_id) 是否以前成功登录过,给"新设备通知邮件"用。
	HasDeviceSeen(ctx context.Context, userID uint64, deviceID string) (bool, error)
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

// FindActiveByID 仅匹配 status=active 的用户。
// pending_verify / banned / deleted 一律按"不存在"返回 gorm.ErrRecordNotFound。
func (r *gormRepository) FindActiveByID(ctx context.Context, id uint64) (*model.User, error) {
	var user model.User
	if err := r.db.WithContext(ctx).Where("id = ? AND status = ?", id, model.StatusActive).First(&user).Error; err != nil {
		return nil, fmt.Errorf("find active user by id: %w", err)
	}
	return &user, nil
}

// FindActiveByEmail 仅匹配 status=active 的用户。
func (r *gormRepository) FindActiveByEmail(ctx context.Context, email string) (*model.User, error) {
	var user model.User
	if err := r.db.WithContext(ctx).Where("email = ? AND status = ?", email, model.StatusActive).First(&user).Error; err != nil {
		return nil, fmt.Errorf("find active user by email: %w", err)
	}
	return &user, nil
}

// FindLivingByID 匹配 status != deleted 的用户(含 pending_verify/active/banned)。
// 用于"自己查自己资料""管理后台"等需要看到非 active 但仍占位的账号的场景。
func (r *gormRepository) FindLivingByID(ctx context.Context, id uint64) (*model.User, error) {
	var user model.User
	if err := r.db.WithContext(ctx).Where("id = ? AND status <> ?", id, model.StatusDeleted).First(&user).Error; err != nil {
		return nil, fmt.Errorf("find living user by id: %w", err)
	}
	return &user, nil
}

// FindLivingByEmail 匹配 status != deleted 的用户。
// 注册流程用此判重 —— banned / pending_verify 账号的 email 也视为"已占用",
// 防止封禁用户被他人抢注同邮箱。deleted 用户的 email 已 pseudo 化,不会误命中。
func (r *gormRepository) FindLivingByEmail(ctx context.Context, email string) (*model.User, error) {
	var user model.User
	if err := r.db.WithContext(ctx).Where("email = ? AND status <> ?", email, model.StatusDeleted).First(&user).Error; err != nil {
		return nil, fmt.Errorf("find living user by email: %w", err)
	}
	return &user, nil
}

func (r *gormRepository) UpdateFields(ctx context.Context, id uint64, updates map[string]any) error {
	if err := r.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return fmt.Errorf("update user fields: %w", err)
	}
	return nil
}

// FindIdentity 按 (provider, subject) 查 identity。
// 未命中走 gorm.ErrRecordNotFound,service 层据此决定走"合并"或"新建"分支。
func (r *gormRepository) FindIdentity(ctx context.Context, provider, subject string) (*model.UserIdentity, error) {
	var identity model.UserIdentity
	if err := r.db.WithContext(ctx).
		Where("provider = ? AND subject = ?", provider, subject).
		First(&identity).Error; err != nil {
		return nil, fmt.Errorf("find identity: %w", err)
	}
	return &identity, nil
}

// CreateIdentity 写入一条新 identity。并发竞争时会撞 (provider, subject) 唯一索引;
// service 层在外层用 WithTx 保证同一用户不会被同时创建两份。
func (r *gormRepository) CreateIdentity(ctx context.Context, identity *model.UserIdentity) error {
	if err := r.db.WithContext(ctx).Create(identity).Error; err != nil {
		return fmt.Errorf("create identity: %w", err)
	}
	return nil
}

// DeleteIdentitiesByUserID 硬删一个 user 的全部第三方绑定。
// 注销流程调用:清掉 (provider, subject) 避免悬空指向一个 pseudo 化的 user,
// 同时允许该第三方账号将来重新绑定到新 user。
func (r *gormRepository) DeleteIdentitiesByUserID(ctx context.Context, userID uint64) error {
	if err := r.db.WithContext(ctx).Where("user_id = ?", userID).Delete(&model.UserIdentity{}).Error; err != nil {
		return fmt.Errorf("delete identities by user id: %w", err)
	}
	return nil
}

// ListStalePendingVerify 取 status=pending_verify 且 created_at < cutoff 的 user ID。
// 按 created_at ASC 取最老批次保证进度推进。
func (r *gormRepository) ListStalePendingVerify(ctx context.Context, cutoff time.Time, limit int) ([]uint64, error) {
	if limit <= 0 {
		limit = 100
	}
	var ids []uint64
	err := r.db.WithContext(ctx).
		Model(&model.User{}).
		Where("status = ? AND created_at < ?", model.StatusPendingVerify, cutoff).
		Order("created_at ASC").
		Limit(limit).
		Pluck("id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("list stale pending_verify: %w", err)
	}
	return ids, nil
}

// MarkUserDeleted 更新用户为 deleted 态;具体字段由 service 层决定。
// 条件 status <> deleted 防止重复注销(已 deleted 的用户再跑一遍会覆盖 deleted_at)。
func (r *gormRepository) MarkUserDeleted(ctx context.Context, userID uint64, updates map[string]any) error {
	res := r.db.WithContext(ctx).
		Model(&model.User{}).
		Where("id = ? AND status <> ?", userID, model.StatusDeleted).
		Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("mark user deleted: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		// 走到这说明用户不存在或已 deleted。service 层语义上两种都算"幂等",
		// 但需要区分:返 ErrRecordNotFound 交由 service 层决定走哪个错误路径。
		return fmt.Errorf("mark user deleted: %w", gorm.ErrRecordNotFound)
	}
	return nil
}

