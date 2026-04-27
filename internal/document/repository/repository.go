// Package repository document 模块数据访问层(PG)。
//
// 设计要点:
//
//   - 所有写操作经 WithTx 或内部事务保证原子性,单条 upsert 路径:
//     INSERT/UPDATE documents → DELETE 旧 chunks → BATCH INSERT 新 chunks 全在一个 TX
//   - FK ON DELETE CASCADE 兜住"doc 行消失 chunks 连带清"
//   - embedding 写入用 Raw SQL 拼 pgvector 字面量 "[v1,v2,...]::vector",不走 gorm struct 字段
package repository

import (
	"context"

	"github.com/eyrihe999-stack/Synapse/internal/document/model"
	"gorm.io/gorm"
)

// VersionInfo GetVersion 的返回。
type VersionInfo struct {
	DocID   uint64
	Version string
	Exists  bool
}

// ChunkWithVec persister 传给 UpsertWithChunks 的 chunk + 向量二元组。
//
// Vec == nil 表示该 chunk embed 失败(非致命)—— repository 会把该 chunk 的 index_status 置 failed、
// embedding 列置 NULL,index_error 用 fallbackErrSummary 兜底(chunk 行级错 vs doc 行级错区分)。
//
// len(Vec) 必须等于 migration 里的 embedding 维度,否则 PG INSERT 会直接报错。
// repository 层不做维度校验(昂贵);上游 pipeline 已经在 cfg 级别 sanity check 过,维度不会漂。
type ChunkWithVec struct {
	Chunk model.DocumentChunk
	Vec   []float32 // nil → failed
}

// ListOptions 列表接口分页 / 过滤。cursor 为上一页最后一行的 ID(string 形式,便于以后换 opaque cursor)。
type ListOptions struct {
	Provider string // 空串 = 所有 provider
	Query    string // 空串 = 不做文本过滤;非空时按 LOWER(title) LIKE %q% OR LOWER(file_name) LIKE %q% 过滤
	Limit    int    // 0 → 默认 20,上限 100
	BeforeID uint64 // 0 → 第一页;非 0 → 取 id < BeforeID 的行(按 id 降序分页)

	// DocID 非 0 时按 doc.id 精确过滤(仍受 KnowledgeSourceIDs 可见集约束)。
	DocID uint64
	// KnowledgeSourceID 非 0 时按 doc.knowledge_source_id 精确过滤。
	// 调用方负责确保该 source_id 在 KnowledgeSourceIDs 可见集里,否则会被 IN 过滤掉返空。
	KnowledgeSourceID uint64

	// KnowledgeSourceIDs M3 ACL 过滤:
	//   - nil:不过滤(向后兼容/单测使用)
	//   - 空 slice:返空结果(等于"该用户在该 org 看不到任何 source")
	//   - 非空:WHERE knowledge_source_id IN (...)
	KnowledgeSourceIDs []uint64
}

