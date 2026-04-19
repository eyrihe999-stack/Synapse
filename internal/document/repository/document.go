// document.go Document 资源的 repository 实现。
package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/eyrihe999-stack/Synapse/internal/document"
	"github.com/eyrihe999-stack/Synapse/internal/document/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CreateDocument 插入文档记录,ID 由调用方(service)用 snowflake 预分配后填入。
// 单条 INSERT 本身原子,不需要外层 tx。
func (r *gormRepository) CreateDocument(ctx context.Context, doc *model.Document) error {
	if err := r.db.WithContext(ctx).Create(doc).Error; err != nil {
		return fmt.Errorf("create document: %w", err)
	}
	return nil
}

// FindDocumentByID 按 (org_id, id) 精确定位,找不到映射为 ErrDocumentNotFound。
// 带 org_id 过滤是为了防越权读。
func (r *gormRepository) FindDocumentByID(ctx context.Context, orgID, docID uint64) (*model.Document, error) {
	var doc model.Document
	err := r.db.WithContext(ctx).
		Where("id = ? AND org_id = ?", docID, orgID).
		First(&doc).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, document.ErrDocumentNotFound
		}
		return nil, fmt.Errorf("find document by id: %w", err)
	}
	return &doc, nil
}

// FindDocumentsByIDs 批量取文档快照。ids 里不存在 / 跨 org 的行静默丢弃。
// 单条 IN 查询,走主键索引,性能线性于 ids 长度。
func (r *gormRepository) FindDocumentsByIDs(ctx context.Context, orgID uint64, ids []uint64) ([]*model.Document, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var docs []*model.Document
	err := r.db.WithContext(ctx).
		Where("org_id = ? AND id IN ?", orgID, ids).
		Find(&docs).Error
	if err != nil {
		return nil, fmt.Errorf("find documents by ids: %w", err)
	}
	return docs, nil
}

// FindByContentHash 按 (org_id, content_hash) soft-lookup,找不到返回 (nil, nil)。
// 供 Upload 幂等路径用:"同 org 内已有同内容的文档就不必重复上传"。
// 走 idx_documents_org_hash 复合索引。理论上 (org_id, hash) 可能匹配多行(并发上传,无唯一约束),
// 返回最新一条(最高 id),让"再上传同内容 → 得到最近一次上传的那个 doc"。
func (r *gormRepository) FindByContentHash(ctx context.Context, orgID uint64, contentHash string) (*model.Document, error) {
	var doc model.Document
	err := r.db.WithContext(ctx).
		Where("org_id = ? AND content_hash = ?", orgID, contentHash).
		Order("id DESC").
		Limit(1).
		Take(&doc).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find document by content hash: %w", err)
	}
	return &doc, nil
}

// FindBySourceRef 见 Repository 接口注释。
//
// SQL 形态:MySQL 的 JSON 列用 `=` 做 canonical 等值比较(5.7+ 支持),不关心键顺序。
// 走 (org_id, source_type) 复合索引定位候选行,JSON 比较是过滤步骤。
// 同一 (org_id, source_type, source_ref) 组合不保证唯一(schema 未加 unique 约束,避免 MySQL JSON
// 唯一索引语法差异);真有并发 insert 极端情况,取 id DESC 最新一条,和 FindByContentHash 语义一致。
func (r *gormRepository) FindBySourceRef(ctx context.Context, orgID uint64, sourceType string, sourceRef []byte) (*model.Document, error) {
	if sourceType == "" || len(sourceRef) == 0 {
		return nil, nil
	}
	var doc model.Document
	err := r.db.WithContext(ctx).
		Where("org_id = ? AND source_type = ? AND source_ref = ?", orgID, sourceType, string(sourceRef)).
		Order("id DESC").
		Limit(1).
		Take(&doc).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find document by source_ref: %w", err)
	}
	return &doc, nil
}

// FindAllByFileName 返回同 org 下所有同名文档,按 updated_at DESC 排序(最新在前)。
// 找不到返回空切片 + nil 错误;不映射为 NotFound(precheck 用例里"无候选"是常规情况)。
//
// 走 idx_documents_org_filename 复合索引;同名并存允许,精确数视业务允许的最大候选数而定,
// 不做硬性上限(极端脏数据下也只是列表更长,前端截断显示即可)。
func (r *gormRepository) FindAllByFileName(ctx context.Context, orgID uint64, fileName string) ([]*model.Document, error) {
	var docs []*model.Document
	err := r.db.WithContext(ctx).
		Where("org_id = ? AND file_name = ?", orgID, fileName).
		Order("updated_at DESC").
		Find(&docs).Error
	if err != nil {
		return nil, fmt.Errorf("find all documents by file name: %w", err)
	}
	return docs, nil
}

