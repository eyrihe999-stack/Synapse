// index_pipeline.go Upload 链路中的"切块 → 向量化 → 原子写 chunks"子流程。
//
// 这个文件只做"索引路径"这一件事,和主 Upload 流程解耦:
//   - 主流程(document_service.go::Upload)负责 MySQL + OSS 的主写 + 补偿编排。
//   - 本文件 indexContent 负责把 content 转成 chunk + 向量,然后一次性落 PG。
//
// 失败语义是分层的:
//   - 致命错误(embedder 配置/鉴权错、PG 不可达)→ 返回已 wrap ErrDocumentIndexFailed/Internal 的 error,
//     让 Upload 主流程看到后走补偿(删 OSS + 删 MySQL)。
//   - 可重试错误(Network/429/Server)→ chunks 以 failed 状态入 PG,本函数返回 nil。
//     这些行会被后续的后台补偿任务扫到并重试 embed,整单 Upload 仍视为"成功"。
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/pgvector/pgvector-go"
	"gorm.io/datatypes"

	"github.com/eyrihe999-stack/Synapse/internal/document"
	"github.com/eyrihe999-stack/Synapse/internal/document/model"
	"github.com/eyrihe999-stack/Synapse/pkg/chunker"
	"github.com/eyrihe999-stack/Synapse/pkg/embedding"
	"github.com/eyrihe999-stack/Synapse/pkg/tokenizer"
)

// indexEmbedTimeout 单次批量 embed 调用的兜底超时。
//
// Azure text-embedding-3-large 正常 <1s,批量 ≤3s;30s 覆盖尾延迟 + TLS 抖动 + 跨区网络抖动。
// 此处是"内部兜底",调用方(Upload)的 ctx 已带用户请求级超时时则以更严格者为准。
const indexEmbedTimeout = 30 * time.Second

// indexErrorSnippetMax chunk.index_error 在本层先做一次 ≤512 字符截断,让 pg 侧日志更好读;
// chunk repo 还有 1KB 的二次截断兜底,两道保险。
const indexErrorSnippetMax = 512

// indexingEnabled 三元依赖(chunkers / embedder / chunkRepo)都就绪才启用索引。
// 任一为 nil:整段 Upload 路径跳过索引,Upload 依然成功。
// 构造期已做"全有或全无"校验,这里只是运行时一处读。
func (s *service) indexingEnabled() bool {
	return s.chunkers != nil && s.embedder != nil && s.chunkRepo != nil
}

// indexContent 对一篇刚落库 + 刚上 OSS 的文档执行"切块 → 向量化 → 批量 INSERT chunks"。
//
// 前置条件(调用方保证):
//   - docID 对应的 documents 行已 commit 到 MySQL;
//   - 这次 content 的 OSS 对象已 PUT 成功(用户视角的"文档原文"已持久化)。
//
// mimeType / fileName 用于路由到合适的 Profile —— Markdown 走结构化切分、其他走 plain_text。
//
// 返回 nil:chunks 全部(或大部分)落盘;上游视为 Upload 成功。
// 返回非 nil:已 wrap document.Err 的 sentinel,上游必须走补偿清理。
func (s *service) indexContent(ctx context.Context, docID, orgID uint64, content, mimeType, fileName string) error {
	profile := s.chunkers.Pick(mimeType, fileName)
	pieces := profile.Chunk(content)
	if len(pieces) == 0 {
		// 空或纯空白文档。无 chunks 可索引,但 Upload 仍视为成功 —— 用户可能上传占位文件。
		return nil
	}

	embedCtx, cancel := context.WithTimeout(ctx, indexEmbedTimeout)
	defer cancel()

	inputs := make([]string, len(pieces))
	for i, p := range pieces {
		inputs[i] = p.Content
	}

	vecs, embedErr := s.embedder.Embed(embedCtx, inputs)

	// 致命 embed 错误 = 配置问题,重试也会失败。直接让上游回滚整单。
	if embedErr != nil && isFatalEmbedError(embedErr) {
		s.logger.ErrorCtx(ctx, "document: index embed fatal", embedErr, map[string]any{"doc_id": docID, "org_id": orgID})
		return fmt.Errorf("embed fatal: %w: %w", embedErr, document.ErrDocumentIndexFailed)
	}

	rows := buildChunkRows(docID, orgID, profile, pieces, vecs, embedErr, s.embedderModel(), s.tokenizer)

	if err := s.chunkRepo.InsertChunks(ctx, rows); err != nil {
		s.logger.ErrorCtx(ctx, "document: insert chunks failed", err, map[string]any{"doc_id": docID, "org_id": orgID})
		// PG 不可达或 schema 不对 → Upload 必须回滚,否则 doc 行永远找不到它的 chunks。
		return fmt.Errorf("insert chunks: %w: %w", err, document.ErrDocumentInternal)
	}
	return nil
}

