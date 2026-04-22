package ingestion

import (
	"context"

	"github.com/eyrihe999-stack/Synapse/internal/common/embedding"
)

// Fetcher 从源端拉数据,输出 NormalizedDoc 流。per-call 构造(一次 ingest = 一个 fetcher),
// 携带本次所需的上下文(用户 PAT / 上传 body / notion cursor 等)。
//
// 生命周期:调用方构造 → 调 Pipeline.Run(fetcher) → 丢弃。fetcher 实例不跨 ingest 复用,
// 也不并发跑 Fetch;fetcher 内部可以自己用 goroutine 并行拉,但对 emit 的调用必须是串行的
// (pipeline 的 emit 实现不是并发安全)。
type Fetcher interface {
	// SourceType 本 fetcher 产出 doc 的 SourceType。Pipeline 按此查 persister 路由。
	// 必须在构造后就能返(不依赖 Fetch 执行过)。
	SourceType() string

	// Fetch 拉数据,每产出一个 doc 调一次 emit。
	//
	// emit 返 error 时 Fetch 必须立刻停止并把错误往上传(pipeline 用它通知 fetcher
	// "下游不可恢复,别再拉了")。fetcher 自身的非致命错(单文件 gone / 单 notion page 403)
	// 由 fetcher 内部判断 skip,不阻塞后续 emit、也不上抛。
	//
	// 致命错(PAT 无效、源 API 整体不可达、ctx 已取消)→ 直接 return err,pipeline 终止整轮。
	Fetch(ctx context.Context, emit Emit) error
}

// Emit pipeline 提供给 fetcher 的"交出一个 doc"的回调。
//
// Fetcher 每构造好一个 NormalizedDoc 就调一次。传进去后 doc 的所有权转给 pipeline,
// fetcher 不应再修改它。
type Emit func(ctx context.Context, doc *NormalizedDoc) error

// Chunker 把 NormalizedDoc 切成 []IngestedChunk。
//
// 无状态(配置在构造期绑定),可并发调用。空或全空白 Content 返 nil、不报错。
//
// Version 会被 persister 写进 chunker_version 列,升级策略时灰度 / 新旧 chunks 共存用。
// 换切分算法应当升版本号(v1 → v2),便于检索层选择性读。
type Chunker interface {
	Name() string
	Version() string
	Chunk(ctx context.Context, doc *NormalizedDoc) ([]IngestedChunk, error)
}

// Persister 把切好的 chunks + embed 结果落到 source-type 专属存储。
//
// 合约(embedErr 与返值组合):
//
//   - embedErr == nil                  :所有 chunk 写 IndexStatus=indexed,Embedding 填好;persister 返 nil。
//   - embedErr != nil 且非致命         :所有 chunk 写 IndexStatus=failed,Embedding=nil,IndexError 记摘要;
//     persister 返 nil(pipeline 视为成功,后台补偿任务兜底)。
//   - persister 自身 DB/IO 错(任何情况):返 error,pipeline 上抛终止整轮。
//
// 致命 embedErr(Auth/DimMismatch)**不会**传到 persister —— pipeline 在调 persister 前就拦下返 error。
// persister 只要处理"成功向量"和"非致命 embed 错"两种 case。
//
// Upsert / supersede 语义由 persister 内部决定:
//
//   - code persister  :按 (repo_id, path) upsert CodeFile + SwapChunksByFileID 原子替换 chunks
//   - upload persister:按预分配的 doc_id INSERT(每次 upload 产生新 doc_id)
//
// chunks 为空(chunker 产出 0)时 persister 仍被调用,各自按场景处理:
//
//   - code :清旧 chunks(文件变空 / 非文本)
//   - upload:skip(纯空白上传是 no-op 但 doc metadata 仍要落)
type Persister interface {
	SourceType() string
	Persist(ctx context.Context, doc *NormalizedDoc, chunks []IngestedChunk, vecs [][]float32, embedErr error) error
}

// Embedder alias 到 internal/common/embedding.Embedder。让 ingestion 子包引用时不用直接 import
// internal/common/embedding,装配层注入即可。接口见原定义:Embed / Dim / Model。
type Embedder = embedding.Embedder
