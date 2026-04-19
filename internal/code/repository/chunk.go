// chunk.go code_chunks 表(Postgres + pgvector)数据访问。
//
// 生命周期(对照 document/repository/chunk.go):
//   - Ingest 先 InsertChunks(status=pending, Embedding=nil)
//   - embedder 成功  → UpdateEmbedding 转 indexed
//   - embedder 失败  → MarkFailed 转 failed + IndexError
//   - 文件内容变化   → SwapChunksByFileID 原子替换整个文件的 chunks
//   - 删 repo / file → DeleteByFileID / DeleteByRepoID 批量清
//   - 检索           → SearchByVector(ANN) + SearchBySymbol(ILIKE) 双路召回,service 层合并
package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/code"
	"github.com/eyrihe999-stack/Synapse/internal/code/model"
)

// ChunkSearchFilter 两路召回共用的过滤条件。字段均可选。
//
// 设计意图:把过滤条件独立抽出,让 Search* 方法调用面稳定;将来加字段(作者 / 创建时间 等)
// 只改这个 struct 不动两个方法签名。
type ChunkSearchFilter struct {
	// Languages 命中 chunks 的 language 必须在此列表里(空 = 不过滤)。
	// 场景:agent 明确"只搜 Go 代码"。比按 language 建索引灵活(可多选)。
	Languages []string
	// RepoIDs 限定只搜这几个 repo。空 = 不过滤。
	// 场景:用户已经定位到了某个 repo,让检索聚焦。
	RepoIDs []uint64
}

// isEmpty 无任何过滤 —— repo 方法据此跳过 WHERE 拼接。
func (f *ChunkSearchFilter) isEmpty() bool {
	return f == nil || (len(f.Languages) == 0 && len(f.RepoIDs) == 0)
}

// ChunkHit 一条召回结果。
//
// Distance 语义和 document.ChunkHit 一致:
//   - 向量路径:pgvector cosine 距离 [0, 2],越小越相关
//   - 符号路径:不计算距离,统一填 0(表示"精确匹配,最高相关"),让合并时走前排
//
// MatchSource 标记命中来源,service 层合并去重后保留首个命中的来源,方便调试和后续 rerank。
type ChunkHit struct {
	Chunk       *model.CodeChunk
	Distance    float32
	MatchSource string // "vector" / "symbol"
}

// maxIndexErrorLen 和 document 模块保持一致。
const maxIndexErrorLen = 1024

// CodeChunkRepo code_chunks 表 CRUD。
type CodeChunkRepo interface {
	// InsertChunks 批量插入。tx 包装保证多条 chunks 要么全可见要么全不可见。
	// 调用侧通常传 pending 状态 + Embedding=nil,走异步 embed 回填。
	InsertChunks(ctx context.Context, chunks []*model.CodeChunk) error

	// UpdateEmbedding 回填向量 + 转 indexed。
	// embedding 长度必须等于 code.ChunkEmbeddingDim,否则返 error(schema / provider 失配保护)。
	UpdateEmbedding(ctx context.Context, chunkID uint64, embedding []float32, modelTag string) error

	// MarkFailed 标记 failed + 记录错误(超长截断 maxIndexErrorLen)。
	MarkFailed(ctx context.Context, chunkID uint64, errMsg string) error

	// SwapChunksByFileID tx 内先删该 file 全部旧 chunks,再插入 new。
	// 原子可见:并发检索要么看全旧集,要么看全新集,看不到"旧删了新没插"的中间态。
	SwapChunksByFileID(ctx context.Context, fileID uint64, newChunks []*model.CodeChunk) error

	// ListByFileID 按 file_id 列所有 chunks(含 embedding 字段),按 chunk_idx 升序。
	// 用途:ingest service 遇到相同 blob_sha 已有 chunks 时,直接复制到新 file_id 下省 embed。
	ListByFileID(ctx context.Context, fileID uint64) ([]*model.CodeChunk, error)

	// DeleteByFileID 删单文件下所有 chunks。返删除行数,诊断用。
	DeleteByFileID(ctx context.Context, fileID uint64) (int64, error)

	// DeleteByRepoID 删整个 repo 下所有 chunks(删 repo 级联用)。
	DeleteByRepoID(ctx context.Context, repoID uint64) (int64, error)

	// SearchByVector pgvector cosine ANN。org_id 强制过滤,index_status=indexed 排除
	// 未 embed 成功的行(否则会命中 NULL 向量 → 异常距离)。
	// queryVec 长度必须等于 code.ChunkEmbeddingDim。topK ≤ 0 返 nil。
	SearchByVector(ctx context.Context, orgID uint64, queryVec []float32, topK int, filter *ChunkSearchFilter) ([]ChunkHit, error)

	// SearchBySymbol 按 symbol_name 做 ILIKE '%query%' 匹配(PG 不区分大小写索引友好)。
	// 用途:用户提问里直接提函数/类名(如 "ChatService"、"handlePayment")时,向量召回未必靠前,
	// 精确前缀 / 子串匹配能补上这一路高置信命中。
	// query 空或仅空白 → 返 nil(不等于 SQL `LIKE '%%'` 把全表拉回)。
	SearchBySymbol(ctx context.Context, orgID uint64, query string, topK int, filter *ChunkSearchFilter) ([]ChunkHit, error)

	// GetByID 按 (org_id, chunk_id) 精确取单条 chunk。
	// org_id 强制过滤,防跨租户越权;找不到返 (nil, nil) —— not-found 不是 error,调用方按 nil 判。
	// 给 retrieval.Retriever.FetchByID 用:agent 拿到 Hit.ID 想展开全文时精确回拉。
	GetByID(ctx context.Context, orgID, chunkID uint64) (*model.CodeChunk, error)
}

