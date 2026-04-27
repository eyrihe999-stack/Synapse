// document.go channel 共享文档(PR #9')的数据访问。三类对象 ——
// channel_documents(元数据 + 最新版指针)、channel_document_versions(版本历史 append-only)、
// channel_document_locks(独占编辑锁,PK=document_id 互斥)。
package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/channel/model"
)

// ── ChannelDocument ─────────────────────────────────────────────────────────

func (r *gormRepository) CreateChannelDocument(ctx context.Context, d *model.ChannelDocument) error {
	return r.db.WithContext(ctx).Create(d).Error
}

// FindChannelDocumentByID 不过滤 deleted_at —— 调用方按需自己判断,审计读历史可能要看已删文档。
func (r *gormRepository) FindChannelDocumentByID(ctx context.Context, id uint64) (*model.ChannelDocument, error) {
	var d model.ChannelDocument
	if err := r.db.WithContext(ctx).First(&d, id).Error; err != nil {
		return nil, err
	}
	return &d, nil
}

// ListChannelDocumentsByChannel 列 channel 下未软删的共享文档,按 updated_at DESC。
// 公共空间视图用 —— 最近编辑的排前面。
func (r *gormRepository) ListChannelDocumentsByChannel(ctx context.Context, channelID uint64) ([]model.ChannelDocument, error) {
	var ds []model.ChannelDocument
	if err := r.db.WithContext(ctx).
		Where("channel_id = ? AND deleted_at IS NULL", channelID).
		Order("updated_at DESC").
		Find(&ds).Error; err != nil {
		return nil, err
	}
	return ds, nil
}

func (r *gormRepository) UpdateChannelDocumentFields(ctx context.Context, id uint64, updates map[string]any) error {
	return r.db.WithContext(ctx).Model(&model.ChannelDocument{}).Where("id = ?", id).Updates(updates).Error
}

// SoftDeleteChannelDocument 设 deleted_at;不级联删 versions / locks(锁可能正被占,留给
// service 层先 ForceReleaseLock 再 SoftDelete)。重复软删幂等(WHERE deleted_at IS NULL)。
func (r *gormRepository) SoftDeleteChannelDocument(ctx context.Context, id uint64, now time.Time) error {
	return r.db.WithContext(ctx).Model(&model.ChannelDocument{}).
		Where("id = ? AND deleted_at IS NULL", id).
		Update("deleted_at", now).Error
}

// ── ChannelDocumentVersion ──────────────────────────────────────────────────

func (r *gormRepository) CreateChannelDocumentVersion(ctx context.Context, v *model.ChannelDocumentVersion) error {
	return r.db.WithContext(ctx).Create(v).Error
}

func (r *gormRepository) FindChannelDocumentVersionByID(ctx context.Context, id uint64) (*model.ChannelDocumentVersion, error) {
	var v model.ChannelDocumentVersion
	if err := r.db.WithContext(ctx).First(&v, id).Error; err != nil {
		return nil, err
	}
	return &v, nil
}

func (r *gormRepository) ListChannelDocumentVersions(ctx context.Context, docID uint64) ([]model.ChannelDocumentVersion, error) {
	var vs []model.ChannelDocumentVersion
	if err := r.db.WithContext(ctx).
		Where("document_id = ?", docID).
		Order("id DESC").
		Find(&vs).Error; err != nil {
		return nil, err
	}
	return vs, nil
}

