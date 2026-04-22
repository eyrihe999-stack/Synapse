// Package document 是 ingestion.Persister 的实现,以及 DocumentPayload 的定义。
//
// Payload:
//
//	所有 SourceType=document 的 fetcher(upload / feishu / notion / url / …)
//	都在 NormalizedDoc.Payload 里放 *Payload。持久化时 persister 断言此类型,
//	读出 Provider / ProviderMeta 等源端字段填到 documents 表。
package document

// Payload 所有 "文档类" 源端的统一扩展字段。
//
// 字段选择:对所有 provider 共有的进 struct 顶层(Provider / ExternalOwnerID);
// 源端特有字段扔 ProviderMeta,persister 写进 documents.external_ref_extra jsonb。
type Payload struct {
	// Provider 源端平台名字,写进 documents.provider 列(路由 / 列表过滤用)。
	// 取值例子:"upload" / "feishu" / "notion" / "url" / "github-wiki" / ...
	Provider string

	// ExternalOwnerID 源端的文档作者 ID(飞书 user id / notion user id / ...)。
	// 写审计日志 / 未来展示 "由谁创建" 用。当前不进 documents 列,
	// 如果将来有需求改进 schema(加 external_owner_id 列)时迁进去。
	// 暂时放 ProviderMeta 里以 owner_id 存也行;此字段留结构化入口。
	ExternalOwnerID string

	// ProviderMeta 源端独有字段(飞书 tenant_key / notion workspace_id / wiki space_id / ...)。
	// 写进 documents.external_ref_extra jsonb。key 的命名由各 fetcher 自定,retrieval 不做解读。
	ProviderMeta map[string]string
}

// SourcePayloadKind 见 ingestion.SourcePayload。
func (Payload) SourcePayloadKind() string { return "document" }