// buildChunkRows 把 chunker 输出 + embed 结果组装成待 INSERT 的行。
//
// 两种主分支:
//   - embedErr == nil:每行 Embedding 填好 + IndexStatus=indexed;
//   - embedErr 是"可重试"类:每行 Embedding 留 nil + IndexStatus=failed + IndexError 记原始错误摘要。
//     (致命错误已在上游被拦下,不会走到这里。)
//
// T1.3 新增写入字段:
//   - Metadata:当前只存 heading_path(有则存,没有就留 NULL);未来 T2 多源时此处扩展 source_ref 等。
//   - ContentType / ChunkLevel:按 piece 透传(plain_text 固定 "text"/0,markdown 带结构信息)。
//   - ChunkerVersion:从 profile.Version() 取,支持"新老 chunker 共存 + 读侧可选过滤"。
//
// 行 content_hash 用 sha256,让后台补偿扫到 failed 行后可以跳过"内容已改/已删"的行。
func buildChunkRows(
	docID, orgID uint64,
	profile chunker.Profile,
	pieces []chunker.Piece,
	vecs [][]float32,
	embedErr error,
	modelTag string,
	tk tokenizer.Tokenizer,
) []*model.DocumentChunk {
	rows := make([]*model.DocumentChunk, len(pieces))
	errSnippet := ""
	if embedErr != nil {
		errSnippet = truncateErrMsg(embedErr.Error())
	}
	chunkerVer := profile.Version()
	for i, p := range pieces {
		contentType := p.ContentType
		if contentType == "" {
			contentType = "text"
		}
		// tsv_tokens 是 BM25 通路的原料:Go 侧分词 → 空格串 → 写列 → PG 自动算 content_tsv。
		// tk=nil(配置未启用 BM25)时留空,content_tsv 为空 tsvector,BM25 通路自然匹不到,
		// 不影响 vector 通路 —— 这就是"BM25 可选"的兜底行为。
		tsvTokens := ""
		if tk != nil {
			tsvTokens = tk.TokensString(p.Content)
		}
		row := &model.DocumentChunk{
			DocID:          docID,
			OrgID:          orgID,
			ChunkIdx:       i,
			Content:        p.Content,
			ContentHash:    sha256Hex(p.Content),
			TokenCount:     p.TokenCount,
			Metadata:       marshalPieceMetadata(p),
			ContentType:    contentType,
			ChunkLevel:     p.ChunkLevel,
			ChunkerVersion: chunkerVer,
			TsvTokens:      tsvTokens,
		}
		if embedErr == nil {
			vec := pgvector.NewVector(vecs[i])
			row.Embedding = &vec
			row.EmbeddingModel = modelTag
			row.IndexStatus = document.ChunkIndexStatusIndexed
		} else {
			row.IndexStatus = document.ChunkIndexStatusFailed
			row.IndexError = errSnippet
		}
		rows[i] = row
	}
	return rows
}

// marshalPieceMetadata 把 Piece 的结构化字段序列化为 metadata jsonb。
//
// 约定 schema:
//   {"heading_path": ["架构", "数据模型"]}  // 仅当 HeadingPath 非空
//
// 返回 nil 让 GORM 对 jsonb 列 omit(PG 默认 '{}'),避免把空对象写成 "{}" 占存储;
// 有 heading_path 才真正发 JSON。将来加其他字段(tags / source_ref / ...)直接在本函数拼。
func marshalPieceMetadata(p chunker.Piece) datatypes.JSON {
	if len(p.HeadingPath) == 0 {
		return nil
	}
	payload := map[string]any{"heading_path": p.HeadingPath}
	raw, err := json.Marshal(payload)
	if err != nil {
		// heading_path 是纯 string slice,理论上不可能失败。真出问题时吞掉,让这行 metadata 缺失,
		// 不阻塞 upload 主流程。
		return nil
	}
	return datatypes.JSON(raw)
}

// isFatalEmbedError 判断这个 embed 错误是否应该整单回滚 Upload。
//
// 致命:Auth / Invalid / DimMismatch —— 配置或 schema 错,重试无用。
// 非致命:Network / RateLimited / Server —— 可后台重试,本次写 failed 行。
func isFatalEmbedError(err error) bool {
	return errors.Is(err, embedding.ErrEmbeddingAuth) ||
		errors.Is(err, embedding.ErrEmbeddingInvalid) ||
		errors.Is(err, embedding.ErrEmbeddingDimMismatch)
}

// embedderModel 读 embedder.Model 的小 wrapper,留一个位置方便未来注入 mock / decorator。
// 现在 trivial 一行,封 getter 以便 index_pipeline 的调用不用每次判 nil(embedder 进入此方法时一定非 nil)。
func (s *service) embedderModel() string {
	return s.embedder.Model()
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func truncateErrMsg(s string) string {
	if len(s) <= indexErrorSnippetMax {
		return s
	}
	return s[:indexErrorSnippetMax-3] + "..."
}
