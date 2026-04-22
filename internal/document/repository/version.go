// version.go document_versions 表的读写实现。
//
// 用法:handler 在 OSS PutObject 成功后 InsertVersion,再 PruneOldVersions(cfg.MaxVersionsPerDocument),
// 拿到返回的 oss_key 列表去 OSS DeleteObject。
package repository

import (
	"context"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/document"
	"github.com/eyrihe999-stack/Synapse/internal/document/model"
)

// InsertVersion 插入一行版本记录。id 由调用方预分配(snowflake)。
func (r *gormRepository) InsertVersion(ctx context.Context, v *model.DocumentVersion) error {
	if err := r.db.WithContext(ctx).Create(v).Error; err != nil {
		return fmt.Errorf("insert version: %w: %w", err, document.ErrDocumentInternal)
	}
	return nil
}

// ListVersionsByDoc 按 created_at DESC 列所有版本。单 doc 最多 MaxVersionsPerDocument 条,量级很小。
func (r *gormRepository) ListVersionsByDoc(ctx context.Context, docID uint64) ([]*model.DocumentVersion, error) {
	var versions []*model.DocumentVersion
	err := r.db.WithContext(ctx).
		Where("doc_id = ?", docID).
		Order("created_at DESC").
		Find(&versions).Error
	if err != nil {
		return nil, fmt.Errorf("list versions by doc: %w: %w", err, document.ErrDocumentInternal)
	}
	return versions, nil
}

// PruneOldVersions 保留最近 keep 条,删超出的最老条,返被删的 oss_key。
//
// 实现用子查询删:DELETE ... WHERE id IN (SELECT id FROM ... ORDER BY created_at DESC OFFSET keep)。
// PG 的 DELETE ... RETURNING 拿到被删行的 oss_key;事务由 gorm 自动包裹。
func (r *gormRepository) PruneOldVersions(ctx context.Context, docID uint64, keep int) ([]string, error) {
	if keep <= 0 {
		return nil, nil
	}
	type row struct {
		OSSKey string
	}
	var rows []row
	// 先查要删的 id + oss_key(超过 keep 的那些)。然后按 id 批量 DELETE。
	// 不用 DELETE ... WHERE id IN (subquery) RETURNING:gorm 对 RETURNING 支持参差,手工两步稳。
	err := r.db.WithContext(ctx).
		Table(document.TableDocumentVersions).
		Select("oss_key").
		Where("doc_id = ?", docID).
		Order("created_at DESC").
		Offset(keep).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("prune versions (select): %w: %w", err, document.ErrDocumentInternal)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(rows))
	for _, v := range rows {
		keys = append(keys, v.OSSKey)
	}
	// 按 oss_key 删(唯一性由 snowflake id 给,但这里按 oss_key 更直观);
	// 若有同 oss_key 历史撞车(理论不会),多删一行也无害——版本号码 sha256 本身保证唯一。
	if err := r.db.WithContext(ctx).
		Where("doc_id = ? AND oss_key IN ?", docID, keys).
		Delete(&model.DocumentVersion{}).Error; err != nil {
		return nil, fmt.Errorf("prune versions (delete): %w: %w", err, document.ErrDocumentInternal)
	}
	return keys, nil
}
