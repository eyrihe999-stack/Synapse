// Package retrieval 定义 agent 与异构知识存储(code / document / image / bug...)之间的检索协议。
//
// 设计目标:
//   - 统一信封:跨模态共享 Query / Hit 结构,上层 agent 写一套引用逻辑;
//   - 模态专属 Filter:按模态用类型化 JSON Schema 表达,schema 即 prompt,提升 LLM 调用准确率;
//   - 多租户隔离:OrgID 由 middleware 注入,绝不暴露为 LLM 参数。
package retrieval

import "encoding/json"

// Modality 知识源类型。新增一种 = 加常量 + 实现 Retriever + 注册进 Registry。
type Modality string

const (
	ModalityCode     Modality = "code"
	ModalityDocument Modality = "document"
	ModalityImage    Modality = "image" // 预留
	ModalityBug      Modality = "bug"   // 预留
)

// RetrieveMode 召回策略。不支持的实现降级到 vector 并在响应里标注。
type RetrieveMode string

const (
	ModeDefault RetrieveMode = ""       // 由实现自选
	ModeVector  RetrieveMode = "vector" // 纯向量
	ModeBM25    RetrieveMode = "bm25"   // 纯字面
	ModeHybrid  RetrieveMode = "hybrid" // 向量 + BM25 融合
	ModeSymbol  RetrieveMode = "symbol" // code 独有:精确符号名 ILIKE
)

// Query 跨模态统一检索请求。
// OrgID 永远由 middleware 从认证上下文注入,不得从 LLM 工具参数读取 —— 否则多租户越权。
type Query struct {
	OrgID    uint64
	Modality Modality
	Text     string
	TopK     int
	Mode     RetrieveMode
	Rerank   bool
	Filter   json.RawMessage // 模态专属;见 CodeFilter / DocumentFilter
}

// Hit 跨模态统一命中。Snippet 给 LLM 即时看,全文走 FetchByID(上下文预算宝贵)。
type Hit struct {
	ID        string          // "{modality}:{inner_id}",跨模态不撞号
	Modality  Modality        // 冗余字段,便于程序分流
	Score     float32         // 归一化相似度 [0,1],1 = 最相关
	Scorer    string          // "vector" / "symbol" / "bm25" / "rerank",调试与 A/B 用
	SourceRef SourceRef       // 让 agent 在生成物里精确引用出处
	Snippet   string          // 截断到 SnippetMaxChars
	Metadata  json.RawMessage // 模态专属 meta(code:symbol/signature/line_range;doc:heading_path/content_type...)
	// Related 供扩展上下文的相关 chunk ID 列表;语义按模态定义:
	//   - document:父层级 chunk(heading → section),由 DocumentFilter.ReturnParents 触发填充
	//   - code:同文件邻近 chunks(siblings),由 CodeFilter.IncludeSiblings 触发填充
	// agent 想要更宽上下文时用 fetch_{modality}(id) 拉取。
	Related []string
}

// SourceRef 指向源,让 agent 在 PRD 里引用出处。
type SourceRef struct {
	Kind  string     `json:"kind"` // "git" / "doc" / "image" / "bug"
	Repo  string     `json:"repo,omitempty"`
	Path  string     `json:"path,omitempty"`
	DocID string     `json:"doc_id,omitempty"`
	Line  *LineRange `json:"line,omitempty"` // code 专属
}

type LineRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// SnippetMaxChars 预览长度预算。超过由 Retriever 截断;想要全文 agent 显式 fetch_{modality}。
const SnippetMaxChars = 800
