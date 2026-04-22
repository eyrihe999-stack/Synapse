package ingestion

import (
	"context"
	"errors"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/common/embedding"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
)

// Pipeline 编排:Fetcher → NormalizedDoc → Chunker → Embedder → Persister。
//
// 一个 Pipeline 实例在服务进程内常驻;每次 ingest 调 Run 传入本次的 Fetcher(带 user 上下文)。
// 并发安全:内部字段都是只读(Registry/Embedder/Logger),Run 不共享可变状态。
type Pipeline struct {
	registry *Registry
	embedder Embedder
	log      logger.LoggerInterface
	cfg      PipelineConfig
}

// PipelineConfig 编排层配置。不控制具体 chunker/persister 行为(那些在实现里自管)。
type PipelineConfig struct {
	// EmbedBatchSize 单次调 Embedder 的 chunk 数上限。太大容易撞 provider 单请求 token 上限;
	// 太小浪费网络 round-trip。默认 32,是 Azure text-embedding-3-large 的实测平衡点。
	EmbedBatchSize int

	// EmbedInputMaxBytes 单条 embed input 字节硬上限。chunker 应已按 token 切分,此处只是
	// "中文注释密集的巨型 preamble / 单行 >8KB 病态文件"的兜底。超限按 UTF-8 rune 边界截断,
	// 只影响 embed 输入;chunks.Content 仍存全量原文。默认 8192。
	EmbedInputMaxBytes int
}

// DefaultPipelineConfig 返回一组经验值默认(EmbedBatchSize=32,EmbedInputMaxBytes=8KiB);
// 和生产 embedder(Azure text-embedding-3-large)的请求上限平衡过。
func DefaultPipelineConfig() PipelineConfig {
	return PipelineConfig{
		EmbedBatchSize:     32,
		EmbedInputMaxBytes: 8 * 1024,
	}
}

// NewPipeline 构造。所有依赖必填。
//
// 错误场景(都是装配期配置错,调用方应 fatal):
//   - registry / embedder / log 为 nil → 缺依赖
func NewPipeline(registry *Registry, embedder Embedder, log logger.LoggerInterface, cfg PipelineConfig) (*Pipeline, error) {
	if registry == nil {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("ingestion: registry is nil")
	}
	if embedder == nil {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("ingestion: embedder is nil")
	}
	if log == nil {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("ingestion: logger is nil")
	}
	if cfg.EmbedBatchSize <= 0 {
		cfg.EmbedBatchSize = 32
	}
	if cfg.EmbedInputMaxBytes <= 0 {
		cfg.EmbedInputMaxBytes = 8 * 1024
	}
	return &Pipeline{
		registry: registry,
		embedder: embedder,
		log:      log,
		cfg:      cfg,
	}, nil
}

// Run 跑一次完整 ingest。调用方构造 Fetcher(携带 user token / 上传 body 等上下文),
// 由 Run 串起 chunk → embed → persist 全链路。
//
// reporter 可 nil(cron / batch 场景);非 nil 时 pipeline 按 chunk 粒度上报:
//   - Prepare 在切块后 SetTotal(len(chunks)),embed 每批成功 Inc(batchSize, 0)
//   - 单 doc upload 这样前端能看到 0/30 → 5/30 → 10/30 的连续跳动,不会 0% 卡半天再突变
//
// 返 error 表示本轮 fatal:
//
//   - fetcher 整体错(PAT 无效 / 源 API 不可达)
//   - 致命 embed 错(Auth / DimMismatch 等配置错)
//   - persister 返 error(DB 不可达 / schema 错)
//
// 单 doc 级问题(某 doc 的 chunk 解析失败、某 doc 的 embed 可重试错)由 persister
// 写成 failed 行后 swallow,不返 error、不中断后续 doc。
//
// 内部 = Prepare + Persist 串起来;调用方需要把 chunk/embed 和 persist 分两段跑
// (如 MySQL 锁外 embed / 锁内 persist)可以直接用 Pipeline.Prepare / Pipeline.Persist。
func (p *Pipeline) Run(ctx context.Context, fetcher Fetcher, reporter ProgressReporter) error {
	if fetcher == nil {
		err := fmt.Errorf("ingestion: fetcher is nil")
		p.log.ErrorCtx(ctx, "ingestion: fetcher is nil", err, nil)
		return err
	}
	if reporter == nil {
		reporter = noopReporter{}
	}

	emit := func(ctx context.Context, doc *NormalizedDoc) error {
		// reporter 贯穿 Prepare→embedChunks,chunk 粒度上报进度;这里不再有 doc 级 Inc(1,0),
		// 否则 done 会超过 total(SetTotal 是 chunks 数)。
		prepared, err := p.Prepare(ctx, doc, reporter)
		if err != nil {
			//sayso-lint:ignore log-coverage
			return err
		}
		//sayso-lint:ignore err-shadow
		if err := p.Persist(ctx, prepared); err != nil {
			//sayso-lint:ignore log-coverage
			return err
		}
		return nil
	}
	//sayso-lint:ignore log-coverage
	return fetcher.Fetch(ctx, emit)
}

