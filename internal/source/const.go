// const.go source 模块常量定义。
//
// Source(知识源)是权限模型里"资源 ACL 的承载者":每个 document 必属于一个 source,
// 权限判定走 source 而不是 doc 本身(详见架构文档 M2 设计)。
//
// M2 阶段只有 kind=manual_upload 一种 —— 每个用户在每个 org 内自动 lazy 创建一个,
// 用户手动上传的 doc 都进这个 source。后续 kind 扩展(gitlab_repo / feishu_space 等)
// 走同一张表,通过 (kind, external_ref, owner_user_id) 区分。
package source

// ─── 表名常量 ─────────────────────────────────────────────────────────────────

const (
	// TableSources 知识源主表
	TableSources = "sources"
)

// ─── 默认值与上限 ─────────────────────────────────────────────────────────────

const (
	// DefaultPageSize 列表接口默认分页大小
	DefaultPageSize = 20
	// MaxPageSize 列表接口最大分页大小
	MaxPageSize = 100
	// MaxSourceNameLength source.name 字节上限(对齐 Source.Name gorm 声明 size:128)
	MaxSourceNameLength = 128
)
