// Package service document 模块业务编排层。
//
// 存储网关 + 向量索引编排:Upload / Get / List / Delete / UpdateMetadata / Download。
// 所有方法以 orgID 为第一参数,租户隔离由上游中间件做 org 解析 + 鉴权。
//
// 一致性约定(与 repository 层共同维护):
//   - Upload  跨库:MySQL INSERT → OSS PUT → PG INSERT chunks,任一下游失败补偿上游;要么整单落成,要么整单不存在。
//   - Delete  以 MySQL 的 DELETE commit 为权威;其后 PG/OSS 只做最终一致性清理,失败不影响用户视角。
//   - Update  用 SELECT FOR UPDATE + UPDATE 的事务闭包,消除 lost update。
//   - List    用事务快照包 Count + Find,消除分页漂移。
//
// 索引依赖(ChunkRepo / Embedder / Chunker)三者"全有或全无":
//   - 全有:索引启用,Upload 同步 embed,失败走补偿或降级写 failed 行。
//   - 全无:索引降级关闭,Upload 不生成 chunks。Search 工具(后续步骤)对应地返回空。
//   - 缺一:构造期 fatal,防"跛脚"状态跑到 runtime 才爆。
package service

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/eyrihe999-stack/Synapse/internal/document"
	"github.com/eyrihe999-stack/Synapse/internal/document/dto"
	"github.com/eyrihe999-stack/Synapse/internal/document/model"
	"github.com/eyrihe999-stack/Synapse/internal/document/repository"
	"github.com/eyrihe999-stack/Synapse/pkg/chunker"
	"github.com/eyrihe999-stack/Synapse/pkg/embedding"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"github.com/eyrihe999-stack/Synapse/pkg/ossclient"
	"github.com/eyrihe999-stack/Synapse/pkg/reranker"
	"github.com/eyrihe999-stack/Synapse/pkg/tokenizer"
)

// Config document 模块 service 层配置。
type Config struct {
	MaxFileSizeBytes int64
	AllowedMIMETypes map[string]struct{}
}

// DefaultConfig 返回一份保底配置:10MB 单文件上限,白名单允许 markdown / text / x-markdown。
// 调用方通常基于 config.DocumentConfig 转换得到自定义配置,仅在缺配置时回退到此默认。
func DefaultConfig() Config {
	return Config{
		MaxFileSizeBytes: 10 * 1024 * 1024,
		AllowedMIMETypes: map[string]struct{}{
			"text/markdown":   {},
			"text/plain":      {},
			"text/x-markdown": {},
		},
	}
}

// ─── OrgPort:document → organization 的跨模块接口 ───────────────────────────

// OrgInfo document 模块需要的 org 快照。
type OrgInfo struct {
	ID          uint64
	Slug        string
	DisplayName string
	Status      string
}

// OrgMembership 成员快照,权限集用于上游中间件判断。
type OrgMembership struct {
	OrgID       uint64
	UserID      uint64
	RoleName    string
	Permissions map[string]struct{}
}

// Has 判断是否持有某权限点。
func (m *OrgMembership) Has(perm string) bool {
	if m == nil {
		return false
	}
	_, ok := m.Permissions[perm]
	return ok
}

// OrgPort 访问 organization 模块的端口。
type OrgPort interface {
	GetOrgBySlug(ctx context.Context, slug string) (*OrgInfo, error)
	GetOrgByID(ctx context.Context, orgID uint64) (*OrgInfo, error)
	GetMembership(ctx context.Context, orgID, userID uint64) (*OrgMembership, error)
	// GetUserDisplayName 按 userID 查 users.display_name,供 DTO 回填上传者昵称。
	// 用户不存在或查询失败时返回空串,由调用方 fallback。
	GetUserDisplayName(ctx context.Context, userID uint64) string
}

// ─── UploadInput 与 DocumentService 接口 ─────────────────────────────────────

