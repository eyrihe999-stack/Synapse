// Package model document 模块数据模型。
package model

import (
	"time"

	"github.com/pgvector/pgvector-go"
	"gorm.io/datatypes"
)

const (
	tableDocuments      = "documents"
	tableDocumentChunks = "document_chunks"
)

// Document 文档主表。上传原文字节存在 OSS(以 OSSKey 定位),这里只留元数据。
//
// OSSKey 形如 {path_prefix}/{org_id}/{doc_id}/{file_name},全局唯一(由 service 层构造)。
// ContentHash 是原始文件内容的 sha256,保留给将来做幂等上传 / 向量回填用。
//
// ID 由 service 层用 snowflake 预分配后填入,不再走 MySQL AUTO_INCREMENT。
// 这样 Upload 能在 INSERT 之前就算出最终 oss_key,避免"INSERT 占位 → UPDATE 回填"的脏态窗口。
type Document struct {
	ID uint64 `gorm:"primaryKey"`
	// OrgID 同时进四条复合索引:
	//   - idx_documents_org_created     (org_id, created_at)   : List 分页排序。
	//   - idx_documents_org_hash        (org_id, content_hash) : 上传 dedup。
	//   - idx_documents_org_filename    (org_id, file_name)    : 覆盖更新 lookup。
	//   - idx_documents_org_source_type (org_id, source_type)  : FindBySourceRef(pull adapter 的 upsert 决策)。
	OrgID uint64 `gorm:"not null;index:idx_documents_org_created,priority:1;index:idx_documents_org_hash,priority:1;index:idx_documents_org_filename,priority:1;index:idx_documents_org_source_type,priority:1"`
	UploaderID  uint64    `gorm:"not null;index:idx_documents_uploader"`
	Title       string    `gorm:"size:256;not null"`
	MIMEType    string    `gorm:"size:128;not null"`
	FileName    string    `gorm:"size:256;not null;index:idx_documents_org_filename,priority:2"`
	SizeBytes   int64     `gorm:"not null"`
	OSSKey      string    `gorm:"size:512;not null;uniqueIndex:uk_documents_oss_key"`
	ContentHash string    `gorm:"size:64;not null;index:idx_documents_org_hash,priority:2"`
	// Source 区分"用户上传"(user)和"AI 生成回写"(ai-generated)等来源。
	// 默认 'user' 让现有行迁移时自动归类;新增来源常量见 document/const.go。
	// 单列索引足够,典型查询"列出 org X 的用户文档"走 (org_id, created_at) 主索引后
	// filter source 即可,不需要复合索引。
	//
	// **维护状态**:此字段为旧语义,T1 起被 SourceType + SourceRef 取代。仍保留做过渡期
	// 向后兼容,service 层写入时两套都填。新检索 / 逻辑应优先读 SourceType。
	Source string `gorm:"size:32;not null;default:'user';index:idx_documents_source"`

	// SourceType 一等公民类型标签(T1 引入,配合 pkg/sourceadapter 的 Adapter Registry 使用)。
	// 默认 'markdown_upload' 让存量 23 条文档自动归类为"用户 md 上传"。
	// 新源类型(git_file / jira_issue / image_caption / ...)在接入时扩展取值 + 对应 Adapter 实现。
	// 进两条索引:单列(诊断用)+ 和 OrgID 的复合(FindBySourceRef 高频查询)。
	SourceType string `gorm:"size:32;not null;default:'markdown_upload';index:idx_documents_source_type;index:idx_documents_org_source_type,priority:2"`

	// SourceRef type-specific 的定位符,shape 由对应 Adapter 自己约定。
	// 例如 git_file 存 {repo, path, commit};markdown_upload 可以是 nil 或 {uploader_channel:"web"}。
	// jsonb + GIN 索引让检索可按 ref 字段过滤("找这个 repo 下的所有 chunks" 等);先加列不急建 GIN,
	// 业务真用到再补(避免空列上的空索引维护开销)。
	SourceRef datatypes.JSON `gorm:"type:jsonb"`

	CreatedAt time.Time `gorm:"index:idx_documents_org_created,priority:2"`
	UpdatedAt time.Time
}

// TableName 返回数据库表名。
func (Document) TableName() string { return tableDocuments }

