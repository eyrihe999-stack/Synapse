// chunk.go document_chunks 表的 Postgres(pgvector)数据访问。
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/document"
	"github.com/eyrihe999-stack/Synapse/internal/document/model"
)

// maxIndexErrorLen 落库的 index_error 最大长度,防止一个超长 stack trace 撑爆行。
// 1KB 足够记关键错误,超出截断到该长度并加省略号。
const maxIndexErrorLen = 1024

// ChunkSearchFilter 结构化 metadata / doc 过滤,附加在向量检索的 WHERE 上。
//
// 所有字段可选,零值 / nil 表示"不过滤"。多个字段同时给是 AND 关系。
//
// 设计取向:预定义字段覆盖 agent 最常用的"限定搜索范围"需求(heading 章节 / 指定 doc),
// 不暴露 jsonb 语法 —— 上层(包括 agent 调用)不用学 `@>` / jsonpath。未来新源类型(code/bug)
// 的 tag 过滤直接在本 struct 加字段,不改调用面。
type ChunkSearchFilter struct {
	// HeadingPathContains 命中 chunks 的 metadata.heading_path 数组必须**包含**这里所有元素(AND)。
	// 例如 ["支付"] 命中 heading_path 里有"支付"这一项的所有 chunks(不管它在路径的第几层)。
	// 顺序无关,元素级精确匹配。空 slice 或 nil = 不过滤。
	// 底层 SQL:metadata @> '{"heading_path": [...]}',走 GIN 索引。
	HeadingPathContains []string

	// DocIDs 限定只搜这些 doc 下的 chunks。nil / empty = 不过滤。
	// 用途:agent 做 scoped follow-up("再在这几篇里深挖");评测做"固定候选集"实验。
	DocIDs []uint64
}

// isEmpty 无任何过滤条件。上层据此判断是否要绕过 filter 分支,保留原路径的 SQL 行为(零变更)。
func (f *ChunkSearchFilter) isEmpty() bool {
	return f == nil || (len(f.HeadingPathContains) == 0 && len(f.DocIDs) == 0)
}

// ChunkHit 一条 ANN 检索结果。
//
// Distance 是 pgvector 的 cosine 距离(<=> 运算符):
//   - 0   :向量完全重合(同 chunk 再检索自己)
//   - 1   :正交(语义无关)
//   - 2   :反向(反义内容)
//
// 上层如果需要 similarity 分数,可以用 `1 - Distance` 换算。
//
// RerankScore 仅在 service 层过了 cross-encoder 重排后由上层填入,repository 一律不写。
// nil = 未经过 rerank。BGE-reranker-v2-m3 raw logits 范围约 [-10, 10],正值大致对应
// sigmoid > 0.5(比 coin flip 更相关);上层可用来做"低置信度"阈值过滤。
// 不同 reranker 实现的分数量纲不同,阈值调用方按实现决定(见 SearchChunksOptions.MinRerankScore)。
type ChunkHit struct {
	Chunk       *model.DocumentChunk
	Distance    float32
	RerankScore *float32
}