// PreparedDoc Prepare 的产物:已完成 chunk + embed,等待 Persist。
//
// 字段对外公开让诊断 / 日志能读(比如"这篇 doc 切出了多少个 chunk"、"embed 失败了没"),
// 但修改它的字段不会影响 Persist 行为 —— persist 拿的是构造时那份 chunks / vecs。
type PreparedDoc struct {
	Doc      *NormalizedDoc
	Chunks   []IngestedChunk
	Vecs     [][]float32 // len == len(Chunks) 当 EmbedErr == nil;EmbedErr 非 nil 时可能为 nil
	EmbedErr error       // 只可能是非致命错(致命已在 Prepare 里拦下抛出);nil = embed 成功

	persister Persister // 构造时锁定,Persist 不再重新路由
}

// Prepare 对单篇 doc 跑 chunk + embed,不落库。返回 PreparedDoc 供调用方后续调 Persist。
//
// 使用场景:调用方需要在 chunk/embed(慢)和 persist(快)之间插入其他逻辑,
// 典型例子是 document overwrite 路径 —— MySQL 锁外跑 Prepare(embed 慢),锁内调 Persist
// (PG swap 快),避免长时间持 MySQL 行锁阻塞并发。
//
// reporter 用来上报 chunk 粒度进度:切块后 SetTotal(N),embed 每批 Inc(batchSize, 0)。
// 传 nil 退化为 noopReporter(对不关心进度的调用者透明)。
//
// 错误语义:
//
//   - validate / 无 persister / 无 chunker / chunk 错 → 返 error
//   - 致命 embed 错(Auth/DimMismatch)              → 返 error
//   - 非致命 embed 错(Network/RateLimited)          → PreparedDoc.EmbedErr 非 nil,调用方传给 Persist 写 failed 行
func (p *Pipeline) Prepare(ctx context.Context, doc *NormalizedDoc, reporter ProgressReporter) (*PreparedDoc, error) {
	if reporter == nil {
		reporter = noopReporter{}
	}
	if err := validateDoc(doc); err != nil {
		p.log.ErrorCtx(ctx, "ingestion: invalid doc", err, nil)
		return nil, fmt.Errorf("ingestion: %w: %w", ErrInvalidDoc, err)
	}
	persister := p.registry.PickPersister(doc.SourceType)
	if persister == nil {
		//sayso-lint:ignore err-shadow
		err := fmt.Errorf("ingestion: no persister registered for source_type %q: %w", doc.SourceType, ErrUnknownSourceType)
		p.log.ErrorCtx(ctx, "ingestion: no persister registered", err, map[string]any{
			"source_type": doc.SourceType,
			"source_id":   doc.SourceID,
		})
		return nil, err
	}
	chunker := p.registry.PickChunker(doc)
	if chunker == nil {
		//sayso-lint:ignore err-shadow
		err := fmt.Errorf("ingestion: no chunker available (source_type=%q mime=%q language=%q): %w",
			doc.SourceType, doc.MIMEType, doc.Language, ErrUnknownSourceType)
		p.log.ErrorCtx(ctx, "ingestion: no chunker available", err, map[string]any{
			"source_type": doc.SourceType,
			"mime_type":   doc.MIMEType,
			"language":    doc.Language,
		})
		return nil, err
	}
	chunks, err := chunker.Chunk(ctx, doc)
	if err != nil {
		p.log.ErrorCtx(ctx, "ingestion: chunk failed", err, map[string]any{
			"source_type": doc.SourceType,
			"source_id":   doc.SourceID,
		})
		return nil, fmt.Errorf("chunk: %w", err)
	}

	// 切块后确定 total。chunks 为空 → SetTotal(0) 让前端直接进入 indeterminate/完成态;
	// 非空 → embedChunks 内部每批 Inc(batchSize, 0),驱动前端进度条。
	//sayso-lint:ignore err-swallow
	_ = reporter.SetTotal(len(chunks))

	// chunks 为空也要产生一个 prepared(persister 对空 chunks 有自己语义:code 清旧 / upload skip)。
	vecs, embedErr := p.embedChunks(ctx, chunks, reporter)
	if embedErr != nil && isFatalEmbedError(embedErr) {
		p.log.ErrorCtx(ctx, "ingestion: fatal embed error", embedErr, map[string]any{
			"source_type": doc.SourceType,
			"source_id":   doc.SourceID,
			"chunk_count": len(chunks),
		})
		return nil, fmt.Errorf("%w: %w", ErrFatalEmbed, embedErr)
	}

	return &PreparedDoc{
		Doc:       doc,
		Chunks:    chunks,
		Vecs:      vecs,
		EmbedErr:  embedErr,
		persister: persister,
	}, nil
}