// Repository document 模块的对外数据访问入口。
type Repository interface {
	// WithTx 在单事务里执行 fn。嵌套 fn 内的 repository 调用共享同一个 tx。
	WithTx(ctx context.Context, fn func(tx Repository) error) error

	// ─── 写 ─────────────────────────────────────────────────────────────

	// UpsertWithChunks 核心 upsert:一个事务里完成
	//   1. 按 (org_id, source_type, source_id) upsert documents(INSERT ... ON CONFLICT ... DO UPDATE)
	//   2. DELETE FROM document_chunks WHERE doc_id = <返 id>(清旧 chunks)
	//   3. 批量 INSERT 新 chunks(带向量)
	//
	// doc.ID 由调用方预先生成(snowflake),既是 PK 也作为 chunks 的 FK。
	// chunks 可以为空(空 chunk 仅走步骤 1+2,文档元数据仍落库)。
	//
	// 失败路径:任意步骤错 → TX 回滚,返 ErrDocumentInternal wrap。
	UpsertWithChunks(ctx context.Context, doc *model.Document, chunks []ChunkWithVec) (uint64, error)

	// ─── 读 ─────────────────────────────────────────────────────────────

	// GetVersion fetcher 增量判定用:查 (org_id, source_type, source_id) 当前版本。
	// 不存在 → VersionInfo{Exists:false}、err=nil。
	GetVersion(ctx context.Context, orgID uint64, sourceType, sourceID string) (VersionInfo, error)

	// GetByID 查单条 doc(不含 chunks)。不存在返 ErrDocumentNotFound。
	GetByID(ctx context.Context, orgID, docID uint64) (*model.Document, error)

	// ListByOrg 分页列 doc 元数据。按 id DESC,基于 BeforeID 的 keyset 分页。
	ListByOrg(ctx context.Context, orgID uint64, opts ListOptions) ([]*model.Document, error)

	// CountChunks 统计某 doc 的 chunks 数量,按状态分组。retrieval / 诊断 UI 用。
	CountChunks(ctx context.Context, docID uint64) (indexed, failed int64, err error)

	// ListChunksByDocOrdered 按 chunk_idx ASC 拉某 doc 全部 chunks(不读 embedding 列)。
	// 给 MCP `get_kb_document` 用 —— 把 chunks 按顺序拼回原文供 agent 阅读。
	// chunks 为空表示文档未分块或 ingestion 未完成。
	ListChunksByDocOrdered(ctx context.Context, orgID, docID uint64) ([]model.DocumentChunk, error)

	// SearchByEmbedding 按向量近邻在 org 内搜 chunks,scope 由 sourceIDs ∪ docIDs 限定
	// (调用方负责把 channel 可见集传进来)。
	//
	// 实现走 HNSW + JOIN documents,返回 chunk 内容 + 必要的 doc 元数据(title/mime/source_id)。
	// 失败返 ErrDocumentInternal wrap;空可见集 / topK ≤ 0 返 (nil, nil)。
	SearchByEmbedding(ctx context.Context, orgID uint64, sourceIDs, docIDs []uint64, queryVec []float32, topK int) ([]ChunkSearchHit, error)

	// CountBySource 统计某 source 下属的 doc 数量。source 删除前的前置守卫用。
	// 0 表示该 source 下没有任何 doc;source.DeleteSource 仅在计数为 0 时放行。
	CountBySource(ctx context.Context, orgID, sourceID uint64) (int64, error)

	// ─── 删 ─────────────────────────────────────────────────────────────

	// DeleteByID 按 ID 删 doc(CASCADE 连带删 chunks)。不存在视为幂等成功,不返 NotFound。
	DeleteByID(ctx context.Context, orgID, docID uint64) error

	// DeleteBySourceID 按源端幂等键删(fetcher 检测到源端 tombstone 时用)。
	// 不存在视为幂等成功。
	DeleteBySourceID(ctx context.Context, orgID uint64, sourceType, sourceID string) error

	// ─── document_versions ─────────────────────────────────────────────

	// InsertVersion 插入一条版本记录。handler 在 OSS PutObject 成功后调用。
	InsertVersion(ctx context.Context, v *model.DocumentVersion) error

	// ListVersionsByDoc 按 created_at DESC 列出某 doc 的所有历史版本。
	// handler 下载 / 删文档时枚举全部 oss_key 用。
	ListVersionsByDoc(ctx context.Context, docID uint64) ([]*model.DocumentVersion, error)

	// PruneOldVersions 保留最近 keep 条(按 created_at DESC),多余的(最老的)删掉。
	// 返回被删掉的 oss_key 列表,供调用方去 OSS 真实删除对象。
	// keep ≤ 0 视为不裁剪,返 nil,nil。
	PruneOldVersions(ctx context.Context, docID uint64, keep int) ([]string, error)
}

// gormRepository 基于 GORM 的统一实现。
type gormRepository struct {
	db *gorm.DB
}

// New 构造一个 Repository 实例。
func New(db *gorm.DB) Repository {
	return &gormRepository{db: db}
}

// WithTx 开启事务并在其中执行 fn。
func (r *gormRepository) WithTx(ctx context.Context, fn func(tx Repository) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&gormRepository{db: tx})
	})
}
