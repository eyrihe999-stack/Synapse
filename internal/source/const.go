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

	// DefaultGitLabBranch 创建 gitlab_repo source 时未指定 branch 的默认值。
	// "main" 而非 "master":新 GitLab 14+ 默认即 main;老 repo 用户必须显式指定。
	DefaultGitLabBranch = "main"

	// MaxGitLabFileBytes 单文件硬上限。超过即 skip(写日志,不入 chunks)。
	// 5MB:覆盖绝大多数源码 / 配置 / 文档;再大基本是数据 / 二进制 / 生成产物,
	// 进向量库价值低,且 embed 单条 input 8KB 上限会被反复触发。
	MaxGitLabFileBytes = 5 * 1024 * 1024
)
