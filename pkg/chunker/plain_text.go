// plain_text.go 递归分隔符 profile:任何文本的 MVP 兜底策略。
//
// 直接复用 chunker.go 里的 recursiveChunker,输出转成 Piece(ContentType="text",无 heading_path)。
// Markdown 也可以用这个 —— 但丢了结构信息,所以 DefaultRegistry 只在没有更合适 profile 时回退。
package chunker

// PlainTextProfile 纯文本 profile。Version="v1" 和 T1.3 之前的 chunker 行为等价,
// 保留这个版本号让存量 chunks 的 chunker_version='v1' 语义连续(schema 默认值就是 v1)。
type PlainTextProfile struct {
	rc *recursiveChunker
}

// NewPlainText 构造 plain_text profile。Config 里的异常值由 newRecursive 做归一化。
func NewPlainText(cfg Config) *PlainTextProfile {
	return &PlainTextProfile{rc: newRecursive(cfg)}
}

// Name 用于日志/路由,不参与持久化。
func (p *PlainTextProfile) Name() string { return "plain_text" }

// Version 写入 document_chunks.chunker_version。
func (p *PlainTextProfile) Version() string { return "v1" }

// Chunk 纯文本切分:直接透传递归分隔符结果,打上 ContentType="text" 其他 metadata 留空。
func (p *PlainTextProfile) Chunk(content string) []Piece {
	raws := p.rc.chunkText(content)
	if len(raws) == 0 {
		return nil
	}
	out := make([]Piece, len(raws))
	for i, r := range raws {
		out[i] = Piece{
			Content:     r.Content,
			TokenCount:  r.TokenCount,
			ContentType: "text",
			// HeadingPath 空、ChunkLevel=0:零值表示"没结构信息",上层 SELECT 时自然不会命中 heading 过滤。
		}
	}
	return out
}
