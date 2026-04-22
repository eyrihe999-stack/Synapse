// source.go Repository 接口中 Source 资源的实现。
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/permission/audit"
	"github.com/eyrihe999-stack/Synapse/internal/source/model"
	"gorm.io/gorm"
)

// CreateSource 创建一条 source 记录,同事务写一条 source.create audit。
func (r *gormRepository) CreateSource(ctx context.Context, src *model.Source) error {
	if src.CreatedAt.IsZero() {
		src.CreatedAt = time.Now().UTC()
	}
	if src.UpdatedAt.IsZero() {
		src.UpdatedAt = src.CreatedAt
	}
	if src.Visibility == "" {
		src.Visibility = model.VisibilityOrg
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(src).Error; err != nil {
			return fmt.Errorf("create source: %w", err)
		}
		return audit.Write(ctx, tx, src.OrgID,
			model.AuditActionSourceCreate, model.AuditTargetSource, src.ID,
			nil, sourceSnapshot(src), nil,
		)
	})
}

// FindSourceByID 按主键查找。
func (r *gormRepository) FindSourceByID(ctx context.Context, id uint64) (*model.Source, error) {
	var s model.Source
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// FindSourceByOwnerAndName 在某 (org, owner) 下按 name 精确查找。
func (r *gormRepository) FindSourceByOwnerAndName(ctx context.Context, orgID, ownerUserID uint64, name string) (*model.Source, error) {
	var s model.Source
	err := r.db.WithContext(ctx).
		Where("org_id = ? AND owner_user_id = ? AND name = ?", orgID, ownerUserID, name).
		First(&s).Error
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// FindManualUploadSource 在某 org 内按 (kind=manual_upload, owner_user_id, external_ref='') 精确查找。
func (r *gormRepository) FindManualUploadSource(ctx context.Context, orgID, ownerUserID uint64) (*model.Source, error) {
	var s model.Source
	err := r.db.WithContext(ctx).
		Where("org_id = ? AND kind = ? AND owner_user_id = ? AND external_ref = ?",
			orgID, model.KindManualUpload, ownerUserID, "").
		First(&s).Error
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// EnsureManualUploadSource 幂等确保 (org, user) 的 manual_upload source 存在。
//
// 实现策略:
//  1. 先 SELECT,命中即返回(created=false)
//  2. 未命中 → 在 tx 内 INSERT + 写 audit;唯一约束冲突(并发场景另一请求先创建)→ 回读返回
//
// 返回 (source, created, err);created=true 表示本次调用真正创建了一条新行。
func (r *gormRepository) EnsureManualUploadSource(ctx context.Context, orgID, ownerUserID uint64) (*model.Source, bool, error) {
	// 快路径:先查
	if existing, err := r.FindManualUploadSource(ctx, orgID, ownerUserID); err == nil {
		return existing, false, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, fmt.Errorf("find manual_upload source: %w", err)
	}

	// 慢路径:不存在,创建。失败时(并发冲突)回读。
	src := &model.Source{
		OrgID:       orgID,
		Kind:        model.KindManualUpload,
		OwnerUserID: ownerUserID,
		ExternalRef: "",
		Name:        model.DefaultManualUploadName,
		Visibility:  model.VisibilityOrg,
	}
	createErr := r.CreateSource(ctx, src)
	if createErr == nil {
		return src, true, nil
	}
	// Create 失败 → 可能是唯一约束冲突(并发),尝试回读
	existing, findErr := r.FindManualUploadSource(ctx, orgID, ownerUserID)
	if findErr == nil {
		return existing, false, nil
	}
	// 真的失败
	return nil, false, fmt.Errorf("ensure manual_upload source: create=%v, refind=%v", createErr, findErr)
}

// UpdateSourceVisibility 更新 visibility,同事务写 source.visibility_change audit。
//
// 若 newVisibility == 当前值则 no-op,不写 audit。
func (r *gormRepository) UpdateSourceVisibility(ctx context.Context, sourceID uint64, newVisibility string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before model.Source
		if err := tx.Where("id = ?", sourceID).First(&before).Error; err != nil {
			return err
		}
		if before.Visibility == newVisibility {
			return nil
		}
		afterSnap := before
		afterSnap.Visibility = newVisibility
		afterSnap.UpdatedAt = time.Now().UTC()

		if err := tx.Model(&model.Source{}).
			Where("id = ?", sourceID).
			Updates(map[string]any{
				"visibility": newVisibility,
				"updated_at": afterSnap.UpdatedAt,
			}).Error; err != nil {
			return fmt.Errorf("update source visibility: %w", err)
		}
		return audit.Write(ctx, tx, before.OrgID,
			model.AuditActionSourceVisibilityChange, model.AuditTargetSource, before.ID,
			sourceSnapshot(&before), sourceSnapshot(&afterSnap),
			map[string]any{
				"old_visibility": before.Visibility,
				"new_visibility": newVisibility,
			},
		)
	})
}

// DeleteSource 按主键删 source,同事务写 source.delete audit(before=snapshot, after=null)。
// 不存在时返回 gorm.ErrRecordNotFound,service 层翻译为 ErrSourceNotFound。
// 本方法只删 source 本体,不处理 resource_acl / permission_audit_log 之外的关联表 ——
// doc 的 knowledge_source_id 由 service 层在前置守卫里校验(CountBySource==0 才放行)。
func (r *gormRepository) DeleteSource(ctx context.Context, sourceID uint64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before model.Source
		if err := tx.Where("id = ?", sourceID).First(&before).Error; err != nil {
			return err
		}
		res := tx.Where("id = ?", sourceID).Delete(&model.Source{})
		if res.Error != nil {
			return fmt.Errorf("delete source: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return audit.Write(ctx, tx, before.OrgID,
			model.AuditActionSourceDelete, model.AuditTargetSource, before.ID,
			sourceSnapshot(&before), nil, nil,
		)
	})
}