// ChunkRepository document_chunks 表(Postgres)数据访问入口。
//
// 生命周期约定:
//   - 上传链路先 InsertChunks(status=pending, Embedding=nil)占位;
//   - embedder 成功 → UpdateChunkEmbedding 回填并转 indexed;失败 → MarkChunkFailed 转 failed。
//   - 删文档 → DeleteChunksByDocID 批量清。
//   - SearchByVector 只返回 indexed 行,避免 pending/failed 的"半成品"污染检索结果。
type ChunkRepository interface {
	// InsertChunks 批量插入。返回后每个 chunk 的 ID 字段被 GORM 填回。
	InsertChunks(ctx context.Context, chunks []*model.DocumentChunk) error

	// UpdateChunkEmbedding 回填向量,状态转 indexed,清 index_error。
	// embedding 长度必须等于 document.ChunkEmbeddingDim,否则返回错误(防 schema / provider 失配)。
	UpdateChunkEmbedding(ctx context.Context, chunkID uint64, embedding []float32, modelTag string) error

	// MarkChunkFailed 标记失败,记录错误消息(超长截断)。
	MarkChunkFailed(ctx context.Context, chunkID uint64, errMsg string) error

	// DeleteChunksByDocID 删除某篇文档的所有 chunks(Delete 级联用)。
	DeleteChunksByDocID(ctx context.Context, docID uint64) error

	// SwapChunksByDocID 在单个 PG tx 内先删掉 docID 的所有旧 chunks,再 INSERT 新 chunks。
	// 原子可见:并发 SearchByVector 要么全看到旧集,要么全看到新集,不会看到"旧已删、新未 INSERT"的空态。
	// 供 Upload 覆盖更新路径用。
	SwapChunksByDocID(ctx context.Context, docID uint64, newChunks []*model.DocumentChunk) error

	// SearchByVector 按 cosine 距离找最近 topK 个 indexed chunk,只看当前 org。
	// queryVec 长度必须等于 document.ChunkEmbeddingDim。topK ≤ 0 返回空。
	// filter 可选:传 nil 或 &ChunkSearchFilter{} 行为等同旧签名 —— 只按 org + indexed 过滤。
	// 传入字段的 AND 语义,详见 ChunkSearchFilter 注释。
	SearchByVector(ctx context.Context, orgID uint64, queryVec []float32, topK int, filter *ChunkSearchFilter) ([]ChunkHit, error)

	// SearchByBM25 T1.1 混合检索的字面召回通路:用 PG ts_rank_cd 对 content_tsv @@ tsquery 打分。
	// queryTokens 是 Go 侧已分词的词流(空格连接),和入库时 tsv_tokens 的格式对称 ——
	// 双端都走 PG 'simple' 配置,避免 ts_rank 因 tokenizer 失配而漏匹。
	// 返回按 rank 降序排列的 ChunkHit(Distance 字段复用成 1-rank,越小越相关,与 vector 通路语义对齐),
	// 供上层 RRF 融合时按顺序取 rank 用。
	// topK ≤ 0 / queryTokens 空 / content_tsv 全空 → 返空切片,不是错误。
	SearchByBM25(ctx context.Context, orgID uint64, queryTokens string, topK int, filter *ChunkSearchFilter) ([]ChunkHit, error)

	// ListPendingChunks 扫 pending + failed 行供后台补偿任务;按 id 升序,limit 截断。
	ListPendingChunks(ctx context.Context, limit int) ([]*model.DocumentChunk, error)

	// GetByID 按 (org_id, chunk_id) 精确取单条 chunk。
	// org_id 强制过滤,防跨租户越权;找不到返 (nil, nil),not-found 不算 error,调用方按 nil 判。
	// 给 retrieval.Retriever.FetchByID 用:agent 拿到 Hit.ID 想展开全文时精确回拉。
	GetByID(ctx context.Context, orgID, chunkID uint64) (*model.DocumentChunk, error)

	// GetByIDs 批量按 (org_id, chunk_ids) 取 chunks,结果按 chunk_id 索引成 map。
	// 给 retrieval docadapter 扩父 chunk 用:命中一批 leaf 后,拿 parent_chunk_id 去重批量回拉。
	// 空 ids 返空 map,不报错。
	GetByIDs(ctx context.Context, orgID uint64, chunkIDs []uint64) (map[uint64]*model.DocumentChunk, error)
}

type gormChunkRepository struct {
	db *gorm.DB
}

// NewChunkRepository 构造 ChunkRepository。pgDB 必须非 nil —— 调用方负责:未启用 pgvector 时整段跳过。
func NewChunkRepository(pgDB *gorm.DB) ChunkRepository {
	return &gormChunkRepository{db: pgDB}
}

// InsertChunks 批量写入,tx 包裹保证原子可见性:并发读者要么看见全部 N 条,要么一条都看不见。
//
// GORM 的 Create(slice) 会拼成单条 INSERT ... VALUES (...), (...), ...(Postgres 的参数上限 65535,
// 我们 schema 13 列 → 最多约 5000 行/次,远高于单篇文档的 chunk 数),单语句天然原子;
// 包进 Transaction 是防御性措施,防后续有人改成 CreateInBatches 时默默破坏原子性。
func (r *gormChunkRepository) InsertChunks(ctx context.Context, chunks []*model.DocumentChunk) error {
	if len(chunks) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&chunks).Error; err != nil {
			return fmt.Errorf("insert chunks: %w", err)
		}
		return nil
	})
}

