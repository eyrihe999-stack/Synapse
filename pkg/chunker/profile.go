// profile.go 切分器 Profile 接口 + 结构化 Piece 类型。
//
// 为什么引入 Profile:不同内容类型(markdown / 纯文本 / 代码 / 表格)的语义边界完全不同。
// 一个单一 Chunker 接口强塞所有类型会导致 "markdown 按字符切" 这种 anti-pattern。
// Profile 把切分策略和内容类型解耦,上层靠 Registry.Pick(mime, filename) 路由。
//
// Piece 相比旧的 Chunk 多了 ContentType / HeadingPath / ChunkLevel 三个结构化字段,
// 这些信息会写入 document_chunks.metadata + content_type + chunk_level,为 T1.4 结构化
// 过滤和未来 parent-child 召回提供骨架。
package chunker

// Piece 单个切片。零值 ContentType / 空 HeadingPath / ChunkLevel=0 表示没有结构信息,
// 退化成 T1 之前的"扁平 chunk"语义,向后兼容。
type Piece struct {
	// Content chunk 正文,embed 和 BM25 的输入。
	Content string
	// TokenCount 近似 token 数(按 rune 估算)。
	TokenCount int
	// ContentType 内容类型标签,同时写入 document_chunks.content_type。
	// 常见值:"text"(段落)/"heading"(标题本身)/"code"(代码块)/"table"(表格)/"section"(整段落集合)。
	// 空串由上层 normalize 成 "text"。
	ContentType string
	// HeadingPath 从文档根到本 chunk 的 heading 路径,如 ["架构", "数据模型"]。
	// 仅结构化 profile(markdown_structured 等)填充,plain_text 留空。
	HeadingPath []string
	// ChunkLevel 所属 heading 的深度(1=h1,2=h2...);preamble/纯文本为 0。
	ChunkLevel int16
}

// Profile 一种具体的切分策略。各实现是无状态的(cfg 在构造期绑定),可并发调用。
type Profile interface {
	// Name 策略标识,如 "plain_text" / "markdown_structured"。用于日志和路由决策。
	Name() string
	// Version 策略版本号,写入 document_chunks.chunker_version。升级 profile 时换版本号,
	// 检索层可选过滤或灰度,避免新旧 chunk 语义混乱。
	Version() string
	// Chunk 切分 content。空 / 全空白输入应返回空切片,不得 panic。
	Chunk(content string) []Piece
}