// Persist 把 Prepare 的产物落盘。单独暴露是给 overwrite 这种"准备和持久化要分开"的场景用。
// 返 error 表示 persister 的 DB/IO 错(fatal);embedErr 场景(failed 写入)不返 error。
func (p *Pipeline) Persist(ctx context.Context, prepared *PreparedDoc) error {
	if prepared == nil {
		err := errors.New("ingestion: nil prepared doc")
		p.log.ErrorCtx(ctx, "ingestion: nil prepared doc", err, nil)
		return err
	}
	if prepared.persister == nil {
		err := errors.New("ingestion: prepared doc has no persister (misconstructed)")
		p.log.ErrorCtx(ctx, "ingestion: prepared doc has no persister", err, nil)
		return err
	}
	if err := prepared.persister.Persist(ctx, prepared.Doc, prepared.Chunks, prepared.Vecs, prepared.EmbedErr); err != nil {
		//sayso-lint:ignore log-coverage
		return fmt.Errorf("persist: %w", err)
	}
	return nil
}

// embedChunks 分批调 Embedder。chunks 为空直接返 (nil, nil)。
//
// reporter 每批成功后 Inc(batchSize, 0) 上报进度;任何一批失败即视为整 doc embed 失败 ——
// 不返"部分成功"的 partial 结果,因为 persister 的 failed 语义是"整 doc 退回待重试",
// 不处理"chunk 0-15 成功、16-31 失败"。上报进度 swallow 错误:进度表写失败不影响真正 embed。
func (p *Pipeline) embedChunks(ctx context.Context, chunks []IngestedChunk, reporter ProgressReporter) ([][]float32, error) {
	if len(chunks) == 0 {
		return nil, nil
	}
	inputs := make([]string, len(chunks))
	for i, c := range chunks {
		inputs[i] = truncateUTF8(c.Content, p.cfg.EmbedInputMaxBytes)
	}

	out := make([][]float32, 0, len(chunks))
	for start := 0; start < len(inputs); start += p.cfg.EmbedBatchSize {
		end := min(start+p.cfg.EmbedBatchSize, len(inputs))
		batch, err := p.embedder.Embed(ctx, inputs[start:end])
		if err != nil {
			p.log.ErrorCtx(ctx, "ingestion: embed batch failed", err, map[string]any{
				"batch_start": start,
				"batch_end":   end,
				"total":       len(inputs),
			})
			return nil, err
		}
		out = append(out, batch...)
		//sayso-lint:ignore err-swallow
		_ = reporter.Inc(end-start, 0)
	}
	return out, nil
}

// isFatalEmbedError 判断一个 embed error 是否应终止整轮 ingest。
//
// 致命 = 配置 / schema 问题,重试其他 doc 一样炸:
//
//   - ErrEmbeddingAuth         :api_key 错 / 权限被撤
//   - ErrEmbeddingDimMismatch  :维度和 schema 对不上
//
// **ErrEmbeddingInvalid 刻意不算 fatal**:Azure 400 常见原因是"单条 input 超 context length"
// (中文注释密集的大文件最容易撞),per-doc 问题;让它走"持久 failed 行,继续下一 doc"路径,
// 比"一个病态文件让整轮 sync 炸"好得多。
func isFatalEmbedError(err error) bool {
	return errors.Is(err, embedding.ErrEmbeddingAuth) ||
		errors.Is(err, embedding.ErrEmbeddingDimMismatch)
}

// validateDoc 基础不变量检查。OrgID / SourceType / SourceID 缺一即拒。
// 辅助函数,无 logger;调用方 Prepare 会在 err return 前统一打日志。
func validateDoc(doc *NormalizedDoc) error {
	if doc == nil {
		//sayso-lint:ignore log-coverage
		return errors.New("nil doc")
	}
	if doc.OrgID == 0 {
		//sayso-lint:ignore log-coverage
		return errors.New("org_id is zero")
	}
	if doc.SourceType == "" {
		//sayso-lint:ignore log-coverage
		return errors.New("source_type is empty")
	}
	if doc.SourceID == "" {
		//sayso-lint:ignore log-coverage
		return errors.New("source_id is empty")
	}
	return nil
}

// truncateUTF8 按 rune 边界截到不超过 maxBytes。续字节高 2 位是 10,
// 遇到就往前退一字节直到落在起始字节上。
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	for maxBytes > 0 && (s[maxBytes]&0xC0) == 0x80 {
		maxBytes--
	}
	return s[:maxBytes]
}

// ─── 进度上报 ───────────────────────────────────────────────────────────────

// ProgressReporter pipeline 每完成 / 失败一个 doc 调一次。
//
// 形状和 internal/asyncjob/service.ProgressReporter 对齐 —— 装配层把 asyncjob 的
// reporter 直接传进来(或薄 adapter 一下)即可。不直接 import asyncjob 是为了避免
// ingestion → asyncjob 的循环依赖隐患(asyncjob 未来可能 import ingestion 跑 runner)。
type ProgressReporter interface {
	SetTotal(n int) error
	Inc(success, failed int) error
}

type noopReporter struct{}

// SetTotal 空实现,reporter 缺省时 swallow 掉 total 设置调用。无错误场景,返回永远为 nil。
func (noopReporter) SetTotal(int) error { return nil }

// Inc 空实现,reporter 缺省时 swallow 掉进度增量调用。无错误场景,返回永远为 nil。
func (noopReporter) Inc(int, int) error { return nil }
