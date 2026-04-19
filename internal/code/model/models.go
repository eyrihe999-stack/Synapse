// Package model code 模块数据模型。
//
// 四张表全部在 Postgres 一个库(和 document 跨 MySQL+PG 不同):
//   - code_repositories    :仓库元信息(per org)
//   - code_files           :文件元信息,不含 content
//   - code_file_contents   :CAS 存储,按 blob_sha 去重(同一 blob 跨 repo 只存一份)
//   - code_chunks          :函数级切片 + 向量
//
// 和 document 的差异:
//   - document 把原文放 OSS,元信息 MySQL,chunks PG,跨三家做 saga 一致性
//   - code 文件天然小(99% <200KB),直接放 PG + TOAST 压缩,一库事务,一致性简单
//
// schema 设计参照 document_chunks(pgvector + HNSW + index status 状态机)保持风格一致,
// 但不做 BM25 tsv(代码检索 MVP 纯向量;要做 BM25 得单独写驼峰/下划线分词,留二期)。
package model

import (
	"time"

	"github.com/pgvector/pgvector-go"
)

const (
	tableCodeRepositories  = "code_repositories"
	tableCodeFiles         = "code_files"
	tableCodeFileContents  = "code_file_contents"
	tableCodeChunks        = "code_chunks"
)

// CodeRepository 外部代码仓库在 Synapse 的本地镜像元信息。每次 sync 刷新字段。
//
// 唯一键:(org_id, provider, external_project_id)。external_project_id 是 provider 侧
// 数字 ID(GitLab int64 project id),比 path_with_namespace 稳定 —— 后者会随 rename/transfer 变。
// Sync 流程按此三元组 upsert,不会因为 repo 改名而创建重复行。
//
// **索引类型必须是 UNIQUE**:repository.Upsert 走 ON CONFLICT (org_id, provider, external_project_id),
// 普通 index 会让 PG 报 42P10(no unique or exclusion constraint matching)。
type CodeRepository struct {
	ID    uint64 `gorm:"primaryKey;autoIncrement"`
	OrgID uint64 `gorm:"not null;uniqueIndex:uk_code_repos_org_provider_extid,priority:1;index:idx_code_repos_org_created,priority:1"`

	// Provider 代码托管平台标识。当前只有 "gitlab";加 GitHub 时直接追加取值。
	Provider string `gorm:"size:32;not null;uniqueIndex:uk_code_repos_org_provider_extid,priority:2"`

	// ExternalProjectID provider 侧的项目数字 ID。GitLab project.id。
	// string 而非 int64:预留给将来某 provider 返非数字 ID(如 gitea 的 owner/name 组合),
	// 也方便 jsonb-ish 的扩展性。查询时按字符串精确匹配,不做数值范围扫。
	ExternalProjectID string `gorm:"size:64;not null;uniqueIndex:uk_code_repos_org_provider_extid,priority:3"`

	PathWithNamespace string `gorm:"size:255;not null"` // 如 "team/group/repo-name"
	DefaultBranch     string `gorm:"size:128;not null"` // sync 时按此分支拉文件树
	WebURL            string `gorm:"size:512"`          // 让前端展示时能点跳回 GitLab

	// LastSyncedCommit 最近一次完整同步时 default_branch 的 HEAD commit。
	// 未来做增量 diff 时:当前 HEAD 对比 LastSyncedCommit 只拉变更的文件。
	// MVP 全量扫,该字段只记录不消费。
	LastSyncedCommit string `gorm:"size:64;default:''"`

	// LastSyncedAt 最近一次 sync 完成的时间点。
	// 前端按 org 列 repos 时展示"上次同步",也给诊断用。
	LastSyncedAt *time.Time

	// Archived provider 侧是否归档;归档 repo ingest service 跳过同步,但不删本地 chunks
	// (保留检索命中能力,失败概率太小不值得清理)。
	Archived bool `gorm:"not null;default:false"`

	CreatedAt time.Time `gorm:"index:idx_code_repos_org_created,priority:2"`
	UpdatedAt time.Time
}