// ListDocumentsByOrg 按 org 分页列出文档,默认按创建时间降序。
//
// **一致性要求:** Count 和 Find 必须在同一个事务里,基于同一 MVCC 快照,
// 否则并发 Insert 可能让"count=10 但第 2 页只看到 9 条"或"同一行被翻到两页"。
// MySQL 默认 REPEATABLE READ,tx 内的读操作使用 tx 建立时的快照,正是我们要的。
//
// query 非空时:
//   - 纯数字且可 parse 为 uint64 → WHERE id = ? 精确匹配(支持用户直接粘贴 snowflake ID 查找);
//   - 其它 → 对 title / file_name 做 LIKE '%q%' 模糊匹配,q 内的 LIKE 通配符会被转义,
//     避免用户输入 % / _ / \ 污染查询。
//
// 纯数字路径不兜底 LIKE:用户粘 ID 查找如果命不中,说明此 org 里真的没有这 ID,
// 退到 LIKE 会跨语义返回"标题里含这串数字"的无关文档,反而误导。
func (r *gormRepository) ListDocumentsByOrg(ctx context.Context, orgID uint64, query string, page, size int) ([]*model.Document, int64, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > document.MaxPageSize {
		size = document.DefaultPageSize
	}

	var (
		total int64
		out   []*model.Document
	)
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		scope := tx.Where("org_id = ?", orgID)
		if q := strings.TrimSpace(query); q != "" {
			if id, ok := tryParseDocID(q); ok {
				scope = scope.Where("id = ?", id)
			} else {
				pattern := "%" + escapeLikePattern(q) + "%"
				// 用显式 ESCAPE '\\' 防 sql_mode=NO_BACKSLASH_ESCAPES 下转义失效。
				scope = scope.Where("(title LIKE ? ESCAPE '\\\\' OR file_name LIKE ? ESCAPE '\\\\')", pattern, pattern)
			}
		}
		if err := scope.Session(&gorm.Session{}).
			Model(&model.Document{}).
			Count(&total).Error; err != nil {
			return fmt.Errorf("count documents: %w", err)
		}
		return scope.Session(&gorm.Session{}).
			Order("created_at DESC").
			Limit(size).
			Offset((page - 1) * size).
			Find(&out).Error
	})
	if err != nil {
		return nil, 0, fmt.Errorf("list documents: %w", err)
	}
	return out, total, nil
}

// escapeLikePattern 转义 LIKE 通配符,把用户输入还原成字面量。
// MySQL 默认 escape char 是 '\\',配合 "ESCAPE '\\'" 使用。
func escapeLikePattern(s string) string {
	return likePatternReplacer.Replace(s)
}

