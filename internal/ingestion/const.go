package ingestion

// SourceType* 常量:按"写入哪个 chunk 表"划分的顶层路由键,不按"源端是什么"。
//
// 例如未来 Feishu / Notion / Git file blob / Markdown upload 等多种 Fetcher 都会产出
// SourceType=SourceTypeDocument 的 NormalizedDoc,共用 document persister 写 document_chunks。
// 这样新加源不需要新开 persister,也不需要新加 chunk 表。
//
// 只有真正需要独立 schema 的才开新常量(code 有专属 symbol/line 列 → SourceTypeCode)。
const (
	SourceTypeDocument = "document"
	SourceTypeCode     = "code"
)
