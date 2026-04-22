package ingestion

import "errors"

// Sentinel errors.Is 可判别的 pipeline 终态错,供上层(asyncjob runner / 其他调用者)按类型分流。
// 非致命错(chunk 为空、embed network retry、persister 写 failed 行)不走 sentinel,
// 在 PreparedDoc.EmbedErr 或 persister 内部自行处理,不污染对外错误面。
var (
	// ErrInvalidDoc Fetcher 产出的 NormalizedDoc 基础不变量不成立(nil / OrgID=0 / SourceType 空 / SourceID 空)。
	// 调用侧 bug 或脏数据,重试无用,应由调用方记录后放弃。
	ErrInvalidDoc = errors.New("invalid normalized doc")

	// ErrUnknownSourceType doc.SourceType 没有对应 persister 或 chunker。典型是装配漏注册
	// (main 没给 Registry 传这个 source type 的 persister),或新增 source type 但还没实现
	// chunker selector 分支。属于配置问题,改配置重启才修得。
	ErrUnknownSourceType = errors.New("unknown source type")

	// ErrFatalEmbed embed provider 返回的致命错(Auth / DimMismatch)—— 配置 / schema 错,
	// 不是瞬时问题,整轮 ingest 应 abort。非致命 embed 错走 PreparedDoc.EmbedErr,不走此 sentinel。
	ErrFatalEmbed = errors.New("fatal embed error")
)