// DocumentChunk 文档切片 + 向量。存 Postgres(pgvector),和 Document 跨库关联:
//   - DocID 对应 documents.id,无 FK 约束,service 层负责级联(建/删同步)。
//   - OrgID 冗余存一份,所有检索按 org 过滤,避免跨库 join 做租户隔离。
//
// 生命周期:
//   - 上传链路先以 IndexStatus=pending + Embedding=nil 插入行,再异步/同步走 embedder 回填向量转 indexed;
//     失败时转 failed + IndexError,留待后续重试。
//   - 删文档走 DeleteChunksByDocID 批量清这张表。
//
// 索引:
//   - (DocID, ChunkIdx) 唯一:保证一篇文档内切片顺序不重;
//   - 单独的 DocID 索引:删文档时按 DocID 批量清;
//   - Embedding 上 HNSW cosine:ANN 检索,由 migration raw SQL 单独建;
//   - IndexStatus 索引:后台扫描补偿 failed / pending 行用。
type DocumentChunk struct {
	ID    uint64 `gorm:"primaryKey;autoIncrement"`
	DocID uint64 `gorm:"not null;uniqueIndex:uk_document_chunks_doc_idx,priority:1;index:idx_document_chunks_doc"`
	// OrgID 进两条索引:单列(租户隔离)+ 和 ContentType 的复合(类型过滤检索高频组合)。
	OrgID          uint64 `gorm:"not null;index:idx_document_chunks_org;index:idx_document_chunks_org_ctype,priority:1"`
	ChunkIdx       int    `gorm:"not null;uniqueIndex:uk_document_chunks_doc_idx,priority:2"`
	Content        string `gorm:"type:text;not null"`
	ContentHash    string `gorm:"size:64;not null"`
	TokenCount     int    `gorm:"not null;default:0"`
	EmbeddingModel string `gorm:"size:128;not null;default:''"`
	// Embedding:nil 表示还没算出来(status=pending)或失败(status=failed);gorm tag 里的
	// 维度必须与 document.ChunkEmbeddingDim 严格一致,改维度 = schema 破坏性迁移。
	Embedding   *pgvector.Vector `gorm:"type:vector(1536)"`
	IndexStatus string           `gorm:"size:16;not null;default:'pending';index:idx_document_chunks_status"`
	IndexError  string           `gorm:"type:text;default:''"`

	// ─── T1.3 / T1.4 结构化字段,KB Roadmap Phase 0 加入 ───

	// Metadata 通用结构化 metadata。字段本身不规定 schema —— 由 chunker profile / source adapter
	// 写入 type-specific 字段(如 {heading_path:[...], source_type:"git", tags:[...], author:...})。
	// jsonb + GIN 索引支持任意 @> 包含查询,检索层做多维过滤的统一入口。
	// 允许 NULL(未填 = 无 metadata),避免给所有现存调用点都加 `Metadata: JSON("{}")`;检索时 NULL
	// 行对任何 `@>` 过滤都是 no-match,行为一致。PG 的 ADD COLUMN DEFAULT 会给存量行填 '{}'。
	Metadata datatypes.JSON `gorm:"type:jsonb;default:'{}'"`

	// ParentChunkID 指向父 chunk(structure-aware 切分时 heading → 子段 的父节点);NULL = root chunk。
	// T1.3 parent-child 召回:小粒度子 chunk 给精度,返回父 chunk 给上下文。
	ParentChunkID *uint64 `gorm:"index:idx_document_chunks_parent"`

	// ChunkLevel 结构深度 0=root,1=h1 下内容,2=h2 下内容...。Markdown 等结构化内容有意义,
	// 纯文本/代码写 0 即可。用来约束父子层级范围,避免跨层级乱认亲。
	ChunkLevel int16 `gorm:"not null;default:0"`

	// ContentType chunk 内容类型:text/code/table/heading/list...由 chunker profile 写入。
	// 检索层可按类型过滤(如"只搜代码"),也是 (OrgID, ContentType) 复合索引的第二列。
	ContentType string `gorm:"size:32;not null;default:'text';index:idx_document_chunks_org_ctype,priority:2"`

	// ChunkerVersion 切分器版本(v1=旧递归分隔符,v2=T1.3 markdown 结构...)。
	// 支持滚动升级:新旧版本 chunk 可共存,检索层可灰度选择读哪一版。
	ChunkerVersion string `gorm:"size:16;not null;default:'v1'"`

	// TsvTokens BM25 通路的分词结果:Go 侧 gse 分词后用空格连接的词流(如 "支付 模块 架构")。
	// 是**可写字段**;content_tsv(generated column,由 PG 自动从 tsv_tokens 算)不在此 struct。
	// NULL / 空串时 BM25 通路自然匹不到,不会报错 —— 等效"这条 chunk 暂不走 BM25 搜"。
	// GORM AutoMigrate 会加这一列(text 类型),但 content_tsv 的 GENERATED 定义必须走 raw SQL。
	TsvTokens string `gorm:"column:tsv_tokens;type:text;default:''"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName 返回数据库表名。
func (DocumentChunk) TableName() string { return tableDocumentChunks }