// UpdateChunkEmbedding 用 WHERE id 精确更新,不依赖外层事务;
// 此操作可能在 Upload 提交后异步发生(embed 慢时),保持独立语义。
func (r *gormChunkRepository) UpdateChunkEmbedding(ctx context.Context, chunkID uint64, embedding []float32, modelTag string) error {
	if len(embedding) != document.ChunkEmbeddingDim {
		return fmt.Errorf("embedding dim mismatch: got %d, want %d", len(embedding), document.ChunkEmbeddingDim)
	}
	vec := pgvector.NewVector(embedding)
	updates := map[string]any{
		"embedding":       vec,
		"embedding_model": modelTag,
		"index_status":    document.ChunkIndexStatusIndexed,
		"index_error":     "",
	}
	if err := r.db.WithContext(ctx).
		Model(&model.DocumentChunk{}).
		Where("id = ?", chunkID).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("update chunk embedding: %w", err)
	}
	return nil
}

// MarkChunkFailed 超长消息按 UTF-8 字节截断到 maxIndexErrorLen-3 再加 "..." 保证可读 + 有边界。
func (r *gormChunkRepository) MarkChunkFailed(ctx context.Context, chunkID uint64, errMsg string) error {
	trimmed := errMsg
	if len(trimmed) > maxIndexErrorLen {
		trimmed = trimmed[:maxIndexErrorLen-3] + "..."
	}
	updates := map[string]any{
		"index_status": document.ChunkIndexStatusFailed,
		"index_error":  trimmed,
	}
	if err := r.db.WithContext(ctx).
		Model(&model.DocumentChunk{}).
		Where("id = ?", chunkID).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("mark chunk failed: %w", err)
	}
	return nil
}

// DeleteChunksByDocID 硬删,不走软删;chunks 没业务审计需求,占空间不如直接清掉。
func (r *gormChunkRepository) DeleteChunksByDocID(ctx context.Context, docID uint64) error {
	if err := r.db.WithContext(ctx).
		Where("doc_id = ?", docID).
		Delete(&model.DocumentChunk{}).Error; err != nil {
		return fmt.Errorf("delete chunks by doc id: %w", err)
	}
	return nil
}

// SwapChunksByDocID tx 包住"先删后插",让 Search 的单句 SELECT 要么命中旧集要么命中新集,
// 不会在两个语句之间观察到"旧全删、新还没插"的空态。
//
// PG 默认 READ COMMITTED 下,别的事务看到的是"COMMIT 时刻一次性翻转":
//   - DELETE 先执行(事务内可见删除效果,但对其他事务不可见);
//   - INSERT 后 COMMIT,对其他事务"新行出现"和"旧行消失"在同一个可见性 tick 内完成。
//
// newChunks 空切片时等效于纯清空(DELETE 只走,没 INSERT)—— 空文档覆盖为更空文档的场景。
func (r *gormChunkRepository) SwapChunksByDocID(ctx context.Context, docID uint64, newChunks []*model.DocumentChunk) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("doc_id = ?", docID).Delete(&model.DocumentChunk{}).Error; err != nil {
			return fmt.Errorf("swap chunks: delete old: %w", err)
		}
		if len(newChunks) > 0 {
			if err := tx.Create(&newChunks).Error; err != nil {
				return fmt.Errorf("swap chunks: insert new: %w", err)
			}
		}
		return nil
	})
}

