// Package document 文档存储模块:上传到 OSS、元数据存 MySQL、供组织管理员 CRUD。
//
// 当前只做 markdown 文档的存储网关(上传/列表/下载/删除/改标题),
// 向量检索 / 切块 / embedding 的能力暂时撤除,后续从 OSS 原文回填即可。
package document

import "github.com/eyrihe999-stack/Synapse/internal/organization"

// ─── 表名 ─────────────────────────────────────────────────────────────────────

// TableDocuments 文档主表(MySQL)。
const TableDocuments = "documents"

// TableDocumentChunks 文档切片表(Postgres + pgvector)。
// 与 TableDocuments 跨库,无 FK 约束,靠 service 层保证一致性。
const TableDocumentChunks = "document_chunks"

// ─── 向量库 ──────────────────────────────────────────────────────────────────

// ChunkEmbeddingDim 向量列维度,必须与 config.embedding.text.model_dim 一致。
// 改这里 = schema 迁移,需要重新索引所有 chunk。
const ChunkEmbeddingDim = 1536

// Index 状态枚举:chunk.index_status 字段取值。
// pending:行已存在但 embedding 未写入;indexed:embedding 已填;failed:embedding 失败(index_error 有详情)。
const (
	ChunkIndexStatusPending = "pending"
	ChunkIndexStatusIndexed = "indexed"
	ChunkIndexStatusFailed  = "failed"
)

// ─── 文档来源 ────────────────────────────────────────────────────────────────

// Document.Source 字段枚举(旧)。区分"用户手动上传"和"AI 生成回写",
// 防止 AI 用自己的输出再参与生成,形成回环污染。
//
// 新增生成类来源(如不同 generator kind)时补常量即可;长度上限 32 字符,
// 对齐 schema(见 model.Document.Source gorm tag)。
//
// **注意**:此字段将逐步被 `SourceType` + `SourceRef`(见下)取代 —— 当前仍保留是为了
// 向后兼容存量读写路径。新代码应优先使用 SourceType。
const (
	DocSourceUser        = "user"
	DocSourceAIGenerated = "ai-generated"
)

// ─── 一等公民:SourceType + SourceRef(T1 基础抽象)───────────────────────────
//
// 旧 `Document.Source` 把"谁创建"和"什么类型"混在一起(user 意味着 HTTP 上传 + 一定是 md,
// ai-generated 同理)。当我们要接入 code / bug / image / DB 等多源时,这个单字段表达不动。
//
// 新模型:
//   - SourceType  "类型"—— 决定 Adapter / chunker profile / source_ref shape 的分派键
//   - SourceRef   "定位符"—— type-specific,jsonb 存进 documents.source_ref 列
//   - CreatedBy   仍靠 uploader_id(uint64);未来 agent 写回时 uploader_id = agent 的 user 映射
//
// 见 pkg/sourceadapter 的 Adapter 接口与 Registry。

// SourceType 常量:已知的源类型标识。Adapter 实现的 Type() 必须返这里的值之一(或新增值后补常量)。
const (
	// SourceTypeMarkdownUpload 用户通过 HTTP multipart 上传的 markdown 文件。push-based,无 Sync 逻辑。
	// 这是 T1 唯一存在的源类型;所有 23 条存量文档迁移后都归类为此。
	SourceTypeMarkdownUpload = "markdown_upload"

	// SourceTypeAINote Agent / generator 输出的回写笔记。防 feedback loop(检索时可按 type 过滤)。
	// 对应旧 DocSourceAIGenerated。
	SourceTypeAINote = "ai_note"

	// 以下是未来 T2 各源的规划占位,adapter 落地时实现 Type() 返回对应字符串。
	// SourceTypeGitFile     = "git_file"     // T2.1
	// SourceTypeJiraIssue   = "jira_issue"   // T2.2
	// SourceTypeImageCaption = "image_caption" // T2.3
	// SourceTypeDBSchema    = "db_schema"    // T2.4
)

// ─── 默认值与上限 ────────────────────────────────────────────────────────────

