// Package docadapter 把 internal/document/service.DocumentService 适配到 retrieval.Retriever 协议。
//
// Mode 映射:
//   - default/vector/bm25/hybrid → 直通 docsvc.RetrievalMode 对应值
//   - symbol                    → symbol 是 code 独有;log warn + 降级为 vector
//
// Rerank 直通 SearchChunksOptions.Rerank。reranker 未注入时由 service 层自行降级 + log。
package docadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"

	"github.com/eyrihe999-stack/Synapse/internal/document/dto"
	docrepo "github.com/eyrihe999-stack/Synapse/internal/document/repository"
	docsvc "github.com/eyrihe999-stack/Synapse/internal/document/service"
	"github.com/eyrihe999-stack/Synapse/internal/retrieval"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
)

// Adapter 实现 retrieval.Retriever。构造后并发安全,无可变状态。
type Adapter struct {
	svc       docsvc.DocumentService
	chunkRepo docrepo.ChunkRepository // FetchByID / ReturnParents 直接打 repo
	log       logger.LoggerInterface
}

func New(svc docsvc.DocumentService, chunkRepo docrepo.ChunkRepository, log logger.LoggerInterface) *Adapter {
	if svc == nil || chunkRepo == nil || log == nil {
		panic("docadapter: svc, chunkRepo and log must be non-nil")
	}
	return &Adapter{svc: svc, chunkRepo: chunkRepo, log: log}
}

func (a *Adapter) Modality() retrieval.Modality { return retrieval.ModalityDocument }

func (a *Adapter) FilterSchema() json.RawMessage { return retrieval.DocumentFilterSchema() }

// Search 组装 SearchChunksOptions → 调 DocumentService.SearchChunks → 归一化到 retrieval.Hit。
// ReturnParents 开启时,一次批量 GetByIDs 把父 chunk ID 填进 Hit.Related。
func (a *Adapter) Search(ctx context.Context, q retrieval.Query) ([]retrieval.Hit, error) {
	if q.OrgID == 0 {
		return nil, errors.New("docadapter: orgID required")
	}

	var f retrieval.DocumentFilter
	if len(q.Filter) > 0 {
		if err := json.Unmarshal(q.Filter, &f); err != nil {
			return nil, fmt.Errorf("docadapter: parse filter: %w", err)
		}
	}

	if q.Mode == retrieval.ModeSymbol {
		a.log.WarnCtx(ctx, "docadapter: symbol mode not supported for document; falling back to vector", map[string]any{"org_id": q.OrgID})
	}

	opts := &docsvc.SearchChunksOptions{
		Mode:           mapMode(q.Mode),
		Rerank:         q.Rerank,
		MaxPerDoc:      f.MaxPerDoc,
		MinSimilarity:  f.MinSimilarity,
		MinRerankScore: f.MinRerankScore,
	}
	if len(f.DocIDs) > 0 || len(f.HeadingPathContains) > 0 {
		opts.Filter = &docrepo.ChunkSearchFilter{
			DocIDs:              f.DocIDs,
			HeadingPathContains: f.HeadingPathContains,
		}
	}

	resp, err := a.svc.SearchChunks(ctx, q.OrgID, q.Text, q.TopK, opts)
	if err != nil {
		return nil, err
	}
	if resp == nil || len(resp.Items) == 0 {
		return nil, nil
	}

	hits := make([]retrieval.Hit, len(resp.Items))
	for i := range resp.Items {
		hits[i] = itemToHit(&resp.Items[i])
	}

	if f.ReturnParents {
		// 父展开失败不 fatal —— 原 hits 还能用,log warn 让上层有线索。
		if err := a.expandParents(ctx, q.OrgID, hits, resp.Items); err != nil {
			a.log.WarnCtx(ctx, "docadapter: parent expansion failed", map[string]any{"org_id": q.OrgID, "err": err.Error()})
		}
	}

	return hits, nil
}

// FetchByID 按 "document:{chunk_id}" 精确拉一条 chunk,Snippet 不截断。
// 顺便取 doc metadata(title/source)放进 Hit.Metadata;拉失败不 fatal。
func (a *Adapter) FetchByID(ctx context.Context, orgID uint64, id string) (*retrieval.Hit, error) {
	if orgID == 0 {
		return nil, errors.New("docadapter: orgID required")
	}
	chunkID, err := ParseID(id)
	if err != nil {
		return nil, err
	}
	chunk, err := a.chunkRepo.GetByID(ctx, orgID, chunkID)
	if err != nil {
		return nil, err
	}
	if chunk == nil {
		return nil, nil
	}

	docTitle := ""
	docSource := ""
	if d, dErr := a.svc.Get(ctx, orgID, chunk.DocID); dErr != nil {
		a.log.WarnCtx(ctx, "docadapter: fetch doc metadata failed", map[string]any{"doc_id": chunk.DocID, "err": dErr.Error()})
	} else if d != nil {
		docTitle = d.Title
		docSource = d.Source
	}

	hit := retrieval.Hit{
		ID:       FormatID(chunk.ID),
		Modality: retrieval.ModalityDocument,
		Score:    1.0,
		Scorer:   "fetch",
		SourceRef: retrieval.SourceRef{
			Kind:  "doc",
			DocID: strconv.FormatUint(chunk.DocID, 10),
		},
		Snippet:  chunk.Content,
		Metadata: buildMetadata([]byte(chunk.Metadata), chunk.ContentType, docTitle, docSource),
	}
	if chunk.ParentChunkID != nil {
		hit.Related = []string{FormatID(*chunk.ParentChunkID)}
	}
	return &hit, nil
}

