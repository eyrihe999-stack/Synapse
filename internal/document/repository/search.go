// search.go SearchByEmbedding 的实现。
//
// 走 pgvector HNSW partial index(在 migration 里建好):
//
//	CREATE INDEX idx_document_chunks_hnsw
//	  ON document_chunks USING hnsw (embedding vector_cosine_ops)
//	  WITH (m=16, ef_construction=64)
//	  WHERE index_status = 'indexed'
//
// 为了让 planner 真用上索引,采用 "oversample → filter" 两阶段查询:
//
//	stage 1  纯按 HNSW 距离取 oversample 个候选(只有 org_id + index_status 过滤,
//	         单表无 JOIN,planner 容易选 HNSW)
//	stage 2  和 documents JOIN,按 channel 可见集(source ∪ doc)过滤,按距离重排截断 top-K
//
// 单表带 JOIN + 多 WHERE 的写法 PG planner 可能退化成顺序扫描,故拆分。
package repository

import (
	"context"
	"fmt"

	"github.com/lib/pq"

	"github.com/eyrihe999-stack/Synapse/internal/document"
)

// ChunkSearchHit 单条命中。Distance 是 cosine 距离(0 = 一致,2 = 反向)。
//
// 字段语义:
//   - Chunk* 来自 document_chunks
//   - Doc* 来自 documents JOIN(给 LLM 显示用,不让调用方再回 docrepo 拼)
type ChunkSearchHit struct {
	ChunkID     uint64
	DocID       uint64
	OrgID       uint64
	ChunkIdx    int
	Content     string
	HeadingPath []string
	Distance    float32

	DocTitle    string
	DocFileName string
	DocMIMEType string
	DocSourceID uint64 // knowledge_source_id
}

// chunkSearchRow GORM Scan 用的扁平行结构,仅在本文件可见。
type chunkSearchRow struct {
	ChunkID     uint64         `gorm:"column:chunk_id"`
	DocID       uint64         `gorm:"column:doc_id"`
	OrgID       uint64         `gorm:"column:org_id"`
	ChunkIdx    int            `gorm:"column:chunk_idx"`
	Content     string         `gorm:"column:content"`
	HeadingPath pq.StringArray `gorm:"column:heading_path"`
	Distance    float32        `gorm:"column:distance"`
	DocTitle    string         `gorm:"column:doc_title"`
	DocFileName string         `gorm:"column:doc_file_name"`
	DocMIMEType string         `gorm:"column:doc_mime_type"`
	DocSourceID uint64         `gorm:"column:doc_source_id"`
}

// SearchByEmbedding 见 Repository 接口注释。
//
// 边界:
//   - topK ≤ 0 或 queryVec 为空 → 返 nil,nil(调用方自己 sanity check)
//   - sourceIDs 和 docIDs 都为空 → 返 nil,nil(可见集为空,无候选)
//   - HNSW oversample = max(topK*5, 50);经验值,平衡"过滤后还剩 K 条"和"index 扫描成本"
func (r *gormRepository) SearchByEmbedding(
	ctx context.Context,
	orgID uint64,
	sourceIDs []uint64,
	docIDs []uint64,
	queryVec []float32,
	topK int,
) ([]ChunkSearchHit, error) {
	if topK <= 0 || len(queryVec) == 0 {
		return nil, nil
	}
	if len(sourceIDs) == 0 && len(docIDs) == 0 {
		return nil, nil
	}

	oversample := max(topK*5, 50)

	// pgvector 字面量,直接拼进 SQL —— 同 insertChunkBatch 的写法,只含数字 + 逗号 + 中括号,
	// 无 SQL 注入面;参数绑定走 vector 类型在不同驱动里行为不一致,字面量最稳。
	vecLit := formatVectorLiteral(queryVec)

	sql := fmt.Sprintf(`
WITH candidates AS (
    SELECT id, doc_id, org_id, chunk_idx, content, heading_path,
           embedding <=> '%s'::vector AS distance
    FROM %s
    WHERE org_id = ?
      AND index_status = ?
    ORDER BY embedding <=> '%s'::vector
    LIMIT ?
)
SELECT c.id           AS chunk_id,
       c.doc_id       AS doc_id,
       c.org_id       AS org_id,
       c.chunk_idx    AS chunk_idx,
       c.content      AS content,
       c.heading_path AS heading_path,
       c.distance     AS distance,
       d.title              AS doc_title,
       d.file_name          AS doc_file_name,
       d.mime_type          AS doc_mime_type,
       d.knowledge_source_id AS doc_source_id
FROM candidates c
JOIN %s d ON d.id = c.doc_id
WHERE d.knowledge_source_id = ANY(?) OR d.id = ANY(?)
ORDER BY c.distance
LIMIT ?
`, vecLit, document.TableDocumentChunks, vecLit, document.TableDocuments)

	var rows []chunkSearchRow
	err := r.db.WithContext(ctx).Raw(sql,
		orgID,
		document.ChunkIndexStatusIndexed,
		oversample,
		pq.Array(uint64SliceToInt64(sourceIDs)),
		pq.Array(uint64SliceToInt64(docIDs)),
		topK,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("search by embedding: %w: %w", err, document.ErrDocumentInternal)
	}

	hits := make([]ChunkSearchHit, 0, len(rows))
	for _, r := range rows {
		hp := []string(r.HeadingPath)
		if hp == nil {
			hp = []string{}
		}
		hits = append(hits, ChunkSearchHit{
			ChunkID:     r.ChunkID,
			DocID:       r.DocID,
			OrgID:       r.OrgID,
			ChunkIdx:    r.ChunkIdx,
			Content:     r.Content,
			HeadingPath: hp,
			Distance:    r.Distance,
			DocTitle:    r.DocTitle,
			DocFileName: r.DocFileName,
			DocMIMEType: r.DocMIMEType,
			DocSourceID: r.DocSourceID,
		})
	}
	return hits, nil
}

// uint64SliceToInt64 pq.Array 不直接支持 []uint64,显式转换。
func uint64SliceToInt64(in []uint64) []int64 {
	out := make([]int64, len(in))
	for i, v := range in {
		out[i] = int64(v)
	}
	return out
}
