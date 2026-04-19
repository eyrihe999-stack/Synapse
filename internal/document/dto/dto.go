// Package dto document 模块 HTTP 接口的请求与响应类型。
package dto

import "encoding/json"

// UpdateMetadataRequest PATCH /documents/:id 的 body。
// 指针字段用来区分"不更新"(nil)与"置空"(指向空串)。
type UpdateMetadataRequest struct {
	Title *string `json:"title,omitempty"`
}

// DocumentResponse 文档基本信息(同时用于 Upload / Get / Update / List / SemanticSearch 响应)。
//
// Similarity / MatchedSnippet 仅在 semantic 搜索路径下填充,其他路径留零值 + omitempty 不序列化。
type DocumentResponse struct {
	ID                  uint64 `json:"id,string"`
	OrgID               uint64 `json:"org_id,string"`
	UploaderID          uint64 `json:"uploader_id,string"`
	UploaderDisplayName string `json:"uploader_display_name,omitempty"`
	Title               string `json:"title"`
	MIMEType            string `json:"mime_type"`
	FileName            string `json:"file_name"`
	SizeBytes           int64  `json:"size_bytes"`
	// Source 文档来源,取值见 document.DocSource* 常量。user / ai-generated。
	// 前端可据此给 AI 产物加标记图标,也供检索时用户选择是否过滤 AI 产物。
	Source    string `json:"source"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	// Similarity 相似度 0.0-1.0(=1 - cosine_distance/2),越大越相关。仅 semantic 搜索路径下填。
	Similarity float32 `json:"similarity,omitempty"`
	// MatchedSnippet 命中文档中最相关那段 chunk 的 content 前 SemanticSnippetChars 字符,超长带 "..."。
	// 仅 semantic 搜索路径下填,前端在列表行下方渲染。
	MatchedSnippet string `json:"matched_snippet,omitempty"`
}

// ─── Chunk 级检索 ────────────────────────────────────────────────────────────

// ChunkSearchResult 一条 chunk 级检索结果。
//
// 和 DocumentResponse 的区别:后者是"一篇文档命中"粒度,带最佳片段;此结构是"一段原文命中"粒度,
// 保留 DocID + ChunkIdx 作为引用定位符([doc_id:chunk_idx]),generator 用它生成 PRD 时
// 每条断言都带精确引用,用户点击能跳到原文段落。
type ChunkSearchResult struct {
	ChunkID    uint64  `json:"chunk_id,string"` // document_chunks.id;retrieval.Hit.ID 与父 chunk 展开的主键
	DocID      uint64  `json:"doc_id,string"`
	ChunkIdx   int     `json:"chunk_idx"`
	Content    string  `json:"content"`
	Similarity float32 `json:"similarity"` // 1 - cosine_distance/2,越大越相关
	DocTitle   string  `json:"doc_title"`  // 冗余给 UI 展示"来自《X》"
	DocSource  string  `json:"doc_source"` // 方便前端/generator 过滤 AI 产物

	// Metadata jsonb 原样透传。典型键:heading_path(数组)/ source_type / tags。retrieval 层用来构建 Hit.Metadata。
	Metadata json.RawMessage `json:"metadata,omitempty"`
	// ParentChunkID 结构化切分时的父 chunk id;nil = root。retrieval DocumentFilter.ReturnParents 依赖此字段扩上下文。
	ParentChunkID *uint64 `json:"parent_chunk_id,string,omitempty"`
	// ContentType 分类标签:heading / text / code / table / list。agent 按类型筛选时直接可用。
	ContentType string `json:"content_type,omitempty"`
	// RerankScore rerank 通路打分,nil = 未经 rerank。量纲随 reranker 实现变化(见 SearchChunksOptions.MinRerankScore)。
	RerankScore *float32 `json:"rerank_score,omitempty"`
}

// ListChunksResponse chunk 级检索的分页响应。
// 和 ListDocumentsResponse 不同 —— 这里 Total = Items 长度(不再分页,一次拿 TopK)。
//
// Confidence 来自 SearchChunksOptions.MinRerankScore / MinSimilarity 的门限判定:
//   - ""     :调用方未设阈值,向后兼容,不做置信度标注
//   - "high" :设了阈值且至少一条过线 —— 可信结果
//   - "none" :设了阈值但无一条过线 —— agent 应明确告诉用户"KB 里没找到",避免硬凑答案
// 当前不暴露 "low" 档(需要双阈值,YAGNI);未来按需加。
type ListChunksResponse struct {
	Items      []ChunkSearchResult `json:"items"`
	Total      int                 `json:"total"`
	TopK       int                 `json:"top_k"`
	Confidence string              `json:"confidence,omitempty"`
}

// ListDocumentsResponse 分页列表。
type ListDocumentsResponse struct {
	Items []DocumentResponse `json:"items"`
	Total int64              `json:"total"`
	Page  int                `json:"page"`
	Size  int                `json:"size"`
}

// ─── Precheck ────────────────────────────────────────────────────────────────

// PrecheckRequest 客户端在点"上传"前发的预检请求。
// 客户端本地读文件后 sha256 hex → 填 content_hash;服务端不校验 hash 的真实性(客户端"骗" hash 只会误导自己的 UI)。
type PrecheckRequest struct {
	Files []PrecheckCandidate `json:"files" binding:"required,min=1,max=50,dive"`
}

// PrecheckCandidate 一个待预检的文件描述。
type PrecheckCandidate struct {
	FileName    string `json:"file_name" binding:"required,max=256"`
	SizeBytes   int64  `json:"size_bytes" binding:"required,min=0"`
	MIMEType    string `json:"mime_type" binding:"required,max=128"`
	ContentHash string `json:"content_hash" binding:"required,len=64,hexadecimal"`
}

// PrecheckResponse 批量预检结果。results 顺序与请求 files 顺序一一对应。
type PrecheckResponse struct {
	Results []PrecheckResultEntry `json:"results"`
}

// PrecheckResultEntry 单个候选的预检结果。
//
// Existing 仅在 action = duplicate 时填充(hash 命中的那条已存在文档)。
// ExistingList 仅在 action = overwrite 时填充(所有同名候选,可能 ≥1 条,让用户选覆盖目标或选择新建)。
type PrecheckResultEntry struct {
	FileName     string             `json:"file_name"`
	Action       string             `json:"action"`      // create | overwrite | duplicate | reject
	ReasonCode   string             `json:"reason_code"` // 见 document.PrecheckReason*
	Existing     *DocumentResponse  `json:"existing,omitempty"`
	ExistingList []DocumentResponse `json:"existing_list,omitempty"`
}

// ─── UploadConfig ────────────────────────────────────────────────────────────

// UploadConfigResponse 上传相关的服务端约束,供前端做本地预过滤 + 能力探测。
type UploadConfigResponse struct {
	MaxFileSizeBytes      int64    `json:"max_file_size_bytes"`
	AllowedMIMETypes      []string `json:"allowed_mime_types"`
	// SemanticSearchEnabled 告诉前端语义搜索是否可用(= service 侧索引三元是否齐备)。
	// 不可用时前端应灰掉"语义"切换选项,避免用户点了给 503。
	SemanticSearchEnabled bool `json:"semantic_search_enabled"`
}