// TableName 固定表名。
func (CodeRepository) TableName() string { return tableCodeRepositories }

// CodeFile 单个文件的元信息。不含内容 —— 内容按 blob_sha 去 code_file_contents 读。
//
// 查询模式:
//   - 列 repo 下所有文件    :WHERE repo_id 走 idx_code_files_repo_path
//   - 按 blob_sha 查是否已存 :WHERE blob_sha 走 idx_code_files_blob(embed 短路决策)
//   - 按 org 检索           :WHERE org_id 走 idx_code_files_org_repo(跨 repo 的过滤场景)
type CodeFile struct {
	ID     uint64 `gorm:"primaryKey;autoIncrement"`
	RepoID uint64 `gorm:"not null;uniqueIndex:uk_code_files_repo_path,priority:1;index:idx_code_files_org_repo,priority:2"`
	OrgID  uint64 `gorm:"not null;index:idx_code_files_org_repo,priority:1"` // 冗余存一份,避免检索时跨 code_repositories join 做 org 隔离

	// Path 相对 repo root 的完整路径,如 "internal/foo/bar.go"。和 RepoID 构成唯一键。
	Path string `gorm:"size:1024;not null;uniqueIndex:uk_code_files_repo_path,priority:2"`

	// Language chunker 识别出的语言标签(小写),"go" / "python" / "typescript" / "unknown"。
	// 按扩展名映射,不依赖 content sniffing(MVP 简单)。检索层可按此 filter。
	Language string `gorm:"size:32;not null;default:''"`

	// SizeBytes 原文字节数。冗余存(内容也在 code_file_contents),方便列表展示不用 join。
	SizeBytes int64 `gorm:"not null"`

	// BlobSHA 按 provider 原生 blob hash 填(GitLab git blob sha1)。
	// 内容指纹:跨 repo 的同一份内容会共享同一条 code_file_contents 行;
	// sync 时对比 DB 里现有的 BlobSHA 可以判断"文件变没变",没变就跳过 chunk 重建。
	BlobSHA string `gorm:"size:64;not null;index:idx_code_files_blob"`

	// LastCommitID 最近改动此文件的 commit SHA(provider 返回)。诊断 + 未来的"按 commit 回溯"。
	LastCommitID string `gorm:"size:64;default:''"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName 固定表名。
func (CodeFile) TableName() string { return tableCodeFiles }

// CodeFileContent 按 blob_sha 去重的 CAS 存储。内容可能被多个 code_files 行引用
// (fork、vendor 等场景下同一个 blob 跨 repo)。
//
// 不建物理 FK:code_files.blob_sha 只做逻辑引用,让 sync 流程能独立 upsert 两张表
// (ON CONFLICT DO NOTHING 在 content 上,不依赖 files 表已写入的顺序)。
//
// GC 策略:MVP 不清理。存储成本 << 一致性风险的代价。以后需要回收时走周期任务
// `DELETE FROM code_file_contents WHERE blob_sha NOT IN (SELECT blob_sha FROM code_files)`。
type CodeFileContent struct {
	// BlobSHA 主键。同一内容只存一行。
	BlobSHA string `gorm:"primaryKey;size:64"`

	// Size 原文字节数。冗余存,诊断用。
	Size int64 `gorm:"not null"`

	// Content 原始字节。PG bytea + TOAST 自动压缩透明。
	// 上限由 adapter 层 MaxFileBytes(2MB)兜底,不会有超大文件进来。
	Content []byte `gorm:"type:bytea;not null"`

	CreatedAt time.Time
}

// TableName 固定表名。
func (CodeFileContent) TableName() string { return tableCodeFileContents }

// CodeChunk 函数级切片 + 向量。和 CodeFile 一对多,FileID 关联。
//
// 生命周期(学 document_chunks):
//   - Ingest 先插入 pending + Embedding=nil
//   - embedder 成功  → UpdateChunkEmbedding 转 indexed
//   - embedder 失败  → MarkChunkFailed 转 failed + IndexError
//   - 文件内容变化   → SwapChunksByFileID 原子替换整个文件的 chunks
//   - 删 repo / file → DeleteChunksByFileID / DeleteChunksByRepoID 批量清
//
// 索引:
//   - (file_id, chunk_idx) 唯一:保证文件内顺序不重
//   - (file_id) 单列:删文件时扫
//   - (org_id, language) 复合:按语言筛选的检索场景
//   - (index_status) 单列:后台扫补偿 pending/failed
//   - embedding 上 HNSW cosine:ANN,由 migration raw SQL 单独建
type CodeChunk struct {
	ID     uint64 `gorm:"primaryKey;autoIncrement"`
	FileID uint64 `gorm:"not null;uniqueIndex:uk_code_chunks_file_idx,priority:1;index:idx_code_chunks_file"`
	RepoID uint64 `gorm:"not null;index:idx_code_chunks_repo"` // 冗余,删 repo 时批量按 repo_id 清
	OrgID  uint64 `gorm:"not null;index:idx_code_chunks_org_lang,priority:1"`

	// ChunkIdx 文件内 chunk 顺序(0-based)。按源码出现顺序排,让同一文件的 chunks
	// 即使被分别召回也能按此字段重建原始阅读顺序。
	ChunkIdx int `gorm:"not null;uniqueIndex:uk_code_chunks_file_idx,priority:2"`

	// SymbolName 函数 / 方法 / 类的名字。preamble 和 unparsed chunk 留空。
	// 短文本精确匹配的高价值字段 —— 即使不做 BM25,也能用 LIKE '%name%' 做简易命中。
	SymbolName string `gorm:"size:255;not null;default:''"`

	// Signature 完整签名字符串(带参数列表 + 返回类型)。长度上限 1024,超长截断。
	// 给 agent 看"这个函数接受什么"用,不参与检索打分。
	Signature string `gorm:"size:1024;not null;default:''"`

	// Language 和 CodeFile.Language 一致,冗余写避免检索时 join。
	Language string `gorm:"size:32;not null;default:'';index:idx_code_chunks_org_lang,priority:2"`

	// ChunkKind 见 code.ChunkKind* 常量。
	ChunkKind string `gorm:"size:16;not null;default:'function'"`

	// LineStart / LineEnd 源文件中的行号(1-based,闭区间)。让 agent 回答能附带
	// "这个函数在 foo.go:42-87" 的精确定位。
	LineStart int `gorm:"not null;default:0"`
	LineEnd   int `gorm:"not null;default:0"`

	// Content chunk 正文。对 function chunk 来说是完整的函数体;对 preamble 是顶部
	// imports + 文件级注释;对 unparsed 是启发式切出来的一段。
	Content string `gorm:"type:text;not null"`

	// TokenCount 近似 token 数(按字符粗估),供 embedder batch 和检索 limit 参考。
	TokenCount int `gorm:"not null;default:0"`

	// EmbeddingModel 算这个向量时用的 model tag(如 "azure/text-embedding-3-small")。
	// 未来换模型时先按 tag 过滤出旧行重 embed,再切流量。
	EmbeddingModel string `gorm:"size:128;not null;default:''"`

	// Embedding nil 表示还没算出(pending)或失败(failed)。HNSW 索引在 migration 单独建,
	// 维度必须和 code.ChunkEmbeddingDim 严格一致。
	Embedding *pgvector.Vector `gorm:"type:vector(1536)"`

	// IndexStatus 见 code.ChunkIndexStatus* 常量。
	IndexStatus string `gorm:"size:16;not null;default:'pending';index:idx_code_chunks_status"`

	// IndexError failed 状态下的错误摘要。超长截断(由 repo 层执行)。
	IndexError string `gorm:"type:text;default:''"`

	// ChunkerVersion 切分器版本 tag。升级 chunker 时灰度切流量用,语义和 document 的
	// chunker_version 对称。当前 v1 = tree-sitter 首发,v2 留给未来 AST 策略调整。
	ChunkerVersion string `gorm:"size:16;not null;default:'v1'"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName 固定表名。
func (CodeChunk) TableName() string { return tableCodeChunks }
