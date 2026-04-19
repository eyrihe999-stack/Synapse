// Package code 代码知识库模块。
//
// 范围:从外部 git 托管(GitLab v1、后续 GitHub/Gitea)同步代码仓库到 Synapse,按函数级切分 + 向量化,
// 供 agent 检索和问答。
//
// 和 document 模块的关系:**平级、独立**。两者都属于"知识库"大类,但数据模型、切分策略、
// 存储链路都不同 —— 文档按段落切走 OSS,代码按 AST 切走 PG。共用 pkg/embedding 做向量化。
//
// 模块结构:
//   - model/          :CodeRepository / CodeFile / CodeFileContent / CodeChunk
//   - repository/     :跨三张表的 CRUD,都在 PG 一个库内
//   - service/        :ingest(同步编排) + search(检索,MVP 后置)
//   - source/         :CodeSource 接口 + 具体 provider 实现(gitlab / 未来 github)
//   - migration.go    :AutoMigrate 四张表 + 建 HNSW 向量索引
package code

// Provider 外部代码托管平台枚举。写入 code_repositories.provider 列 + 作为 source registry 查找键。
const (
	ProviderGitLab = "gitlab"
)

// ChunkKind 代码 chunk 的语义种类。写入 code_chunks.chunk_kind 列,检索层可按此过滤
// (例:"只搜函数,不搜 preamble")。
//
// 用枚举字符串而非 int:后续加新类型不破坏老数据,日志/诊断更可读。
const (
	// ChunkKindFunction 顶层函数。
	ChunkKindFunction = "function"
	// ChunkKindMethod 结构体/类的方法。
	ChunkKindMethod = "method"
	// ChunkKindClass 类 / struct / interface 整体(当类本身很短 + 成员少时整个作为一个 chunk,
	// 避免每个小方法都炸成独立 chunk 让检索召回满屏都是零碎片段)。
	ChunkKindClass = "class"
	// ChunkKindPreamble 文件顶部的 import / package 声明 / 顶层注释块。
	// 单独成 chunk:让"这个文件依赖啥"的问题能被检索命中,不被函数 chunk 冲掉。
	ChunkKindPreamble = "preamble"
	// ChunkKindUnparsed tree-sitter 不支持的语言,或 parse 失败走启发式切分的产物。
	// 指示这条 chunk 是低质量兜底,检索层可选择降权或过滤。
	ChunkKindUnparsed = "unparsed"
)

// Chunk 索引状态。语义和 document 模块对齐(但独立常量,避免跨模块依赖)。
const (
	ChunkIndexStatusPending = "pending" // 行已插入,向量还没算出
	ChunkIndexStatusIndexed = "indexed" // 向量已回填
	ChunkIndexStatusFailed  = "failed"  // embed 失败;IndexError 字段记根因
)

// ChunkEmbeddingDim code_chunks.embedding 列的向量维度。
// 和 document.ChunkEmbeddingDim 保持一致(复用同一 embedding provider)—— 两个模块共用
// config.embedding.text,换模型时两边同步迁移。改维度 = schema 破坏性迁移,HNSW 索引要重建。
const ChunkEmbeddingDim = 1536