// expandParents 收集 items 里非 nil 的 parent_chunk_id,批量取存在性后追加到 Hit.Related。
// items 和 hits 一一对应(同一次 Search 的输出,顺序相同)。
func (a *Adapter) expandParents(ctx context.Context, orgID uint64, hits []retrieval.Hit, items []dto.ChunkSearchResult) error {
	parentIDs := make([]uint64, 0, len(items))
	seen := make(map[uint64]struct{}, len(items))
	for _, it := range items {
		if it.ParentChunkID == nil {
			continue
		}
		pid := *it.ParentChunkID
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		parentIDs = append(parentIDs, pid)
	}
	if len(parentIDs) == 0 {
		return nil
	}
	parents, err := a.chunkRepo.GetByIDs(ctx, orgID, parentIDs)
	if err != nil {
		return err
	}
	// 只追加真正存在的 parent ID(GetByIDs 已按 orgID 过滤,不存在 = 跨 org 或已删,跳过)。
	for i := range items {
		if items[i].ParentChunkID == nil {
			continue
		}
		pid := *items[i].ParentChunkID
		if _, ok := parents[pid]; ok {
			hits[i].Related = append(hits[i].Related, FormatID(pid))
		}
	}
	return nil
}

func itemToHit(it *dto.ChunkSearchResult) retrieval.Hit {
	scorer := "similarity"
	if it.RerankScore != nil {
		scorer = "rerank"
	}
	return retrieval.Hit{
		ID:       FormatID(it.ChunkID),
		Modality: retrieval.ModalityDocument,
		Score:    it.Similarity, // 始终归一化 similarity;rerank 分数量纲不统一,放 metadata 里给 debug
		Scorer:   scorer,
		SourceRef: retrieval.SourceRef{
			Kind:  "doc",
			DocID: strconv.FormatUint(it.DocID, 10),
		},
		Snippet:  truncateSnippet(it.Content, retrieval.SnippetMaxChars),
		Metadata: buildMetadata(it.Metadata, it.ContentType, it.DocTitle, it.DocSource),
	}
}

func mapMode(m retrieval.RetrieveMode) docsvc.RetrievalMode {
	switch m {
	case retrieval.ModeVector:
		return docsvc.RetrievalVector
	case retrieval.ModeBM25:
		return docsvc.RetrievalBM25
	case retrieval.ModeHybrid:
		return docsvc.RetrievalHybrid
	case retrieval.ModeSymbol:
		return docsvc.RetrievalVector // symbol 是 code 独有,降级
	default:
		return docsvc.RetrievalDefault
	}
}

// FormatID "document:{chunk_id}"。跨模态 ID 前缀防撞号。
func FormatID(chunkID uint64) string {
	return "document:" + strconv.FormatUint(chunkID, 10)
}

// ParseID "document:123" → 123。给 FetchByID / 调试用。
func ParseID(id string) (uint64, error) {
	const prefix = "document:"
	if !strings.HasPrefix(id, prefix) {
		return 0, fmt.Errorf("docadapter: invalid ID prefix: %q", id)
	}
	return strconv.ParseUint(strings.TrimPrefix(id, prefix), 10, 64)
}

// buildMetadata 原 jsonb metadata 作为扁平基底,再叠加 content_type / doc_title / doc_source。
// chunker 存的键(heading_path / tags / source_type 等)和适配器键同层,agent 按键取用。
// 原 metadata 和附加键冲突时,附加键覆盖(content_type / doc_title 是 adapter 语义,更权威)。
func buildMetadata(raw json.RawMessage, contentType, docTitle, docSource string) json.RawMessage {
	m := map[string]any{}
	if len(raw) > 0 {
		var inner map[string]any
		if err := json.Unmarshal(raw, &inner); err == nil {
			maps.Copy(m, inner)
		}
	}
	if contentType != "" {
		m["content_type"] = contentType
	}
	if docTitle != "" {
		m["doc_title"] = docTitle
	}
	if docSource != "" {
		m["doc_source"] = docSource
	}
	if len(m) == 0 {
		return nil
	}
	b, _ := json.Marshal(m)
	return b
}

func truncateSnippet(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}