// SearchByVector 用 Raw SQL + pgvector.Vector 绑定参数,让 pgx 自动处理类型转换。
//
// 查询形态:ORDER BY embedding <=> $1 LIMIT N,WHERE 过滤 org_id + index_status='indexed',
// 可选再加 metadata jsonb `@>` 过滤(GIN 索引)和 doc_id IN 过滤(btree 索引)。
// HNSW 对向量预排序 —— 但 PG 在有额外 WHERE 时会选 HNSW 还是全扫决定于 cost model;
// org 内文档数很小时(<1k),HNSW 仍稳占优。filter 选择性很强时可能会变 seq scan,
// 这是 acceptable —— 此场景本来候选集就少。
// SELECT 里带 `embedding <=> $1 AS distance` 重复了 ORDER BY 的表达式 —— pg 会复用一次计算。
func (r *gormChunkRepository) SearchByVector(ctx context.Context, orgID uint64, queryVec []float32, topK int, filter *ChunkSearchFilter) ([]ChunkHit, error) {
	if topK <= 0 {
		return nil, nil
	}
	if len(queryVec) != document.ChunkEmbeddingDim {
		return nil, fmt.Errorf("query vector dim mismatch: got %d, want %d", len(queryVec), document.ChunkEmbeddingDim)
	}
	vec := pgvector.NewVector(queryVec)

	// 动态拼 WHERE:基础条件两个 + 可选 filter。args 顺序严格和 ? 对齐。
	// 不用 gorm 链式 API 是因为 ORDER BY `embedding <=> ?` 需要参数绑定,链式 API 上 gorm.Expr
	// 的参数会和 Raw 的参数次序搅在一起,反而更易出错。
	var sb strings.Builder
	sb.WriteString(`SELECT id, doc_id, org_id, chunk_idx, content, content_hash, token_count,
		       embedding_model, embedding, index_status, index_error,
		       metadata, parent_chunk_id, chunk_level, content_type, chunker_version,
		       created_at, updated_at,
		       embedding <=> ? AS distance
		FROM document_chunks
		WHERE org_id = ? AND index_status = ?`)
	args := []any{vec, orgID, document.ChunkIndexStatusIndexed}

	if !filter.isEmpty() {
		if len(filter.HeadingPathContains) > 0 {
			// metadata @> '{"heading_path":["A","B"]}' 语义:heading_path 数组同时包含 A 和 B
			// (与顺序无关的子集关系)。Marshal 失败几乎不可能(都是 string slice),
			// 真出错就当作"未过滤"—— 总比整条检索 fail 强。
			payload, err := json.Marshal(map[string]any{"heading_path": filter.HeadingPathContains})
			if err == nil {
				sb.WriteString(" AND metadata @> ?")
				args = append(args, string(payload))
			}
		}
		if len(filter.DocIDs) > 0 {
			// `IN (?)` + 切片:GORM/pgx 会展开成 `IN ($n, $n+1, ...)`,走 btree idx_document_chunks_doc。
			sb.WriteString(" AND doc_id IN ?")
			args = append(args, filter.DocIDs)
		}
	}

	sb.WriteString(" ORDER BY embedding <=> ? LIMIT ?")
	args = append(args, vec, topK)

	type row struct {
		model.DocumentChunk
		Distance float32 `gorm:"column:distance"`
	}
	var rows []row
	if err := r.db.WithContext(ctx).Raw(sb.String(), args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("search chunks by vector: %w", err)
	}

	hits := make([]ChunkHit, len(rows))
	for i := range rows {
		c := rows[i].DocumentChunk
		hits[i] = ChunkHit{Chunk: &c, Distance: rows[i].Distance}
	}
	return hits, nil
}