type gormCodeChunkRepo struct {
	db *gorm.DB
}

// NewCodeChunkRepo 构造。
func NewCodeChunkRepo(pgDB *gorm.DB) CodeChunkRepo {
	return &gormCodeChunkRepo{db: pgDB}
}

// InsertChunks tx 包裹:防未来被改成 CreateInBatches 时默默破坏原子性。
// 单条 INSERT ... VALUES 在 PG 参数上限(65535)下天然原子;单文件的 chunks 数远低于此阈值。
func (r *gormCodeChunkRepo) InsertChunks(ctx context.Context, chunks []*model.CodeChunk) error {
	if len(chunks) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&chunks).Error; err != nil {
			return fmt.Errorf("insert code chunks: %w", err)
		}
		return nil
	})
}

func (r *gormCodeChunkRepo) UpdateEmbedding(ctx context.Context, chunkID uint64, embedding []float32, modelTag string) error {
	if len(embedding) != code.ChunkEmbeddingDim {
		return fmt.Errorf("embedding dim mismatch: got %d, want %d", len(embedding), code.ChunkEmbeddingDim)
	}
	vec := pgvector.NewVector(embedding)
	updates := map[string]any{
		"embedding":       vec,
		"embedding_model": modelTag,
		"index_status":    code.ChunkIndexStatusIndexed,
		"index_error":     "",
	}
	err := r.db.WithContext(ctx).
		Model(&model.CodeChunk{}).
		Where("id = ?", chunkID).
		Updates(updates).Error
	if err != nil {
		return fmt.Errorf("update code chunk embedding: %w", err)
	}
	return nil
}

func (r *gormCodeChunkRepo) MarkFailed(ctx context.Context, chunkID uint64, errMsg string) error {
	trimmed := errMsg
	if len(trimmed) > maxIndexErrorLen {
		trimmed = trimmed[:maxIndexErrorLen-3] + "..."
	}
	updates := map[string]any{
		"index_status": code.ChunkIndexStatusFailed,
		"index_error":  trimmed,
	}
	err := r.db.WithContext(ctx).
		Model(&model.CodeChunk{}).
		Where("id = ?", chunkID).
		Updates(updates).Error
	if err != nil {
		return fmt.Errorf("mark code chunk failed: %w", err)
	}
	return nil
}

// SwapChunksByFileID tx 内"先删后插",PG 默认 READ COMMITTED 下
// 其他事务只能在 COMMIT 时刻一次性看到新集,看不到"旧全删、新没插"的空态。
func (r *gormCodeChunkRepo) SwapChunksByFileID(ctx context.Context, fileID uint64, newChunks []*model.CodeChunk) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("file_id = ?", fileID).Delete(&model.CodeChunk{}).Error; err != nil {
			return fmt.Errorf("swap code chunks: delete old: %w", err)
		}
		if len(newChunks) > 0 {
			if err := tx.Create(&newChunks).Error; err != nil {
				return fmt.Errorf("swap code chunks: insert new: %w", err)
			}
		}
		return nil
	})
}

func (r *gormCodeChunkRepo) ListByFileID(ctx context.Context, fileID uint64) ([]*model.CodeChunk, error) {
	var out []*model.CodeChunk
	err := r.db.WithContext(ctx).
		Where("file_id = ?", fileID).
		Order("chunk_idx ASC").
		Find(&out).Error
	if err != nil {
		return nil, fmt.Errorf("list code chunks by file: %w", err)
	}
	return out, nil
}

func (r *gormCodeChunkRepo) DeleteByFileID(ctx context.Context, fileID uint64) (int64, error) {
	res := r.db.WithContext(ctx).
		Where("file_id = ?", fileID).
		Delete(&model.CodeChunk{})
	if res.Error != nil {
		return 0, fmt.Errorf("delete code chunks by file: %w", res.Error)
	}
	return res.RowsAffected, nil
}

