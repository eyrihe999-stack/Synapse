// Package codeadapter 把 internal/code/service.SearchService 适配到 retrieval.Retriever 协议。
//
// 关键映射:
//   - Mode=default/hybrid  → 两路召回都保留(服务默认行为)
//   - Mode=vector          → 只保留向量命中
//   - Mode=symbol          → 只保留符号命中;CodeFilter.SymbolName 非空时替换 query
//   - Mode=bm25            → 不支持,log warn + 降级为 vector
//
// Rerank 当前 SearchService 不支持,置 true 只 log warn,不报错。
package codeadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/eyrihe999-stack/Synapse/internal/code/model"
	"github.com/eyrihe999-stack/Synapse/internal/code/repository"
	codesvc "github.com/eyrihe999-stack/Synapse/internal/code/service"
	"github.com/eyrihe999-stack/Synapse/internal/retrieval"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
)

// Adapter 实现 retrieval.Retriever。构造后并发安全,无可变状态。
type Adapter struct {
	svc  codesvc.SearchService
	repo repository.Repository // FetchByID 直接打 repo,绕开 SearchService 的相关度检索开销
	log  logger.LoggerInterface
}

// New 构造。svc / repo / log 必须非 nil(适配器不做降级,装配期就该报错)。
func New(svc codesvc.SearchService, repo repository.Repository, log logger.LoggerInterface) *Adapter {
	if svc == nil || repo == nil || log == nil {
		panic("codeadapter: svc, repo and log must be non-nil")
	}
	return &Adapter{svc: svc, repo: repo, log: log}
}

func (a *Adapter) Modality() retrieval.Modality { return retrieval.ModalityCode }

func (a *Adapter) FilterSchema() json.RawMessage { return retrieval.CodeFilterSchema() }

// Search 调用 SearchCode → 按 Mode / ChunkKinds 过滤 → 归一化到 retrieval.Hit。
func (a *Adapter) Search(ctx context.Context, q retrieval.Query) ([]retrieval.Hit, error) {
	if q.OrgID == 0 {
		return nil, errors.New("codeadapter: orgID required")
	}

	var f retrieval.CodeFilter
	if len(q.Filter) > 0 {
		if err := json.Unmarshal(q.Filter, &f); err != nil {
			return nil, fmt.Errorf("codeadapter: parse filter: %w", err)
		}
	}

	if q.Rerank {
		a.log.WarnCtx(ctx, "codeadapter: rerank requested but not supported; ignoring", map[string]any{"org_id": q.OrgID})
	}

	mode := q.Mode
	if mode == retrieval.ModeBM25 {
		a.log.WarnCtx(ctx, "codeadapter: bm25 mode not supported; falling back to vector", map[string]any{"org_id": q.OrgID})
		mode = retrieval.ModeVector
	}

	// 当 Mode=symbol 且 SymbolName 明确给了,用 SymbolName 作为主查询 —— 比自然语言 query 更精准。
	// 其余情况(包括 Mode=symbol 但 SymbolName 空)直接用 q.Text。
	query := q.Text
	if mode == retrieval.ModeSymbol && f.SymbolName != "" {
		query = f.SymbolName
	}

	opts := codesvc.SearchOptions{
		Languages:       f.Languages,
		RepoIDs:         f.RepoIDs,
		TopK:            q.TopK,
		IncludeSiblings: f.IncludeSiblings,
		SiblingWindow:   f.SiblingWindow,
	}

	res, err := a.svc.SearchCode(ctx, q.OrgID, query, opts)
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, nil
	}

	// Mode / ChunkKinds 过滤放在 adapter 层。SearchService 不暴露 mode 开关,过滤是后置的 ——
	// 付出的代价是两路仍都跑,但 topK 已经限了量,额外开销可忽略。
	hits := make([]retrieval.Hit, 0, len(res.Hits))
	for _, h := range res.Hits {
		if h == nil || h.Chunk == nil {
			continue
		}
		if !matchMode(mode, h.MatchSource) {
			continue
		}
		if !matchChunkKinds(h.Chunk.ChunkKind, f.ChunkKinds) {
			continue
		}
		hits = append(hits, toHit(h))
	}
	return hits, nil
}