// SearchByBM25 用 plainto_tsquery + ts_rank_cd 做 BM25-ish 字面打分检索。
//
// 关键点:
//   - 'simple' 配置双端对称:入库 tsv_tokens 在 GENERATED 列里走 to_tsvector('simple', ...),
//     查询 plainto_tsquery('simple', ?) 也用 'simple',保证 tokenizer 一致。'simple' 只做 lowercase
//     + whitespace split,不做 stem/stop word(正合意:我们已经在 Go 侧 gse 做了中文分词)。
//   - @@ 是 tsvector 的 match 操作符,GIN 索引 idx_document_chunks_content_tsv_gin 会被用上。
//   - ts_rank_cd 是 "cover density" —— 考虑词频 + 词位置 + 覆盖密度,比 ts_rank 更适合短文档。
//   - 返回的 ChunkHit.Distance 填 1 - score(越小越相关),和 vector 通路对齐,
//     让 RRF 合并代码路径统一:上层只按 rank(index)融合,Distance 供 debug 看相对质量。
func (r *gormChunkRepository) SearchByBM25(ctx context.Context, orgID uint64, queryTokens string, topK int, filter *ChunkSearchFilter) ([]ChunkHit, error) {
	if topK <= 0 {
		return nil, nil
	}
	queryTokens = strings.TrimSpace(queryTokens)
	if queryTokens == "" {
		return nil, nil
	}

	var sb strings.Builder
	sb.WriteString(`SELECT id, doc_id, org_id, chunk_idx, content, content_hash, token_count,
		       embedding_model, embedding, index_status, index_error,
		       metadata, parent_chunk_id, chunk_level, content_type, chunker_version,
		       created_at, updated_at,
		       ts_rank_cd(content_tsv, plainto_tsquery('simple', ?)) AS score
		FROM document_chunks
		WHERE org_id = ? AND index_status = ?
		  AND content_tsv @@ plainto_tsquery('simple', ?)`)
	args := []any{queryTokens, orgID, document.ChunkIndexStatusIndexed, queryTokens}

	if !filter.isEmpty() {
		if len(filter.HeadingPathContains) > 0 {
			payload, err := json.Marshal(map[string]any{"heading_path": filter.HeadingPathContains})
			if err == nil {
				sb.WriteString(" AND metadata @> ?")
				args = append(args, string(payload))
			}
		}
		if len(filter.DocIDs) > 0 {
			sb.WriteString(" AND doc_id IN ?")
			args = append(args, filter.DocIDs)
		}
	}

	sb.WriteString(" ORDER BY score DESC LIMIT ?")
	args = append(args, topK)

	type row struct {
		model.DocumentChunk
		Score float32 `gorm:"column:score"`
	}
	var rows []row
	if err := r.db.WithContext(ctx).Raw(sb.String(), args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("search chunks by bm25: %w", err)
	}

	hits := make([]ChunkHit, len(rows))
	for i := range rows {
		c := rows[i].DocumentChunk
		// 用 1 - score 让"距离越小越相关"语义和 vector 通路对齐。ts_rank_cd 理论上在 [0, +∞),
		// 实践中很少 > 1;clamp 到 0 让 RRF / debug 输出更干净。
		dist := 1.0 - rows[i].Score
		if dist < 0 {
			dist = 0
		}
		hits[i] = ChunkHit{Chunk: &c, Distance: dist}
	}
	return hits, nil
}

func (r *gormChunkRepository) GetByID(ctx context.Context, orgID, chunkID uint64) (*model.DocumentChunk, error) {
	var c model.DocumentChunk
	err := r.db.WithContext(ctx).
		Where("org_id = ? AND id = ?", orgID, chunkID).
		First(&c).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("get document chunk by id: %w", err)
	}
	return &c, nil
}

func (r *gormChunkRepository) GetByIDs(ctx context.Context, orgID uint64, chunkIDs []uint64) (map[uint64]*model.DocumentChunk, error) {
	if len(chunkIDs) == 0 {
		return map[uint64]*model.DocumentChunk{}, nil
	}
	var rows []*model.DocumentChunk
	if err := r.db.WithContext(ctx).
		Where("org_id = ? AND id IN ?", orgID, chunkIDs).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("get document chunks by ids: %w", err)
	}
	out := make(map[uint64]*model.DocumentChunk, len(rows))
	for _, c := range rows {
		out[c.ID] = c
	}
	return out, nil
}

// ListPendingChunks 按 id 升序扫 pending + failed 行,老的先重试。
// limit ≤ 0 视为无限制是个坑,这里强制至少 1,让调用方必须说清楚一次扫多少。
func (r *gormChunkRepository) ListPendingChunks(ctx context.Context, limit int) ([]*model.DocumentChunk, error) {
	if limit < 1 {
		limit = 1
	}
	var out []*model.DocumentChunk
	err := r.db.WithContext(ctx).
		Where("index_status IN ?", []string{document.ChunkIndexStatusPending, document.ChunkIndexStatusFailed}).
		Order("id ASC").
		Limit(limit).
		Find(&out).Error
	if err != nil {
		return nil, fmt.Errorf("list pending chunks: %w", err)
	}
	return out, nil
}