// UploadInput 上传文档的输入。Content 优先级高于 ContentReader。
//
// TargetDocID 非 0 时走显式覆盖:Upload 会定位这条 doc 并替换内容 + 元数据。
// 为 0 时永远走 dedup(命中幂等返回)或新建,**绝不按文件名自动覆盖**。
// 调用方若要"按文件名覆盖"的语义,需自行先查 Precheck 拿到候选 doc_id 再传回来。
//
// Source 为文档来源标签(旧),取值见 document.DocSource* 常量。空串 → 归类为 "user"。
// 仅在"新建 doc"路径下生效;覆盖路径保留被覆盖目标的原有 source 不变
// (避免出现"AI 覆盖掉 user 文档,内容变了但 source 还是 user" 的矛盾态)。
//
// SourceType / SourceRef 为 T1 引入的一等公民来源标签,配合 pkg/sourceadapter 使用。
// SourceType 空 → 归类为 "markdown_upload"(HTTP 上传默认)。SourceRef 是 jsonb,nil = 无定位符。
// 和 Source 同样只在新建路径生效,覆盖路径保留原值。
type UploadInput struct {
	OrgID         uint64
	UploaderID    uint64
	Title         string
	FileName      string
	MIMEType      string
	Content       []byte
	ContentReader io.Reader
	TargetDocID   uint64
	Source        string
	SourceType    string
	SourceRef     []byte // 原始 jsonb bytes;nil = null 列
}

// DownloadResult 下载输出。Body 由调用方负责 Close。
type DownloadResult struct {
	FileName  string
	MIMEType  string
	SizeBytes int64
	Body      io.ReadCloser
}

// DocumentService 对外接口。
type DocumentService interface {
	Upload(ctx context.Context, in UploadInput) (*dto.DocumentResponse, error)
	Get(ctx context.Context, orgID, docID uint64) (*dto.DocumentResponse, error)
	// List 支持按 title / file_name 做 LIKE 模糊匹配;query 为空时列出全部。
	List(ctx context.Context, orgID uint64, query string, page, size int) (*dto.ListDocumentsResponse, error)
	Delete(ctx context.Context, orgID, docID uint64) error
	UpdateMetadata(ctx context.Context, orgID, docID uint64, req dto.UpdateMetadataRequest) (*dto.DocumentResponse, error)
	Download(ctx context.Context, orgID, docID uint64) (*DownloadResult, error)

	// PrecheckUpload 对一批候选文件做"预演":返回每个候选的 Action(create/overwrite/duplicate/reject)
	// 与 ReasonCode,不写任何库。前端据此分流 UI,只对 action=create/overwrite 的文件发起真正 Upload。
	//
	// 该方法是"建议性"的:从 precheck 到真 Upload 之间另外的请求可能改变状态,Upload 的最终三分支是权威。
	PrecheckUpload(ctx context.Context, orgID uint64, candidates []PrecheckCandidate) ([]PrecheckResult, error)

	// GetUploadConfig 返回上传约束 + 语义搜索可用性,供前端预过滤 UI + 能力探测。
	// 稳定小值,调用方可以自己缓存。
	GetUploadConfig() UploadConfig

	// SemanticSearch 按语义相似度检索本 org 的文档。流程:
	//   1. embed query → 2. pgvector ANN top_k×5 chunks → 3. 按 doc_id 去重取最优 →
	//   4. JOIN MySQL 过滤孤儿 + 补 metadata → 5. 返 top_k 篇文档,每篇带 similarity + matched_snippet。
	//
	// 索引未启用(chunkRepo/embedder/chunker 任一为 nil)时返 ErrDocumentIndexFailed。
	// query 空返空列表(非错);过长 query 按 MaxQueryLength 截断。
	SemanticSearch(ctx context.Context, orgID uint64, query string, topK int) (*dto.ListDocumentsResponse, error)

	// SearchChunks chunk 级语义检索,不 dedup 同 doc 的多个 chunk。
	//
	// 与 SemanticSearch 的差别:后者返"命中文档列表",一篇文档 → 最佳 snippet;
	// 本方法返"命中片段列表",一段原文 → 一条结果,保留 [doc_id:chunk_idx] 作为引用定位。
	// 供 generator agentic RAG 使用 —— 每条 PRD 断言都要带精确引用。
	//
	// opts 可选:传 nil 行为和原签名一致(纯 org + indexed 过滤)。
	// 用 Options struct 而不是继续加参数,是给 T1.1(hybrid) / T1.2(rerank)留 stable 扩展位。
	//
	// 孤儿过滤:chunk 对应 MySQL doc 已删时跳过(和 SemanticSearch 同策略)。
	// 失败语义同 SemanticSearch:索引未启用 → ErrDocumentIndexFailed;query 空返空列表。
	SearchChunks(ctx context.Context, orgID uint64, query string, topK int, opts *SearchChunksOptions) (*dto.ListChunksResponse, error)
}

