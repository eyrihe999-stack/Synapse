package ingestion

// IngestedChunk chunker 产出 → embedder 输入 → persister 落库 的中间形态。
//
// 有意设计成"宽结构":不同 source type 关心不同字段(code 看 SymbolName/LineStart,
// document 看 HeadingPath/Level),persister 取自己需要的,不取的字段零值忽略。
// 对比"按 source type 分 struct + 接口":字段总量不大,宽结构更好读写、接口更轻。
//
// 一个 chunker 调用产出一批 chunks(属于同一 NormalizedDoc),Index 按出现顺序 0-based。
// pipeline 传给 embedder 时按 Index 顺序发,persister 按 Index 顺序写 chunk_idx。
type IngestedChunk struct {
	// Index 在同一 NormalizedDoc 内的顺序,0-based。
	Index int

	// Content chunk 正文 —— embedder 的输入,也是存库的 chunk.content 列。
	Content string

	// TokenCount 近似 token 数(字节/4 粗估,对 UI 展示 + batch 估算足够)。
	TokenCount int

	// ContentType 内容类型标签。已知取值:"text" / "heading" / "code" / "table" /
	// "function" / "method" / "class" / "preamble" / "unparsed"。空字符串由 persister
	// normalize 成 "text"。
	ContentType string

	// Level 结构深度。markdown 是 heading 层级(h1=1, h2=2...);纯文本 / 代码 0。
	Level int16

	// HeadingPath markdown 专用。["架构", "数据模型"] 表示本 chunk 在 "架构 > 数据模型"
	// 二级标题下。非 markdown 留空。
	HeadingPath []string

	// ─── 代码专属(非代码留零值)─────────────────────────────────────────

	SymbolName string // 函数/方法/类名
	Signature  string // 完整签名(仅 function/method)
	Language   string // go / python / ...
	LineStart  int    // 1-based 闭区间
	LineEnd    int

	// Metadata 通用结构化 meta。已知字段不够用时扩展放这里,persister 有权选择
	// 透传到 document_chunks.metadata jsonb 列 / 忽略 / 做专属处理。
	Metadata map[string]any

	// ParentIndex T1.3 parent-child:指向同批次内父 chunk 的 Index。nil 表示 root。
	// persister 在批量 insert 后把"Index → 实际 chunk_id"的映射回填 ParentChunkID 列。
	ParentIndex *int

	// ChunkerVersion 切分器版本 tag(由 Chunker 实现在 Chunk() 里填),写进 chunks.chunker_version。
	// 同一 Chunk 调用产出的所有 chunks 共享同一个值;设计上算"批量共享"字段冗余到每行,
	// 但实现成本远低于新增一个"批"结构,而且 DB 侧本就每行一列,无额外代价。
	ChunkerVersion string
}