// FindChannelDocumentVersionByHash 用于 save 路径的"同 hash 已存在 → 返已有行"幂等查询。
func (r *gormRepository) FindChannelDocumentVersionByHash(ctx context.Context, docID uint64, version string) (*model.ChannelDocumentVersion, error) {
	var v model.ChannelDocumentVersion
	err := r.db.WithContext(ctx).
		Where("document_id = ? AND version = ?", docID, version).
		Take(&v).Error
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// ── ChannelDocumentLock ─────────────────────────────────────────────────────

// AcquireChannelDocumentLock 见 Repository 接口注释。
//
// 实现:两步原子(无事务,各自 SQL 自身原子)——
//
//  1. INSERT IGNORE → RowsAffected=1 表示无现有锁,抢到
//  2. RowsAffected=0 → 已有锁。条件 UPDATE WHERE expires_at<now OR locked_by=caller。
//     RowsAffected=1 表示过期或同人,续/抢成功;=0 表示别人持着未过期 → 不报错,
//     SELECT 当前持锁人返回。
//
// MySQL `NOW()` 用入参的 now 替代,便于测试注入时间。
func (r *gormRepository) AcquireChannelDocumentLock(
	ctx context.Context,
	docID, callerPrincipalID uint64,
	ttl time.Duration,
	now time.Time,
) (heldBy uint64, expiresAt time.Time, acquired bool, err error) {
	if docID == 0 || callerPrincipalID == 0 || ttl <= 0 {
		return 0, time.Time{}, false, errors.New("repository: invalid lock arguments")
	}
	newExpires := now.Add(ttl)

	// 1) INSERT IGNORE
	insertSQL := "INSERT IGNORE INTO channel_document_locks " +
		"(document_id, locked_by_principal_id, locked_at, expires_at) VALUES (?, ?, ?, ?)"
	res := r.db.WithContext(ctx).Exec(insertSQL, docID, callerPrincipalID, now, newExpires)
	if res.Error != nil {
		return 0, time.Time{}, false, res.Error
	}
	if res.RowsAffected == 1 {
		return callerPrincipalID, newExpires, true, nil
	}

	// 2) 已有锁:条件 UPDATE(过期或同人)
	updateSQL := "UPDATE channel_document_locks SET locked_by_principal_id = ?, locked_at = ?, expires_at = ? " +
		"WHERE document_id = ? AND (expires_at < ? OR locked_by_principal_id = ?)"
	res = r.db.WithContext(ctx).Exec(updateSQL, callerPrincipalID, now, newExpires, docID, now, callerPrincipalID)
	if res.Error != nil {
		return 0, time.Time{}, false, res.Error
	}
	if res.RowsAffected == 1 {
		return callerPrincipalID, newExpires, true, nil
	}

	// 3) 别人持着未过期:SELECT 返回当前状态
	var lock model.ChannelDocumentLock
	if err := r.db.WithContext(ctx).Where("document_id = ?", docID).Take(&lock).Error; err != nil {
		// 极小概率别人刚 release —— 再走一次抢比较繁琐,直接报"被别人持有"让 caller 重试
		return 0, time.Time{}, false, err
	}
	return lock.LockedByPrincipalID, lock.ExpiresAt, false, nil
}

func (r *gormRepository) ReleaseChannelDocumentLock(ctx context.Context, docID, callerPrincipalID uint64) (bool, error) {
	res := r.db.WithContext(ctx).
		Where("document_id = ? AND locked_by_principal_id = ?", docID, callerPrincipalID).
		Delete(&model.ChannelDocumentLock{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

func (r *gormRepository) ForceReleaseChannelDocumentLock(ctx context.Context, docID uint64) (bool, error) {
	res := r.db.WithContext(ctx).
		Where("document_id = ?", docID).
		Delete(&model.ChannelDocumentLock{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

func (r *gormRepository) FindChannelDocumentLock(ctx context.Context, docID uint64) (*model.ChannelDocumentLock, error) {
	var lock model.ChannelDocumentLock
	if err := r.db.WithContext(ctx).Where("document_id = ?", docID).Take(&lock).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &lock, nil
}

func (r *gormRepository) ListChannelDocumentLocksByDocIDs(ctx context.Context, docIDs []uint64) ([]model.ChannelDocumentLock, error) {
	if len(docIDs) == 0 {
		return nil, nil
	}
	var locks []model.ChannelDocumentLock
	if err := r.db.WithContext(ctx).
		Where("document_id IN ?", docIDs).
		Find(&locks).Error; err != nil {
		return nil, err
	}
	return locks, nil
}