// ListSourcesByOrg 分页列出某 org 的 source(按 created_at DESC)。
//
// idsFilter 控制 ACL 可见性过滤:
//   - nil:不过滤(scope=all)
//   - 空 slice:短路返空(用户看不到任何 source)
//   - 非空:WHERE id IN (...)
func (r *gormRepository) ListSourcesByOrg(ctx context.Context, orgID uint64, kindFilter string, idsFilter []uint64, page, size int) ([]*model.Source, int64, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	// ACL 短路:可见集合为空 → 直接返空
	if idsFilter != nil && len(idsFilter) == 0 {
		return nil, 0, nil
	}
	var (
		items []*model.Source
		total int64
	)
	q := r.db.WithContext(ctx).Model(&model.Source{}).Where("org_id = ?", orgID)
	if kindFilter != "" {
		q = q.Where("kind = ?", kindFilter)
	}
	if idsFilter != nil {
		q = q.Where("id IN ?", idsFilter)
	}
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count sources: %w", err)
	}
	if err := q.Order("created_at DESC").
		Offset((page - 1) * size).
		Limit(size).
		Find(&items).Error; err != nil {
		return nil, 0, fmt.Errorf("list sources: %w", err)
	}
	return items, total, nil
}

// ListSourcesByOwner 列出某 user 在某 org 中作为 owner 的所有 source。
func (r *gormRepository) ListSourcesByOwner(ctx context.Context, orgID, ownerUserID uint64) ([]*model.Source, error) {
	var items []*model.Source
	err := r.db.WithContext(ctx).
		Where("org_id = ? AND owner_user_id = ?", orgID, ownerUserID).
		Order("created_at DESC").
		Find(&items).Error
	if err != nil {
		return nil, fmt.Errorf("list sources by owner: %w", err)
	}
	return items, nil
}

// ListSourceIDsByOwner 只取 id,permission 判定用。
func (r *gormRepository) ListSourceIDsByOwner(ctx context.Context, orgID, ownerUserID uint64) ([]uint64, error) {
	var ids []uint64
	err := r.db.WithContext(ctx).
		Table("sources").
		Select("id").
		Where("org_id = ? AND owner_user_id = ?", orgID, ownerUserID).
		Scan(&ids).Error
	if err != nil {
		return nil, fmt.Errorf("list source ids by owner: %w", err)
	}
	return ids, nil
}

// ListSourceIDsByVisibility 只取 id,permission 判定用("全 org 可见"过滤)。
func (r *gormRepository) ListSourceIDsByVisibility(ctx context.Context, orgID uint64, visibility string) ([]uint64, error) {
	var ids []uint64
	err := r.db.WithContext(ctx).
		Table("sources").
		Select("id").
		Where("org_id = ? AND visibility = ?", orgID, visibility).
		Scan(&ids).Error
	if err != nil {
		return nil, fmt.Errorf("list source ids by visibility: %w", err)
	}
	return ids, nil
}

// ─── snapshot 辅助 ────────────────────────────────────────────────────────────

// sourceSnapshot 把 Source 转为 audit 用的快照。
func sourceSnapshot(s *model.Source) map[string]any {
	return map[string]any{
		"id":            s.ID,
		"org_id":        s.OrgID,
		"kind":          s.Kind,
		"owner_user_id": s.OwnerUserID,
		"external_ref":  s.ExternalRef,
		"name":          s.Name,
		"visibility":    s.Visibility,
		"created_at":    s.CreatedAt.Unix(),
		"updated_at":    s.UpdatedAt.Unix(),
	}
}