// RetrievalMode 控制 SearchChunks 走哪条召回路径。
//
// 默认(空串 / 未设置)语义:
//   - tokenizer 已配置 → 走 hybrid(向量 + BM25,RRF 融合)
//   - tokenizer 未配置 → 走 vector(BM25 拿不到分词无法工作,降级)
// 显式传值用来 A/B 对比或 debug,evalretrieval 的 --mode flag 直通此处。
type RetrievalMode string

const (
	// RetrievalDefault 让 service 自己选(有 tokenizer → hybrid,没有 → vector)。
	RetrievalDefault RetrievalMode = ""
	// RetrievalVector 只走向量 ANN,等同 T1.1 之前的行为。
	RetrievalVector RetrievalMode = "vector"
	// RetrievalBM25 只走 BM25 字面召回;tokenizer 不存在时返空列表(不报错)。
	RetrievalBM25 RetrievalMode = "bm25"
	// RetrievalHybrid 两路并发召回 + RRF 融合。tokenizer 不存在时自动降级到 vector。
	RetrievalHybrid RetrievalMode = "hybrid"
)

// SearchChunksOptions 向后兼容扩展 SearchChunks 行为。nil = 全默认。
//
// 已落:Filter(T1.4)、Mode(T1.1)、Rerank(T1.2)、MaxPerDoc + 阈值门(T1.X 多样性/置信度)。
// 未来扩展位:ReturnGranularity(子/父 chunk)、CandidateMultiplier(手动调召回倍率)。
type SearchChunksOptions struct {
	// Filter 结构化过滤条件。nil 或空 Filter = 只过 org + indexed(保持旧行为)。
	Filter *repository.ChunkSearchFilter
	// Mode 选召回策略。空串 = RetrievalDefault(默认 vector,T1.1 完工日评估决定)。
	Mode RetrievalMode
	// Rerank 启用二阶段 cross-encoder 重排。只在 service 注入了 Reranker 时实际生效;
	// 未注入时 log warn 但不 error,返回 retrieve 原顺序(降级透明)。
	Rerank bool

	// MaxPerDoc 限制每个 doc_id 在最终结果里最多出现多少条 chunk。0 = 不限(保持旧行为)。
	//
	// 解决什么:RRF + rerank 会让同文档相邻 chunk 扎堆 top-K —— agent context 被重复内容吃掉,
	// top-10 实际只覆盖 1-2 个主题。设 2 或 3 让 top-K 覆盖更多独立来源。
	//
	// 实现:在 rerank(或 retrieve)排序后、截 topK 前应用。为了保证过滤后仍有 topK 条,
	// 召回池会自动放大 —— 调用方不用手调 CandidateMultiplier。
	MaxPerDoc int

	// MinRerankScore cross-encoder rerank 阶段的置信度阈值。nil = 不过滤。
	//
	// 仅在 Rerank = true 且 service 注入了 Reranker 时生效。低于阈值的 chunk 被直接丢弃。
	// 过滤后没有任何条剩下时,响应里 Confidence 字段会是 "none",供 agent 识别"KB 里没有相关内容"。
	//
	// 参考值(BGE-reranker-v2-m3,raw_scores):
	//   - 0.0 ≈ sigmoid 0.5,"coin flip",过滤掉明显不相关的内容
	//   - 1.0 ≈ sigmoid 0.73,只保留较强相关
	// 不同 reranker 实现量纲不同(Cohere [0,1] / Voyage [0,1]),阈值语义由调用方按实现调。
	MinRerankScore *float32

	// MinSimilarity vector / hybrid 通路的 similarity 阈值(1 - cosine_distance/2,[0,1])。nil = 不过滤。
	//
	// Rerank 关闭时的降级 gate —— 向量分数比 rerank 分数噪声大,阈值要更宽松。
	// 同样影响 Confidence:过滤后空集 → "none"。
	//
	// 参考值:0.3 过滤"完全无关",0.5 过滤"弱相关"。具体门槛跑一遍 evalretrieval 看分布决定。
	MinSimilarity *float32
}

// PrecheckCandidate Precheck 输入单元。ContentHash 由客户端本地算 sha256。
type PrecheckCandidate struct {
	FileName    string
	SizeBytes   int64
	MIMEType    string
	ContentHash string
}

