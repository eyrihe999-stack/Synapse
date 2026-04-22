// document.go documents 表的读写实现。
package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/eyrihe999-stack/Synapse/internal/document"
	"github.com/eyrihe999-stack/Synapse/internal/document/model"
	"gorm.io/gorm"
)

// GetVersion fetcher 增量判定的轻量查询:只 SELECT id + version。
func (r *gormRepository) GetVersion(
	ctx context.Context,
	orgID uint64,
	sourceType, sourceID string,
) (VersionInfo, error) {
	var row struct {
		ID      uint64
		Version string
	}
	err := r.db.WithContext(ctx).
		Table(document.TableDocuments).
		Select("id, version").
		Where("org_id = ? AND source_type = ? AND source_id = ?", orgID, sourceType, sourceID).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return VersionInfo{Exists: false}, nil
	}
	if err != nil {
		return VersionInfo{}, fmt.Errorf("get version: %w: %w", err, document.ErrDocumentInternal)
	}
	return VersionInfo{DocID: row.ID, Version: row.Version, Exists: true}, nil
}

// GetByID 查单条 doc 元数据。
func (r *gormRepository) GetByID(ctx context.Context, orgID, docID uint64) (*model.Document, error) {
	var doc model.Document
	err := r.db.WithContext(ctx).
		Where("org_id = ? AND id = ?", orgID, docID).
		Take(&doc).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, document.ErrDocumentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get by id: %w: %w", err, document.ErrDocumentInternal)
	}
	return &doc, nil
}

// ListByOrg keyset 分页列 doc 元数据。
//
// M3:opts.KnowledgeSourceIDs 控制 ACL 过滤
//   - nil:不过滤(老调用者 / 单测)
//   - 空 slice:直接返 nil(用户在该 org 看不到任何 source)
//   - 非空:WHERE knowledge_source_id IN (...)
func (r *gormRepository) ListByOrg(ctx context.Context, orgID uint64, opts ListOptions) ([]*model.Document, error) {
	const defaultLimit, maxLimit = 20, 100
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	// ACL 短路:用户没有任何可见 source → 直接返空
	if opts.KnowledgeSourceIDs != nil && len(opts.KnowledgeSourceIDs) == 0 {
		return nil, nil
	}

	q := r.db.WithContext(ctx).Where("org_id = ?", orgID)
	if opts.KnowledgeSourceIDs != nil {
		q = q.Where("knowledge_source_id IN ?", opts.KnowledgeSourceIDs)
	}
	if opts.DocID > 0 {
		q = q.Where("id = ?", opts.DocID)
	}
	if opts.KnowledgeSourceID > 0 {
		q = q.Where("knowledge_source_id = ?", opts.KnowledgeSourceID)
	}
	if opts.Provider != "" {
		q = q.Where("provider = ?", opts.Provider)
	}
	if qs := strings.TrimSpace(opts.Query); qs != "" {
		// title / file_name 都可能包含中文大写,统一 LOWER 再 LIKE;LIKE 通配符手工转义
		// 避免用户输入里的 % / _ 命中意外匹配。量级小(单 org 文档 k 级),无需 trigram 索引。
		like := "%" + escapeLike(strings.ToLower(qs)) + "%"
		q = q.Where("LOWER(title) LIKE ? ESCAPE '\\' OR LOWER(file_name) LIKE ? ESCAPE '\\'", like, like)
	}
	if opts.BeforeID > 0 {
		q = q.Where("id < ?", opts.BeforeID)
	}

	var docs []*model.Document
	if err := q.Order("id DESC").Limit(limit).Find(&docs).Error; err != nil {
		return nil, fmt.Errorf("list by org: %w: %w", err, document.ErrDocumentInternal)
	}
	return docs, nil
}

// escapeLike 将用户输入里的 LIKE 通配符 % / _ / \ 转义,防止"搜 10%"匹配所有文档。
// 搭配 SQL 的 ESCAPE '\' 子句生效。
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// CountBySource 统计某 source 下属的 doc 数量(按 knowledge_source_id 精确匹配)。
// 给 source 模块删除源的前置守卫用:只有 count==0 时源才可被 owner 删除。
func (r *gormRepository) CountBySource(ctx context.Context, orgID, sourceID uint64) (int64, error) {
	var cnt int64
	if err := r.db.WithContext(ctx).
		Model(&model.Document{}).
		Where("org_id = ? AND knowledge_source_id = ?", orgID, sourceID).
		Count(&cnt).Error; err != nil {
		return 0, fmt.Errorf("count by source: %w: %w", err, document.ErrDocumentInternal)
	}
	return cnt, nil
}

// CountChunks 按 index_status 分组统计。单张 doc 的 chunks 通常百级别,一次聚合够用。
func (r *gormRepository) CountChunks(ctx context.Context, docID uint64) (indexed, failed int64, err error) {
	type row struct {
		Status string
		Cnt    int64
	}
	var rows []row
	err = r.db.WithContext(ctx).
		Table(document.TableDocumentChunks).
		Select("index_status AS status, COUNT(*) AS cnt").
		Where("doc_id = ?", docID).
		Group("index_status").
		Scan(&rows).Error
	if err != nil {
		return 0, 0, fmt.Errorf("count chunks: %w: %w", err, document.ErrDocumentInternal)
	}
	for _, r := range rows {
		switch r.Status {
		case document.ChunkIndexStatusIndexed:
			indexed = r.Cnt
		case document.ChunkIndexStatusFailed:
			failed = r.Cnt
		}
	}
	return indexed, failed, nil
}

// DeleteByID 按主键删,CASCADE 连带 chunks。不存在视为幂等。
func (r *gormRepository) DeleteByID(ctx context.Context, orgID, docID uint64) error {
	if err := r.db.WithContext(ctx).
		Where("org_id = ? AND id = ?", orgID, docID).
		Delete(&model.Document{}).Error; err != nil {
		return fmt.Errorf("delete by id: %w: %w", err, document.ErrDocumentInternal)
	}
	return nil
}

// DeleteBySourceID 按源端幂等键删,fetcher tombstone 用。
func (r *gormRepository) DeleteBySourceID(
	ctx context.Context,
	orgID uint64,
	sourceType, sourceID string,
) error {
	if err := r.db.WithContext(ctx).
		Where("org_id = ? AND source_type = ? AND source_id = ?", orgID, sourceType, sourceID).
		Delete(&model.Document{}).Error; err != nil {
		return fmt.Errorf("delete by source id: %w: %w", err, document.ErrDocumentInternal)
	}
	return nil
}