const (
	// DefaultPageSize 列表接口默认分页大小。
	DefaultPageSize = 20
	// MaxPageSize 列表接口最大分页大小。
	MaxPageSize = 100

	// MaxTitleLength 文档标题最大字符数。
	MaxTitleLength = 256

	// MaxQueryLength 列表接口搜索词最大字符数(超长时在 service 层截断,不报错)。
	MaxQueryLength = 128

	// MaxUploadBodyBytes 上传路由单请求 body 上限(20MB,给 10MB 文件 + multipart 开销留余量)。
	MaxUploadBodyBytes = 20 * 1024 * 1024

	// MaxPrecheckBatch 单次 precheck 请求最多评估多少个候选文件。
	// 每个候选 2 次 DB 查询(hash + filename),50 → 100 query 上限,小且可控。
	MaxPrecheckBatch = 50

	// DefaultSemanticTopK 语义搜索默认返回 20 个文档(按最相关 chunk 排序 + 去重后)。
	DefaultSemanticTopK = 20
	// MaxSemanticTopK 语义搜索单次最多返 50 个文档;每个 doc 需向 pg 多取 chunks 做 dedup。
	MaxSemanticTopK = 50
	// SemanticSnippetChars 返给前端的"最匹配片段"按 rune 数截断到此长度 + "...";
	// 典型 chunker chunk 长度 500-1500,截到 200 给用户一瞥的上下文即可。
	SemanticSnippetChars = 200
)

// ─── 搜索模式 ────────────────────────────────────────────────────────────────

// 前端 GET /documents?mode= 的取值。fuzzy 是默认,走 MySQL LIKE;semantic 走 pgvector cosine。
const (
	SearchModeFuzzy    = "fuzzy"
	SearchModeSemantic = "semantic"
)

// ─── Chunk 搜索置信度 ────────────────────────────────────────────────────────

// ListChunksResponse.Confidence 取值。空串("")表示调用方未开启 gate,语义是"向后兼容,不标注"。
const (
	SearchConfidenceHigh = "high"
	SearchConfidenceNone = "none"
)

// PerDocCapBuffer MaxPerDoc 启用时,召回池相对 topK 的放大倍率。
// 假设最坏情况同一 doc 占满 top 位,需要足够候选让 cap 后还能凑齐 topK。
// 3x 经验值:对 MaxPerDoc=2, topK=10 意味着召回 30 条 —— 即便前 10 全来自 3 个文档也能继续挑到替补。
const PerDocCapBuffer = 3

// ─── Precheck actions ────────────────────────────────────────────────────────

// Precheck 对每个候选文件给出四种 action 之一,前端按此分流 UI:
//
//	create    : 全新,默认勾选,等用户点"确认上传"才真上传。
//	overwrite : 同文件名已存在但内容不同,默认勾选 + 警示,上传后会覆盖。
//	duplicate : 完全相同内容已存在,不可勾选上传,显示已有文档信息。
//	reject    : 本来就会被 Upload 拒掉(超限/类型不支持/空文件),置灰 + 原因。
const (
	PrecheckActionCreate    = "create"
	PrecheckActionOverwrite = "overwrite"
	PrecheckActionDuplicate = "duplicate"
	PrecheckActionReject    = "reject"
)

// ─── Precheck reason codes ──────────────────────────────────────────────────

// 前端按 reason_code 做 i18n 文案。后端只给码,不给文案,避免文案改动牵动后端。
const (
	PrecheckReasonNewFile                = "new_file"
	PrecheckReasonSameFilenameNewContent = "same_filename_new_content"
	PrecheckReasonIdenticalContentExists = "identical_content_exists"
	PrecheckReasonFileTooLarge           = "file_too_large"
	PrecheckReasonMIMEUnsupported        = "mime_unsupported"
	PrecheckReasonEmptyFile              = "empty_file"
	PrecheckReasonInvalidContentHash     = "invalid_content_hash"
)

// ─── 权限点别名 ─────────────────────────────────────────────────────────────
// 权威定义在 organization 模块,这里起别名省一次跨包引用。
const (
	PermDocumentRead   = organization.PermDocumentRead
	PermDocumentWrite  = organization.PermDocumentWrite
	PermDocumentDelete = organization.PermDocumentDelete
)