// tryParseDocID 判断 s 是否是一串纯数字 snowflake id(1..20 位)并能 parse 成 uint64。
// 允许前导 0 以外的任意数字串;空串 / 非数字 / 溢出 uint64 都返回 false。
// 不判断该 id 是否真实存在 —— 那是 SQL WHERE id=? 的事。
func tryParseDocID(s string) (uint64, bool) {
	if s == "" || len(s) > 20 {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	id, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

//nolint:gochecknoglobals // strings.NewReplacer 复用一份,避免每次查询重建。
var likePatternReplacer = strings.NewReplacer(
	`\`, `\\`,
	`%`, `\%`,
	`_`, `\_`,
)

// UpdateDocumentFieldsAtomic 在事务内 SELECT FOR UPDATE → 交给 buildUpdates 基于当前快照决策 → UPDATE → 重载返回。
//
// 消除 lost update:两个并发 PATCH 不会再用各自旧快照互相覆盖对方的改动,
// 后到的那个会在 FOR UPDATE 处阻塞,直到前一个 tx 释放行锁才继续,届时它读到的是最新状态。
//
// 失败场景:
//   - 行不存在或不属于此 org → ErrDocumentNotFound(锁 FOR UPDATE 在 WHERE id=? AND org_id=? 上命中不到)。
//   - buildUpdates 返回 error → 直接透传(service 层用这个通道抛 ErrDocumentTitleInvalid 等语义错)。
//   - DB 错误 → wrap 原始错误。
//
// buildUpdates 返回空 map + nil err 视为 no-op,不触发 UPDATE 语句。
func (r *gormRepository) UpdateDocumentFieldsAtomic(
	ctx context.Context,
	orgID, docID uint64,
	buildUpdates func(current *model.Document) (map[string]any, error),
) (*model.Document, error) {
	var result model.Document
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var doc model.Document
		// FOR UPDATE: 锁行阻塞并发 PATCH, 消除 lost update。
		//sayso-lint:ignore err-shadow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND org_id = ?", docID, orgID).
			First(&doc).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return document.ErrDocumentNotFound
			}
			return fmt.Errorf("lock document: %w", err)
		}

		updates, err := buildUpdates(&doc)
		if err != nil {
			//sayso-lint:ignore sentinel-wrap
			return err
		}
		if len(updates) > 0 {
			if err := tx.Model(&doc).Where("id = ?", doc.ID).Updates(updates).Error; err != nil {
				return fmt.Errorf("update fields: %w", err)
			}
			// 重载拿到 updated_at 等由 DB 自动填的字段的最新值。
			if err := tx.Where("id = ?", docID).First(&doc).Error; err != nil {
				return fmt.Errorf("reload after update: %w", err)
			}
		}
		result = doc
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// OverwriteDocumentContent 覆盖更新:tx 内 SELECT FOR UPDATE 锁行 → 调 sideEffects → UPDATE newFields → COMMIT。
//
// 顺序理由:
//  1. 先拿行锁,确保 sideEffects 执行期间这一行不会被 Update/Delete 改动或撤销。
//  2. sideEffects 在锁里做 OSS PUT + PG chunk swap —— 持锁阻塞同行的并发写,不阻塞其他行。
//  3. sideEffects 返回 nil → 应用 newFields → COMMIT,metadata 变更和 OSS/PG 变更"对外同时可见"。
//  4. sideEffects 返回 error → ROLLBACK metadata;**但 OSS 的 PUT 已经覆盖、PG 的 chunks 已经替换,这些副作用不会回滚**。
//     后续重试同内容会走幂等路径修复最终一致性;本方法只保证"metadata 要么更新成功要么完全不变"。
//
// 失败场景:
//   - 行不存在/不属 org → ErrDocumentNotFound。
//   - sideEffects 返回 error → 透传(已是带 sentinel 的 wrap)。
//   - DB 错误 → wrap 原始错误。
//
// newFields 空 map 视为 no-op UPDATE(仅走 sideEffects + 重载返回)。
func (r *gormRepository) OverwriteDocumentContent(
	ctx context.Context,
	orgID, docID uint64,
	sideEffects func(current *model.Document) error,
	newFields map[string]any,
) (*model.Document, error) {
	var result model.Document
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var doc model.Document
		// FOR UPDATE: 锁行期间执行 sideEffects(OSS+PG), 同行并发写被阻塞到 COMMIT。
		//sayso-lint:ignore err-shadow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND org_id = ?", docID, orgID).
			First(&doc).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return document.ErrDocumentNotFound
			}
			return fmt.Errorf("lock document: %w", err)
		}

		if err := sideEffects(&doc); err != nil {
			//sayso-lint:ignore sentinel-wrap
			return err
		}

		if len(newFields) > 0 {
			if err := tx.Model(&doc).Where("id = ?", doc.ID).Updates(newFields).Error; err != nil {
				return fmt.Errorf("update overwrite fields: %w", err)
			}
			if err := tx.Where("id = ?", docID).First(&doc).Error; err != nil {
				return fmt.Errorf("reload after overwrite: %w", err)
			}
		}
		result = doc
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// DeleteDocumentAtomic 在事务内 SELECT FOR UPDATE → DELETE,返回已删除行的快照。
//
// 返回已删除行是为了让 service 能拿到 oss_key 做下游 OSS / pgvector chunks 的清理。
// FOR UPDATE 确保并发 Delete 不会双跑,也让同时进行的 UpdateMetadata 在 DELETE 落盘后读到 NotFound。
//
// 失败场景:
//   - 行不存在或不属于此 org → ErrDocumentNotFound。
//   - DB 错误 → wrap 原始错误。
func (r *gormRepository) DeleteDocumentAtomic(ctx context.Context, orgID, docID uint64) (*model.Document, error) {
	var deleted model.Document
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var doc model.Document
		// FOR UPDATE: 串行化并发 Delete, 阻塞同时 UpdateMetadata 的读,确保删除后读到 NotFound。
		//sayso-lint:ignore err-shadow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND org_id = ?", docID, orgID).
			First(&doc).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return document.ErrDocumentNotFound
			}
			return fmt.Errorf("lock document: %w", err)
		}
		if err := tx.Where("id = ?", doc.ID).Delete(&doc).Error; err != nil {
			return fmt.Errorf("delete document: %w", err)
		}
		deleted = doc
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &deleted, nil
}

// DeleteDocumentByID 按 PK 硬删,不做 org 校验。
//
// **只供 Upload 补偿路径用**:service 层刚用 snowflake 预分配 ID 插了行,
// 下游(OSS/PG chunks)失败后回滚自己的插入。因为调用方手里就握着这个 id,跳 org 校验是安全的。
// 常规删除请走 DeleteDocumentAtomic。
func (r *gormRepository) DeleteDocumentByID(ctx context.Context, docID uint64) error {
	if err := r.db.WithContext(ctx).
		Where("id = ?", docID).
		Delete(&model.Document{}).Error; err != nil {
		return fmt.Errorf("delete document by id: %w", err)
	}
	return nil
}
