// Package document 是知识库文档持久层。
//
// 职责边界:
//
//   - 只做 PG 侧两张表(documents / document_chunks)的 CRUD + upsert,
//     供后续 ingestion persister、retrieval 模块、HTTP CRUD 共用。
//   - 不做切分、不做向量化、不做源端拉取(那是 ingestion/chunker、common/embedding、
//     ingestion/source/* 的事)。
//
// 表设计要点(和 internal/ingestion/normalized.go 的 NormalizedDoc 对齐):
//
//   - 同 (org_id, source_type, source_id) 唯一,作为源端幂等键
//   - provider 独立一列,允许"同一 source_type 多 provider"(全部走 SourceType=document)
//   - chunks 通过 doc_id FK + ON DELETE CASCADE 随 doc 一起清
//   - embedding 维度随 cfg.Embedding.Text.ModelDim 动态建表,启动时做 sanity check
package document

// ─── 表名 ────────────────────────────────────────────────────────────────────

const (
	// TableDocuments doc 元数据表(PG)。
	TableDocuments = "documents"
	// TableDocumentChunks chunk + 向量表(PG)。
	TableDocumentChunks = "document_chunks"
	// TableDocumentVersions 文档 OSS 版本历史表(PG)。每次 upload 成功写 OSS 后 insert 一行。
	TableDocumentVersions = "document_versions"
)

// ─── chunk index_status 取值(决策 4) ────────────────────────────────────────

const (
	// ChunkIndexStatusIndexed embed 成功,embedding 非 NULL,进 HNSW partial index。
	ChunkIndexStatusIndexed = "indexed"
	// ChunkIndexStatusFailed embed 非致命失败,embedding 为 NULL,index_error 记摘要。
	// 留库用于后续补偿任务重试;HNSW partial index 不收录。
	ChunkIndexStatusFailed = "failed"
)

// ─── HNSW 索引参数(决策 2) ──────────────────────────────────────────────────

const (
	// HNSWParamM 每节点邻居数。pgvector 推荐默认 16。
	HNSWParamM = 16
	// HNSWParamEFConstruction 建索引候选集大小。默认 64。
	HNSWParamEFConstruction = 64

	// PGVectorMaxDim pgvector HNSW 索引支持的维度上限。
	// 超过必须走 ivfflat 或改小维度;当前链路强制 ≤ 2000,装配期 fatal。
	PGVectorMaxDim = 2000
)
