// chunk.go UpsertWithChunks 的实现:
//
// 一个事务内完成 doc upsert + 旧 chunks 清 + 新 chunks 批量插入。
// 向量列走 Raw SQL + "[v1,v2,...]::vector" 字面量,gorm struct 不参与 embedding 列。
package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/eyrihe999-stack/Synapse/internal/document"
	"github.com/eyrihe999-stack/Synapse/internal/document/model"
	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UpsertWithChunks 见接口注释。
//
// 实现分三步,全部在同一 TX:
//
//  1. documents INSERT ... ON CONFLICT (org_id, source_type, source_id) DO UPDATE SET ...
//     —— 用 gorm clause.OnConflict 自动处理
//  2. DELETE FROM document_chunks WHERE doc_id = <id>
//  3. 批量 INSERT document_chunks(手拼 SQL,embedding 列走 "[...]::vector" 或 NULL)
func (r *gormRepository) UpsertWithChunks(
	ctx context.Context,
	doc *model.Document,
	chunks []ChunkWithVec,
) (uint64, error) {
	if doc == nil {
		return 0, fmt.Errorf("document: upsert: nil doc: %w", document.ErrDocumentInternal)
	}
	if doc.ID == 0 {
		return 0, fmt.Errorf("document: upsert: doc.ID is zero (caller must pre-assign): %w", document.ErrDocumentInternal)
	}

	// chunk_count / content_byte_size 由调用方填;这里只兜底确保和实际 chunks 数量对齐。
	if doc.ChunkCount == 0 && len(chunks) > 0 {
		doc.ChunkCount = len(chunks)
	}
	// ACLGroupIDs nil → 空数组(列 NOT NULL;nil Value() 返 NULL 会导致 INSERT 失败)。
	if doc.ACLGroupIDs == nil {
		doc.ACLGroupIDs = pq.Int64Array{}
	}

	var finalID uint64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// ─── step 1: documents upsert ──────────────────────────────────
		//
		// 不能用 `INSERT ... ON CONFLICT (org_id,source_type,source_id) DO UPDATE`:
		// PG 的 ON CONFLICT 只能指定一个 target,若 caller 传入的 id 撞 PK(覆盖场景),
		// 会以 PK 冲突形式抛错,不会走 DO UPDATE 分支。
		//
		// 所以先按源端幂等键查老行 id,再决定 INSERT 还是 UPDATE。
		var existingID uint64
		// FOR UPDATE: 锁住候选行,避免本事务在 query→insert/update 之间被并发 upsert 撞车。
		//sayso-lint:ignore err-shadow
		err := tx.Table(document.TableDocuments).
			Select("id").
			// 锁住候选行,防止并发 upsert 在 query→insert/update 之间撞车
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("org_id = ? AND source_type = ? AND source_id = ?",
				doc.OrgID, doc.SourceType, doc.SourceID).
			Take(&existingID).Error
		switch {
		case err == nil:
			// 老行存在 → UPDATE。必须强制把 doc.ID 对齐成老行 id(chunks FK 用)
			doc.ID = existingID
			updates := map[string]any{
				"provider":           doc.Provider,
				"title":              doc.Title,
				"mime_type":          doc.MIMEType,
				"file_name":          doc.FileName,
				"version":            doc.Version,
				"external_ref_kind":  doc.ExternalRefKind,
				"external_ref_uri":   doc.ExternalRefURI,
				"external_ref_extra": doc.ExternalRefExtra,
				"uploader_id":        doc.UploaderID,
				"acl_group_ids":      doc.ACLGroupIDs,
				"chunk_count":        doc.ChunkCount,
				"content_byte_size":  doc.ContentByteSize,
				"last_synced_at":     doc.LastSyncedAt,
				"updated_at":         gorm.Expr("now()"),
			}
			if err := tx.Table(document.TableDocuments).
				Where("id = ?", existingID).
				Updates(updates).Error; err != nil {
				return fmt.Errorf("update documents: %w", err)
			}
		case errors.Is(err, gorm.ErrRecordNotFound):
			// 无老行 → INSERT。要求调用方传入有效 doc.ID(persister 会负责)
			if doc.ID == 0 {
				return fmt.Errorf("insert documents: doc.ID is zero and no existing row found")
			}
			if err := tx.Create(doc).Error; err != nil {
				return fmt.Errorf("insert documents: %w", err)
			}
		default:
			return fmt.Errorf("lookup existing: %w", err)
		}
		finalID = doc.ID

		// ─── step 2: 清旧 chunks ──────────────────────────────────────
		if err := tx.Exec(
			`DELETE FROM `+document.TableDocumentChunks+` WHERE doc_id = ?`, finalID,
		).Error; err != nil {
			return fmt.Errorf("delete old chunks: %w", err)
		}

		// ─── step 3: 批量插入新 chunks ─────────────────────────────────
		if err := insertChunks(tx, finalID, chunks); err != nil {
			return fmt.Errorf("insert chunks: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("upsert with chunks: %w: %w", err, document.ErrDocumentInternal)
	}
	return finalID, nil
}

// insertChunks 批量 INSERT。embedding 列字面量自己拼,其它字段走参数占位符。
//
// 为什么不用 gorm CreateInBatches:pgvector 的 VECTOR(N) 列无法经由 gorm 结构字段正确序列化,
// 必须 Raw SQL 显式 "[v1,v2,...]::vector"。
func insertChunks(tx *gorm.DB, docID uint64, chunks []ChunkWithVec) error {
	if len(chunks) == 0 {
		return nil
	}

	// 列顺序(和 VALUES placeholders 对齐):
	// id, doc_id, org_id, chunk_idx, content, content_type, level,
	// heading_path, token_count, embedding, chunker_version, parent_chunk_id,
	// index_status, index_error, metadata
	const columns = `id, doc_id, org_id, chunk_idx, content, content_type, level,
		heading_path, token_count, embedding, chunker_version, parent_chunk_id,
		index_status, index_error, metadata`

	// 按批拼大 INSERT。单 batch 控制在 100 行,避免 PG 参数上限(65535)超限:
	// 14 字段 × 100 = 1400 占位符,安全。
	const batchSize = 100
	for start := 0; start < len(chunks); start += batchSize {
		end := min(start+batchSize, len(chunks))
		batch := chunks[start:end]
		if err := insertChunkBatch(tx, columns, docID, batch); err != nil {
			return err
		}
	}
	return nil
}

func insertChunkBatch(tx *gorm.DB, columns string, docID uint64, batch []ChunkWithVec) error {
	var (
		values []string
		args   []any
	)
	values = make([]string, 0, len(batch))
	args = make([]any, 0, len(batch)*14)

	for _, cv := range batch {
		c := cv.Chunk
		// 强制对齐:即便调用方忘了塞 DocID,这里补上。
		c.DocID = docID

		// 索引状态兜底:nil Vec → failed;非 nil → indexed。
		// 如果调用方已经设过 IndexStatus 就尊重调用方(例如将来加 "retrying" 中间态)。
		if c.IndexStatus == "" {
			if cv.Vec == nil {
				c.IndexStatus = document.ChunkIndexStatusFailed
			} else {
				c.IndexStatus = document.ChunkIndexStatusIndexed
			}
		}

		// embedding 占位:有向量 → "[...]::vector",无 → NULL。
		embSQL := "NULL"
		if cv.Vec != nil {
			embSQL = "'" + formatVectorLiteral(cv.Vec) + "'::vector"
		}

		// metadata 可空
		var meta any
		if len(c.Metadata) > 0 {
			meta = c.Metadata
		}

		// heading_path: pq.StringArray 实现了 Valuer;nil → NULL,必须兜底成空 slice。
		headingPath := c.HeadingPath
		if headingPath == nil {
			headingPath = pq.StringArray{}
		}

		row := fmt.Sprintf(
			`(?, ?, ?, ?, ?, ?, ?, ?, ?, %s, ?, ?, ?, ?, ?)`, embSQL,
		)
		values = append(values, row)
		args = append(args,
			c.ID,
			c.DocID,
			c.OrgID,
			c.ChunkIdx,
			c.Content,
			nonEmpty(c.ContentType, "text"),
			c.Level,
			headingPath,
			c.TokenCount,
			// embedding inlined in row template
			c.ChunkerVersion,
			c.ParentChunkID,
			c.IndexStatus,
			c.IndexError,
			meta,
		)
	}

	sql := "INSERT INTO " + document.TableDocumentChunks +
		" (" + columns + ") VALUES " + strings.Join(values, ", ")
	if err := tx.Exec(sql, args...).Error; err != nil {
		return err
	}
	return nil
}

// ListChunksByDocOrdered 按 chunk_idx ASC 拉某 doc 的全部 chunks(不读 embedding 列)。
//
// 用途:agent 通过 MCP 拉 KB 文档全文 —— 把 chunks 按顺序拼回原文。
//
// 不读 embedding 列(显式 Select 列)的两个理由:
//   1. struct 上 Embedding 标了 gorm:"-" 不会反序列化,但 GORM 默认 SELECT * 仍会
//      把 vector 列拉出来再丢弃,白浪费带宽 + pg-vector 长字符串解析
//   2. 让本方法对 retrieval-style scan 友好(后续 retrieval 单独走自己的 raw SQL)
//
// 不返 ErrDocumentNotFound:doc 元数据校验由调用方先行(GetByID),这里只看 doc_id。
// chunks 为空表示文档未分块或 ingestion 未完成。
func (r *gormRepository) ListChunksByDocOrdered(ctx context.Context, orgID, docID uint64) ([]model.DocumentChunk, error) {
	var rows []model.DocumentChunk
	err := r.db.WithContext(ctx).
		Select("id, doc_id, org_id, chunk_idx, content, content_type, level, " +
			"heading_path, token_count, chunker_version, parent_chunk_id, " +
			"index_status, index_error, metadata, created_at").
		Where("org_id = ? AND doc_id = ?", orgID, docID).
		Order("chunk_idx ASC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list chunks by doc: %w: %w", err, document.ErrDocumentInternal)
	}
	return rows, nil
}

// formatVectorLiteral 把 []float32 转成 pgvector 字面量 "[v1,v2,...]".
// 用 strconv.FormatFloat + 'f'/'g' 保留精度,避免科学记数法在某些 pgvector 版本上的解析差异。
func formatVectorLiteral(v []float32) string {
	var b strings.Builder
	b.Grow(len(v) * 10)
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