func (r *gormCodeChunkRepo) DeleteByRepoID(ctx context.Context, repoID uint64) (int64, error) {
	res := r.db.WithContext(ctx).
		Where("repo_id = ?", repoID).
		Delete(&model.CodeChunk{})
	if res.Error != nil {
		return 0, fmt.Errorf("delete code chunks by repo: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// SearchByVector pgvector cosine ANN。HNSW 索引(migration 里建好)自动命中。
//
// 用 Raw SQL 的原因和 document.ChunkRepo.SearchByVector 相同:ORDER BY 表达式里要绑定向量参数,
// gorm chain API 对 ORDER BY + 参数绑定的支持不如 Raw 直观,且两次出现同一参数(SELECT + ORDER BY)
// 让 chain 拼起来更绕。
func (r *gormCodeChunkRepo) SearchByVector(ctx context.Context, orgID uint64, queryVec []float32, topK int, filter *ChunkSearchFilter) ([]ChunkHit, error) {
	if topK <= 0 {
		return nil, nil
	}
	if len(queryVec) != code.ChunkEmbeddingDim {
		return nil, fmt.Errorf("query vector dim mismatch: got %d, want %d", len(queryVec), code.ChunkEmbeddingDim)
	}
	vec := pgvector.NewVector(queryVec)

	var sb strings.Builder
	sb.WriteString(`SELECT id, file_id, repo_id, org_id, chunk_idx, symbol_name, signature, language,
		       chunk_kind, line_start, line_end, content, token_count,
		       embedding_model, embedding, index_status, index_error,
		       chunker_version, created_at, updated_at,
		       embedding <=> ? AS distance
		FROM code_chunks
		WHERE org_id = ? AND index_status = ?`)
	args := []any{vec, orgID, code.ChunkIndexStatusIndexed}

	if !filter.isEmpty() {
		if len(filter.Languages) > 0 {
			sb.WriteString(" AND language IN ?")
			args = append(args, filter.Languages)
		}
		if len(filter.RepoIDs) > 0 {
			sb.WriteString(" AND repo_id IN ?")
			args = append(args, filter.RepoIDs)
		}
	}

	sb.WriteString(" ORDER BY embedding <=> ? LIMIT ?")
	args = append(args, vec, topK)

	type row struct {
		model.CodeChunk
		Distance float32 `gorm:"column:distance"`
	}
	var rows []row
	if err := r.db.WithContext(ctx).Raw(sb.String(), args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("search code chunks by vector: %w", err)
	}

	hits := make([]ChunkHit, len(rows))
	for i := range rows {
		c := rows[i].CodeChunk
		hits[i] = ChunkHit{Chunk: &c, Distance: rows[i].Distance, MatchSource: "vector"}
	}
	return hits, nil
}

// SearchBySymbol symbol_name ILIKE '%q%'。
//
// 为什么走 ILIKE 不走 tsvector:
//   - 代码标识符不是自然语言词,tsvector 的 simple tokenizer 会把 "handlePayment" 当成一个整 token,
//     查 "handle" 不会命中。用 ILIKE 直接做子串匹配,对驼峰 / 下划线都友好。
//   - symbol_name 长度有限(size:255),PG 对短字符串的 ILIKE 性能可以接受;(org_id) 索引先把规模缩到单 org。
//   - 只限 indexed 行:failed 的 chunk 虽然有 symbol_name,但没向量,不该混进检索结果。
//
// 排序:按 chunk_idx ASC 保证输出稳定(同一个符号名的多次命中按源码顺序),不按"相关度"排 —— 命中就是命中。
func (r *gormCodeChunkRepo) SearchBySymbol(ctx context.Context, orgID uint64, query string, topK int, filter *ChunkSearchFilter) ([]ChunkHit, error) {
	if topK <= 0 {
		return nil, nil
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	// LIKE 特殊字符转义(% / _),避免用户输入的符号名里的下划线被当通配符。
	pattern := "%" + escapeLike(q) + "%"

	tx := r.db.WithContext(ctx).
		Where("org_id = ? AND index_status = ?", orgID, code.ChunkIndexStatusIndexed).
		Where("symbol_name ILIKE ? ESCAPE '\\'", pattern)

	if !filter.isEmpty() {
		if len(filter.Languages) > 0 {
			tx = tx.Where("language IN ?", filter.Languages)
		}
		if len(filter.RepoIDs) > 0 {
			tx = tx.Where("repo_id IN ?", filter.RepoIDs)
		}
	}

	var chunks []*model.CodeChunk
	if err := tx.
		Order("chunk_idx ASC").
		Limit(topK).
		Find(&chunks).Error; err != nil {
		return nil, fmt.Errorf("search code chunks by symbol: %w", err)
	}

	hits := make([]ChunkHit, len(chunks))
	for i, c := range chunks {
		// 精确匹配统一 Distance=0,让 service 层合并时排在向量命中前面。
		hits[i] = ChunkHit{Chunk: c, Distance: 0, MatchSource: "symbol"}
	}
	return hits, nil
}

func (r *gormCodeChunkRepo) GetByID(ctx context.Context, orgID, chunkID uint64) (*model.CodeChunk, error) {
	var c model.CodeChunk
	err := r.db.WithContext(ctx).
		Where("org_id = ? AND id = ?", orgID, chunkID).
		First(&c).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("get code chunk by id: %w", err)
	}
	return &c, nil
}

// escapeLike 转义 LIKE 的特殊字符(\ % _),和上面 ESCAPE '\' 配套。
// 不转 [] —— PG 的 LIKE 不支持 POSIX 字符类,不会误匹配。
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}
