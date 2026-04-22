package ingestion

import "time"

// NormalizedDoc 归一化后的文档级中间表示。所有 source type 的 Fetcher 都输出这种结构,
// 下游的 chunker / embedder / persister 只认它,不关心源是 git / notion / upload 还是 bug。
//
// 这是"从异构到同构"的闸口 —— 新增一种 source type 只需实现 Fetcher 产出 NormalizedDoc,
// pipeline 其他环节不动。字段选择遵循两条原则:
//
//  1. **公共字段进 struct**:OrgID / SourceType / Content 这类所有 source 都有的,放顶层。
//  2. **类型专属字段进 Payload**:RepoID / OSSKey 这类 source-specific 的,走 SourcePayload
//     接口,persister 按 SourceType 做类型断言。
//
// 关键不变量(pipeline 会校验):
//
//   - OrgID 非零(多租户隔离强制)
//   - SourceType 非空(chunker / persister 路由依据)
//   - SourceID 非空(幂等键:同 (OrgID, SourceType, SourceID) 重复 ingest 由 persister upsert)
//   - Version 用于增量判定(同 SourceID 下 Version 未变 → persister 可 skip 重 embed)
type NormalizedDoc struct {
	OrgID uint64

	// DocID 可选的预分配主键。
	//
	//   - 非 0:persister 用此值作为 documents.id。适用于 handler 层已生成 snowflake 并需要
	//     立刻返给客户端的场景(upload fetcher:handler 提前生成 doc_id 返 202,runner 异步跑 pipeline)
	//   - 0(默认):persister 按 (org_id, source_type, source_id) 查已有行 → 有则复用 id,
	//     无则自己生成 snowflake。适用 sync 类 fetcher(飞书/Notion)—— fetcher 不预知 id。
	DocID uint64

	// SourceType "code" / "document_upload" / "notion" / "bug" / "image" / "db" ...
	// 和 documents.source_type 列语义对齐。
	SourceType string

	// SourceID 源端幂等键。拼接规则由各 Fetcher 自定:
	//   - git 文件     :"gitlab:<external_project_id>:<path>"
	//   - upload       :雪花 doc_id 字符串
	//   - notion page  :page_id
	// 同一 (OrgID, SourceType, SourceID) 命中 persister 的 upsert 分支,不会产生重复行。
	SourceID string

	// ExternalRef 回源锚点。召回时附在 Hit 上让 agent/user 能点回原资源,
	// persister 也可以用它的字段(如 Commit)写入 DB 做审计。
	ExternalRef ExternalRef

	// Version 内容指纹。含义由 source type 定:
	//   - git       :git blob sha1
	//   - upload    :文件原文 sha256
	//   - notion    :updated_at + etag 组合串
	// 同 SourceID 下 Version 未变 → persister 会短路(不重 chunk / 不重 embed),
	// 变了 → 走完整 chunk+embed+swap。
	Version string

	Title    string
	MIMEType string
	FileName string

	// Content 原始字节。文本 source 是 UTF-8。图像 / 二进制走独立链路(VLM caption 后
	// 以 text 形式再进 pipeline),不直接把二进制灌这里。
	Content []byte

	// Language 代码类 source 的语言 tag(go / python / typescript...)。非代码留空。
	// chunker 路由用:Language 非空 → 选 tree-sitter;空 + markdown mime → 选 markdown 结构化;
	// 否则 plain_text。
	Language string

	UploaderID uint64 // 发起本次 ingest 的 user,写审计 / failed 明细用

	// KnowledgeSourceID 指向 sources.id —— 权限模型里本 doc 所属的"知识源"。
	// Fetcher 负责填充(upload fetcher 从 handler 透传,未来 gitlab/feishu fetcher
	// 从同步配置里拿);persister 写入 documents.knowledge_source_id。
	KnowledgeSourceID uint64

	// Payload source-specific 扩展字段。persister 断言成具体类型读需要的字段。
	// nil 允许(source type 不需要额外字段时);不 nil 时其 SourcePayloadKind 必须和
	// 本 doc 的 SourceType 语义匹配(pipeline 不强校验,实现侧约定)。
	Payload SourcePayload

	// ACL M4.2 Doc 级 ACL 的预留。当前只填 UploaderID;GroupIDs 留空,等 M4.2 启用。
	ACL ACLHints

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ExternalRef 回源锚点。字段按 Kind 取不同语义,不是每一类都填所有字段。
//
// Kind 取值约定:"git" / "oss" / "url" / "notion" / "jira" / "image_caption" / ...
// 新源类型只加 Kind 常量和相应填充约定,不需要为每种加子类型 struct —— 字段少,
// 用 Extra 兜底也够。
type ExternalRef struct {
	Kind string

	// 通用
	URI string // 用户可点的 URL(如 GitLab blob 链接、OSS 预签名 URL)

	// git / code
	Repo   string // path_with_namespace,如 "team/sub/repo"
	Path   string // 相对 repo 根的路径
	Commit string // 本次索引时的 commit SHA

	// document / upload
	OSSKey string // OSS object key

	// Extra 其他类型的非关键字段。Persister 读,自行约定 key。
	Extra map[string]string
}

// SourcePayload 标记接口,带一个名字让 persister 可断言 + 防误写。
//
// 实现方:
//
//   - CodePayload      (ingestion/source/git)
//   - DocumentPayload  (ingestion/source/upload)
//   - 未来的 NotionPayload / BugPayload / ...
//
// persister 的典型用法:
//
//	func (p *codePersister) Persist(... doc *NormalizedDoc, ...) error {
//	    payload, ok := doc.Payload.(ingestion.CodePayload)
//	    if !ok {
//	        return fmt.Errorf("code persister: expected CodePayload, got %T", doc.Payload)
//	    }
//	    // 用 payload.RepoID / payload.BlobSHA 等字段
//	}
type SourcePayload interface {
	SourcePayloadKind() string
}

// ACLHints Doc 级 ACL(M4.2)的预留结构。
//
// 当前只填 UploaderID(方便审计 / owner 展示);GroupIDs 留空。
// M4.2 启用后,chunker / persister 会把 GroupIDs 写进 documents.acl_group_ids,
// 检索层 JOIN user_groups 过滤。
type ACLHints struct {
	UploaderID uint64
	GroupIDs   []uint64
}