// PrecheckResult 单个候选的预检结果。
//
// Existing 仅在 Action = duplicate 时非 nil,代表 hash 命中的那条已存在文档。
// ExistingList 仅在 Action = overwrite 时非空,代表**所有同名候选**(可能 ≥1 条);
// 前端据此让用户选"覆盖哪一条"或"作为新文档上传"。
type PrecheckResult struct {
	FileName     string
	Action       string
	ReasonCode   string
	Existing     *dto.DocumentResponse
	ExistingList []dto.DocumentResponse
}

// UploadConfig 上传约束 + 能力探测快照,给 GET /documents/config 使用。
// SemanticSearchEnabled = 索引三元依赖是否齐备,前端据此灰掉语义搜索开关。
type UploadConfig struct {
	MaxFileSizeBytes      int64
	AllowedMIMETypes      []string
	SemanticSearchEnabled bool
}

// ─── helpers ────────────────────────────────────────────────────────────────

// documentToDTO model → dto 响应体转换。
//
// Source 空串映射为 "user"(历史行迁移前可能为空;schema 默认值兜底后应为 "user")。
// 这一步是防御性转换,保证前端永远看到明确的 source 取值。
func documentToDTO(d *model.Document) dto.DocumentResponse {
	source := d.Source
	if source == "" {
		source = document.DocSourceUser
	}
	return dto.DocumentResponse{
		ID:         d.ID,
		OrgID:      d.OrgID,
		UploaderID: d.UploaderID,
		Title:      d.Title,
		MIMEType:   d.MIMEType,
		FileName:   d.FileName,
		SizeBytes:  d.SizeBytes,
		Source:     source,
		CreatedAt:  d.CreatedAt.Unix(),
		UpdatedAt:  d.UpdatedAt.Unix(),
	}
}

// Dependencies service 构造期需要的所有外部依赖,传 Dependencies struct 避免 8-参位置构造易读性差。
//
// ChunkRepo / Embedder / Chunkers 是索引能力的三元组,**全有或全无**:
//   - 全有 → 索引启用。
//   - 全无 → 索引关闭,Upload 正常完成但不写 chunks。
//   - 任一单独 nil → 构造返回错误(见 New 内校验)。
//
// Chunkers 是 Registry(按 mime/filename 路由到具体 Profile),T1.3 起取代旧的单一 Chunker 接口。
//
// Tokenizer 为 T1.1 BM25 通路用于 ingest 时把 chunk 内容分词后写 tsv_tokens 列(PG 自动算 content_tsv),
// 以及 search 时把 query 分词后喂 plainto_tsquery。**可选**:nil = 不启用 BM25,SearchChunks 走纯 vector。
// 不进 indexing 三元强依赖组 —— 已有的 274/646 存量 chunk tsv_tokens 为空,没分词器也能正常跑 vector 路。
//
// Reranker 为 T1.2 二阶段重排用:retrieve 拿 candidateK 条后,对每条 (query, chunk.content) 对过一次
// cross-encoder 重新打分,截取 top K。**可选**:nil = 不启用,SearchChunks 直接返 retrieve 结果。
// 不进强依赖组的原因:rerank 是精度优化,不是功能必需 —— TEI 服务挂了不应该导致搜索不可用。
type Dependencies struct {
	DocRepo   repository.Repository
	ChunkRepo repository.ChunkRepository
	OSS       ossclient.Client
	Embedder  embedding.Embedder
	Chunkers  *chunker.Registry
	Tokenizer tokenizer.Tokenizer
	Reranker  reranker.Reranker
	OrgPort   OrgPort
	Logger    logger.LoggerInterface
}

// errWrap 让 service 层内部错误统一 wrap sentinel。
// 纯包装辅助,不打日志也不二次包 sentinel(base 本身就是 sentinel)。
func errWrap(base, inner error) error {
	if inner == nil {
		return nil
	}
	//sayso-lint:ignore sentinel-wrap,log-coverage
	return errors.Join(inner, base)
}

// fmtErr 为了未来单测而存在的内部 helper(目前只被 New 使用),
// 把 Dependencies 缺字段的错误拼一个带 sentinel 的返回值。
func fmtErr(msg string) error {
	//sayso-lint:ignore sentinel-wrap,log-coverage
	return fmt.Errorf("document service: %s: %w", msg, document.ErrDocumentInternal)
}