// FetchByID 按 "code:{chunk_id}" 精确拉一条 chunk,回填 file / repo 元信息,Snippet 不截断。
// 用途:agent 拿到 search 结果后觉得 snippet 不够,显式 fetch 全文。
// Score=1 / Scorer="fetch" —— 非相关度命中,标明来源用。
func (a *Adapter) FetchByID(ctx context.Context, orgID uint64, id string) (*retrieval.Hit, error) {
	if orgID == 0 {
		return nil, errors.New("codeadapter: orgID required")
	}
	chunkID, err := ParseID(id)
	if err != nil {
		return nil, err
	}
	chunk, err := a.repo.Chunks().GetByID(ctx, orgID, chunkID)
	if err != nil {
		return nil, err
	}
	if chunk == nil {
		return nil, nil
	}

	// file / repo 元信息取不到不 fatal —— chunk 自身内容已拿到,agent 至少能看正文。
	files, fErr := a.repo.Files().GetByIDs(ctx, []uint64{chunk.FileID})
	if fErr != nil {
		a.log.WarnCtx(ctx, "codeadapter: fetch file failed", map[string]any{"file_id": chunk.FileID, "err": fErr.Error()})
		files = map[uint64]*model.CodeFile{}
	}
	repos, rErr := a.repo.Repos().GetByIDs(ctx, []uint64{chunk.RepoID})
	if rErr != nil {
		a.log.WarnCtx(ctx, "codeadapter: fetch repo failed", map[string]any{"repo_id": chunk.RepoID, "err": rErr.Error()})
		repos = map[uint64]*model.CodeRepository{}
	}

	webURL := ""
	repoPath := ""
	if r, ok := repos[chunk.RepoID]; ok {
		webURL = r.WebURL
		repoPath = r.PathWithNamespace
	}

	hit := retrieval.Hit{
		ID:       FormatID(chunk.ID),
		Modality: retrieval.ModalityCode,
		Score:    1.0,
		Scorer:   "fetch",
		SourceRef: retrieval.SourceRef{
			Kind: "git",
			Repo: repoPath,
			Line: &retrieval.LineRange{Start: chunk.LineStart, End: chunk.LineEnd},
		},
		Snippet:  chunk.Content, // 不截断,agent 明确要求全文
		Metadata: buildMetadata(chunk, webURL),
	}
	if f, ok := files[chunk.FileID]; ok {
		hit.SourceRef.Path = f.Path
	}
	return &hit, nil
}

func matchMode(mode retrieval.RetrieveMode, matchSource string) bool {
	switch mode {
	case retrieval.ModeVector:
		return matchSource == "vector"
	case retrieval.ModeSymbol:
		return matchSource == "symbol"
	default:
		return true
	}
}

func matchChunkKinds(kind string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	return slices.Contains(allowed, kind)
}

func toHit(h *codesvc.Hit) retrieval.Hit {
	chunk := h.Chunk
	hit := retrieval.Hit{
		ID:       FormatID(chunk.ID),
		Modality: retrieval.ModalityCode,
		Score:    normalizeScore(h.Distance, h.MatchSource),
		Scorer:   h.MatchSource,
		SourceRef: retrieval.SourceRef{
			Kind: "git",
			Line: &retrieval.LineRange{Start: chunk.LineStart, End: chunk.LineEnd},
		},
		Snippet: truncateSnippet(chunk.Content, retrieval.SnippetMaxChars),
	}
	webURL := ""
	if h.Repo != nil {
		hit.SourceRef.Repo = h.Repo.PathWithNamespace
		webURL = h.Repo.WebURL
	}
	hit.Metadata = buildMetadata(chunk, webURL)
	if h.File != nil {
		hit.SourceRef.Path = h.File.Path
	}
	if len(h.Siblings) > 0 {
		hit.Related = make([]string, 0, len(h.Siblings))
		for _, sib := range h.Siblings {
			if sib == nil {
				continue
			}
			hit.Related = append(hit.Related, FormatID(sib.ID))
		}
	}
	return hit
}

// normalizeScore pgvector cosine distance / symbol 命中 → 相似度 [0,1]。
// symbol 命中 service 层给的 distance=0("精确命中")归一化成 1.0,排在前面。
func normalizeScore(distance float32, matchSource string) float32 {
	if matchSource == "symbol" {
		return 1.0
	}
	s := 1.0 - distance/2.0
	if s < 0 {
		return 0
	}
	if s > 1 {
		return 1
	}
	return s
}

// FormatID "code:{chunk_id}"。跨模态 ID 前缀防撞号。
func FormatID(chunkID uint64) string {
	return "code:" + strconv.FormatUint(chunkID, 10)
}

// ParseID "code:123" → 123。给 FetchByID / 调试用。
func ParseID(id string) (uint64, error) {
	const prefix = "code:"
	if !strings.HasPrefix(id, prefix) {
		return 0, fmt.Errorf("codeadapter: invalid ID prefix: %q", id)
	}
	return strconv.ParseUint(strings.TrimPrefix(id, prefix), 10, 64)
}

func buildMetadata(chunk *model.CodeChunk, webURL string) json.RawMessage {
	m := map[string]any{
		"chunk_kind": chunk.ChunkKind,
		"language":   chunk.Language,
	}
	if chunk.SymbolName != "" {
		m["symbol"] = chunk.SymbolName
	}
	if chunk.Signature != "" {
		m["signature"] = chunk.Signature
	}
	if webURL != "" {
		m["web_url"] = webURL
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
