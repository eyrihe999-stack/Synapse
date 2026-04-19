// search_service.go 文档语义搜索(pgvector ANN → MySQL JOIN 过滤孤儿 → 返文档级命中)。
package service

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/eyrihe999-stack/Synapse/internal/document"
	"github.com/eyrihe999-stack/Synapse/internal/document/dto"
	"github.com/eyrihe999-stack/Synapse/internal/document/model"
	"github.com/eyrihe999-stack/Synapse/internal/document/repository"
)

// SemanticSearch 见 DocumentService 接口文档。
//
// 一致性/过滤语义:
//   - 以 **MySQL documents 行的存在性** 为权威。PG 中可能有 chunks 但 MySQL 已删(孤儿),
//     通过 FindDocumentsByIDs 的 IN 查询天然过滤掉,不泄露"幽灵文档"。
//   - chunks 只有 index_status='indexed' 的才参与检索(SearchByVector 内置过滤)。
//   - 同一 doc 的多个 chunks 可能都命中 top_k_chunks,按 doc_id dedup 取距离最小的那个。
//
// 成本:每次调用 = 1 次 Azure embed(query)+ 1 次 pg ANN + 1 次 mysql IN 查询 + orgPort user 查询。
// Azure embed 带 indexEmbedTimeout 超时兜底。
//
// 失败场景:
//   - 索引能力未启用(chunkRepo/embedder 为 nil) → ErrDocumentIndexFailed。
//   - orgID 为 0 → ErrDocumentInvalidRequest。
//   - embed / pg / mysql 故障 → ErrDocumentIndexFailed 或 ErrDocumentInternal(wrap 原错)。
func (s *service) SemanticSearch(ctx context.Context, orgID uint64, query string, topK int) (*dto.ListDocumentsResponse, error) {
	if !s.indexingEnabled() {
		s.logger.WarnCtx(ctx, "document: semantic search disabled (indexing deps missing)", map[string]any{"org_id": orgID})
		return nil, fmt.Errorf("semantic search disabled (indexing deps missing): %w", document.ErrDocumentIndexFailed)
	}
	if orgID == 0 {
		s.logger.WarnCtx(ctx, "document: semantic search rejected, missing orgID", nil)
		return nil, document.ErrDocumentInvalidRequest
	}
	query = strings.TrimSpace(query)
	if query == "" {
		// 空 query 直接返空,不打 Azure。前端切到语义模式但还没输入时不应当产生请求,但防御一下。
		return emptyListResponse(topK), nil
	}
	if runeLen := len([]rune(query)); runeLen > document.MaxQueryLength {
		query = string([]rune(query)[:document.MaxQueryLength])
	}
	if topK <= 0 || topK > document.MaxSemanticTopK {
		topK = document.DefaultSemanticTopK
	}

	// Step 1:embed query(单次 Azure 调用)。失败按致命/可重试共同映射到 IndexFailed,
	// semantic search 是用户交互入口,不像 Upload 那种异步补偿路径,统一让 UI 看到"搜索暂不可用"。
	embedCtx, cancel := context.WithTimeout(ctx, indexEmbedTimeout)
	defer cancel()
	vecs, err := s.embedder.Embed(embedCtx, []string{query})
	if err != nil {
		s.logger.WarnCtx(ctx, "semantic search: embed failed", map[string]any{"org_id": orgID, "err": err.Error()})
		return nil, fmt.Errorf("embed query: %w: %w", err, document.ErrDocumentIndexFailed)
	}
	if len(vecs) != 1 || len(vecs[0]) == 0 {
		return nil, fmt.Errorf("embed returned unexpected shape: %w", document.ErrDocumentInternal)
	}
	queryVec := vecs[0]

	// Step 2:pg ANN。请求 topK*5 chunks 给 dedup 留量 —— 单篇长文可能占很多 top chunks。
	// SemanticSearch 目前不暴露 filter 参数(doc 级 API,metadata 过滤放在 chunk 级 SearchChunks 里)。
	chunkLimit := topK * 5
	hits, err := s.chunkRepo.SearchByVector(ctx, orgID, queryVec, chunkLimit, nil)
	if err != nil {
		s.logger.ErrorCtx(ctx, "semantic search: pg ANN failed", err, map[string]any{"org_id": orgID})
		return nil, errWrap(document.ErrDocumentInternal, err)
	}
	if len(hits) == 0 {
		return emptyListResponse(topK), nil
	}

	// Step 3:按 doc_id dedup 保留最优 chunk(首次出现即距离最小,因 hits 是升序)。
	type bestHit struct {
		distance float32
		snippet  string
	}
	bestByDoc := make(map[uint64]bestHit, topK)
	orderedDocIDs := make([]uint64, 0, topK)
	for _, h := range hits {
		if h.Chunk == nil {
			continue
		}
		id := h.Chunk.DocID
		if _, seen := bestByDoc[id]; seen {
			continue
		}
		bestByDoc[id] = bestHit{distance: h.Distance, snippet: h.Chunk.Content}
		orderedDocIDs = append(orderedDocIDs, id)
		if len(orderedDocIDs) >= topK {
			break
		}
	}

	// Step 4:MySQL 批取 metadata,过滤孤儿(pg 里有但 MySQL 已删的 doc_id)。
	docs, err := s.docRepo.FindDocumentsByIDs(ctx, orgID, orderedDocIDs)
	if err != nil {
		s.logger.ErrorCtx(ctx, "semantic search: mysql batch fetch failed", err, map[string]any{"org_id": orgID})
		return nil, errWrap(document.ErrDocumentInternal, err)
	}
	docByID := make(map[uint64]*model.Document, len(docs))
	for _, d := range docs {
		docByID[d.ID] = d
	}

	// Step 5:按向量距离顺序组装响应,跳过孤儿。
	items := make([]dto.DocumentResponse, 0, len(orderedDocIDs))
	for _, id := range orderedDocIDs {
		d, ok := docByID[id]
		if !ok {
			s.logger.WarnCtx(ctx, "semantic search: orphan chunk skipped", map[string]any{"doc_id": id, "org_id": orgID})
			continue
		}
		best := bestByDoc[id]
		r := documentToDTO(d)
		r.Similarity = distanceToSimilarity(best.distance)
		r.MatchedSnippet = truncateSnippet(best.snippet, document.SemanticSnippetChars)
		items = append(items, r)
	}
	s.fillUploaderNames(ctx, items)

	return &dto.ListDocumentsResponse{
		Items: items,
		Total: int64(len(items)),
		Page:  1,
		Size:  topK,
	}, nil
}

// SearchChunks 见 DocumentService 接口文档。
//
// 和 SemanticSearch 共用前置链路(embed query + pg ANN),差别在后半段:
//   - SemanticSearch:按 doc_id dedup 取最优 chunk,返回 doc 级列表(带最佳 snippet)。
//   - SearchChunks:**不 dedup**,一个 doc 多个 chunk 命中就都返回;供 generator 按
//     chunk 级引用([doc_id:chunk_idx])生成 PRD 用。
//
// 孤儿过滤:JOIN MySQL 仍做,删了的 doc 对应的 chunk 跳过(和 SemanticSearch 一致)。
// topK:用户请求的"最多返多少条 chunk"。因无 dedup,一次 pg 查询拿 topK 条就够,
// 不再放大 topK×5 —— 那个放大系数是 SemanticSearch 的 dedup 余量,这里用不到。
func (s *service) SearchChunks(ctx context.Context, orgID uint64, query string, topK int, opts *SearchChunksOptions) (*dto.ListChunksResponse, error) {
	if !s.indexingEnabled() {
		s.logger.WarnCtx(ctx, "document: chunk search disabled (indexing deps missing)", map[string]any{"org_id": orgID})
		return nil, fmt.Errorf("chunk search disabled (indexing deps missing): %w", document.ErrDocumentIndexFailed)
	}
	if orgID == 0 {
		s.logger.WarnCtx(ctx, "document: chunk search rejected, missing orgID", nil)
		return nil, document.ErrDocumentInvalidRequest
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return emptyChunksResponse(topK), nil
	}
	if runeLen := len([]rune(query)); runeLen > document.MaxQueryLength {
		query = string([]rune(query)[:document.MaxQueryLength])
	}
	if topK <= 0 || topK > document.MaxSemanticTopK {
		topK = document.DefaultSemanticTopK
	}

	var filter *repository.ChunkSearchFilter
	mode := RetrievalDefault
	useRerank := false
	maxPerDoc := 0
	var minRerankScore, minSimilarity *float32
	if opts != nil {
		filter = opts.Filter
		mode = opts.Mode
		useRerank = opts.Rerank
		maxPerDoc = opts.MaxPerDoc
		minRerankScore = opts.MinRerankScore
		minSimilarity = opts.MinSimilarity
	}
	// RetrievalDefault → 默认走 vector。
	//
	// 为什么默认不是 hybrid:
	//   T1.1 完成日(2026-04)23 篇 / 58 query 实测:vector MAP 0.980,hybrid MAP 0.965,BM25 只 0.23。
	//   当前语料规模 + 强语义 embedding 下,BM25 的字面命中优势还没兑现,RRF 融合反而稀释了向量
	//   的 top-1 准确度(-0.035 hit@1)。架构上 hybrid 仍正确,但默认开启会引入轻微回归。
	//
	// 调用方可用 opts.Mode = "hybrid" 显式启用,evalretrieval 的 --mode hybrid 同样。
	// 当语料扩到 5k+ 文档或引入 code/bug 等字面强相关源后,重跑 eval 决定是否翻转默认。
	if mode == RetrievalDefault {
		mode = RetrievalVector
	}

	// 召回池大小由三个条件共同决定:
	//   - rerank 开启 → topK × rerankCandidateMultiplier(rerank 发挥空间)
	//   - MaxPerDoc 开启 → topK × PerDocCapBuffer(cap 过滤后还要留够 topK)
	// 取二者最大值。Reranker 为 nil(配置未注入)时,opts.Rerank 静默降级:召回仍按 topK,不扩池。
	retrieveK := topK
	rerankActive := useRerank && s.reranker != nil
	if rerankActive {
		retrieveK = topK * rerankCandidateMultiplier
	} else if useRerank && s.reranker == nil {
		s.logger.WarnCtx(ctx, "chunk search: opts.Rerank=true but reranker nil; serving retrieve order", map[string]any{"org_id": orgID})
	}
	if maxPerDoc > 0 {
		if expanded := topK * document.PerDocCapBuffer; expanded > retrieveK {
			retrieveK = expanded
		}
	}

	hits, err := s.retrieveChunks(ctx, orgID, query, retrieveK, filter, mode)
	if err != nil {
		return nil, err
	}
	if len(hits) == 0 {
		return emptyChunksResponse(topK), nil
	}

	// Rerank 阶段:二阶段 cross-encoder 重排。失败不 fatal —— 降级返回 retrieve 顺序,让用户至少拿到结果。
	// applyRerank 不再内部截 topK —— 后面的 cap / gate 需要完整池来发挥,截 topK 放到最后。
	if rerankActive {
		reranked, rerr := s.applyRerank(ctx, query, hits, orgID)
		if rerr != nil {
			s.logger.WarnCtx(ctx, "chunk search: rerank failed, falling back to retrieve order", map[string]any{"org_id": orgID, "err": rerr.Error()})
		} else {
			hits = reranked
		}
	}

	// 置信度 gate:按 rerank / similarity 阈值过滤。两个阈值互斥按 rerank 是否生效选 —— rerank 分数
	// 远比 cosine 距离稳定,生效时优先用它;未生效时才用 similarity 兜底。
	// 过滤在 per-doc cap 之前:先把明显不相关的剔掉,cap 再基于"真相关"的候选做多样性筛。
	confidence := ""
	gateActive := false
	if rerankActive && minRerankScore != nil {
		hits = filterByRerankScore(hits, *minRerankScore)
		gateActive = true
	} else if !rerankActive && minSimilarity != nil {
		hits = filterBySimilarity(hits, *minSimilarity)
		gateActive = true
	}
	if gateActive {
		if len(hits) == 0 {
			confidence = document.SearchConfidenceNone
		} else {
			confidence = document.SearchConfidenceHigh
		}
	}

	// Per-doc cap:同 doc 多个 chunk 扎堆时,按 DocID 限流。rerank / gate 之后应用 —— cap 用的是
	// 经过精度筛的顺序,不会把"高分但同文档"的候选早早剔出去。
	if maxPerDoc > 0 && len(hits) > 0 {
		hits = applyPerDocCap(hits, maxPerDoc)
	}

	// 最终截到 topK(cap 后可能仍 > topK,也可能 < topK)。
	if len(hits) > topK {
		hits = hits[:topK]
	}

	if len(hits) == 0 {
		resp := emptyChunksResponse(topK)
		resp.Confidence = confidence
		return resp, nil
	}

	// Step 3: 收集 doc_id 去重(仅用于批取 metadata,不影响 chunk-level 输出顺序)。
	docIDSeen := make(map[uint64]struct{}, len(hits))
	docIDs := make([]uint64, 0, len(hits))
	for _, h := range hits {
		if h.Chunk == nil {
			continue
		}
		if _, ok := docIDSeen[h.Chunk.DocID]; ok {
			continue
		}
		docIDSeen[h.Chunk.DocID] = struct{}{}
		docIDs = append(docIDs, h.Chunk.DocID)
	}

	// Step 4: MySQL 批取 doc metadata,过滤孤儿。
	docs, err := s.docRepo.FindDocumentsByIDs(ctx, orgID, docIDs)
	if err != nil {
		s.logger.ErrorCtx(ctx, "chunk search: mysql batch fetch failed", err, map[string]any{"org_id": orgID})
		return nil, errWrap(document.ErrDocumentInternal, err)
	}
	docByID := make(map[uint64]*model.Document, len(docs))
	for _, d := range docs {
		docByID[d.ID] = d
	}

	// Step 5: 按向量距离顺序组装响应,逐条 chunk 输出(不 dedup)。孤儿 chunk 跳过。
	items := make([]dto.ChunkSearchResult, 0, len(hits))
	for _, h := range hits {
		if h.Chunk == nil {
			continue
		}
		d, ok := docByID[h.Chunk.DocID]
		if !ok {
			s.logger.WarnCtx(ctx, "chunk search: orphan chunk skipped", map[string]any{
				"doc_id": h.Chunk.DocID, "org_id": orgID, "chunk_idx": h.Chunk.ChunkIdx,
			})
			continue
		}
		source := d.Source
		if source == "" {
			source = document.DocSourceUser
		}
		items = append(items, dto.ChunkSearchResult{
			ChunkID:       h.Chunk.ID,
			DocID:         d.ID,
			ChunkIdx:      h.Chunk.ChunkIdx,
			Content:       h.Chunk.Content,
			Similarity:    distanceToSimilarity(h.Distance),
			DocTitle:      d.Title,
			DocSource:     source,
			Metadata:      []byte(h.Chunk.Metadata),
			ParentChunkID: h.Chunk.ParentChunkID,
			ContentType:   h.Chunk.ContentType,
			RerankScore:   h.RerankScore,
		})
	}

	return &dto.ListChunksResponse{
		Items:      items,
		Total:      len(items),
		TopK:       topK,
		Confidence: confidence,
	}, nil
}

// retrieveChunks 按 mode 把 query 分发到 vector / bm25 / hybrid 三条路径之一。
//
// hybrid 路径:向量和 BM25 并发召回 topK×hybridCandidateMultiplier 条,按 RRF 融合后截 topK。
// 扩大召回池是因为 RRF 合并后有效条数会打折(两路重叠部分就算一条),给 2x buffer 让 top-K 稳定。
//
// 失败语义:向量通路失败 = 必 fatal(query 都 embed 不出来 UI 要提示"搜索暂不可用");
// BM25 通路失败 = 只 log 降级到纯 vector(BM25 不是关键路径,不应该连累向量)。
func (s *service) retrieveChunks(
	ctx context.Context,
	orgID uint64,
	query string,
	topK int,
	filter *repository.ChunkSearchFilter,
	mode RetrievalMode,
) ([]repository.ChunkHit, error) {
	switch mode {
	case RetrievalBM25:
		return s.retrieveBM25(ctx, orgID, query, topK, filter)
	case RetrievalVector:
		return s.retrieveVector(ctx, orgID, query, topK, filter)
	case RetrievalHybrid:
		return s.retrieveHybrid(ctx, orgID, query, topK, filter)
	default:
		// 理论上 SearchChunks 已经把 RetrievalDefault 解析成具体 mode 了,兜底 fallback 到 vector。
		return s.retrieveVector(ctx, orgID, query, topK, filter)
	}
}

// hybridCandidateMultiplier 给 RRF 融合留足缓冲。两路各召回 topK*N 条,取合并后的 topK。
// N=3 经验值:工业 RAG 常见 topK=10 → 召回 30,rank 融合后 top-10 稳定。
// 太大浪费 PG 查询,太小会让 RRF 结果边缘漏召回(单路排名靠后但另一路靠前的 chunk)。
const hybridCandidateMultiplier = 3

// rrfK 论文标准值 60,平衡"高排名主导"和"低排名仍有贡献"。可调但几乎所有实现都不动。
const rrfK = 60.0

// rerankCandidateMultiplier rerank 阶段扩召回池的倍率。拿 topK × N 条给 cross-encoder 打分,
// 再截 topK。N=5 经验值:太小则 rerank 发挥不开(好候选没进来),太大则 rerank API 成本翻倍。
// 对 BGE-reranker-v2-m3 CPU 实例,N=5 + topK=10 意味着一次 50 pairs,延迟 ~300ms,可接受。
const rerankCandidateMultiplier = 5

// applyRerank 把 retrieve 返回的 hits 喂给 cross-encoder,返回**全部**重排结果(按 rerank 分数降序)。
//
// 输入 hits 已按 retrieve 层(vector / RRF 等)排过序;rerank 可能完全打乱这个顺序。
// 实现选择把所有 chunk.Content 都送进去 —— 哪怕 content 带了 heading prefix / 较长(几千字),
// BGE tokenizer 会自动截断到 model max_len(v2-m3 是 8192 token)。
//
// 返回切片长度 = 输入长度(modulo out-of-range 异常)。**不在这里截 topK** —— 下游还要跑
// confidence gate + per-doc cap,截早了会让过滤/限流没有替补候选。
//
// 每条 ChunkHit 的 RerankScore 字段被填上 cross-encoder 原始分数,供 gate 阈值判定用。
// Distance 保留原 retrieve 相对分(量纲不同,不覆盖)。
func (s *service) applyRerank(ctx context.Context, query string, hits []repository.ChunkHit, orgID uint64) ([]repository.ChunkHit, error) {
	docs := make([]string, len(hits))
	for i, h := range hits {
		if h.Chunk == nil {
			docs[i] = ""
			continue
		}
		docs[i] = h.Chunk.Content
	}
	results, err := s.reranker.Rerank(ctx, query, docs)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		// reranker 正常返回但空 —— 降级到 retrieve 原顺序(不填 RerankScore,下游 gate 会识别)。
		return hits, nil
	}
	// 按 rerank 分数序回填 hits。results 已按 score DESC 排好。
	reordered := make([]repository.ChunkHit, 0, len(results))
	for _, r := range results {
		if r.Index < 0 || r.Index >= len(hits) {
			s.logger.WarnCtx(ctx, "chunk search: rerank returned out-of-range index", map[string]any{
				"org_id": orgID, "index": r.Index, "hits": len(hits),
			})
			continue
		}
		h := hits[r.Index]
		// 复制分数到一个局部变量再取地址 —— 避免 range 循环变量共享导致所有指针指向最后一次迭代。
		score := r.Score
		h.RerankScore = &score
		reordered = append(reordered, h)
	}
	return reordered, nil
}

// filterByRerankScore 过滤掉 RerankScore < threshold 的 hits。RerankScore 为 nil 视为"未 rerank"
// —— 这种情况理论上不该走到 gate(上层 gateActive 已经校验 rerankActive),防御性保留,nil 也丢弃。
// 输入假定按 rerank 分数降序:遇到第一个低于阈值的直接截断,避免全量扫描。
func filterByRerankScore(hits []repository.ChunkHit, threshold float32) []repository.ChunkHit {
	for i, h := range hits {
		if h.RerankScore == nil || *h.RerankScore < threshold {
			return hits[:i]
		}
	}
	return hits
}

// filterBySimilarity 按 1 - Distance/2 (similarity ∈ [0,1]) 过滤。
// 输入假定按 Distance 升序(= similarity 降序):遇到第一个 < threshold 直接截断。
func filterBySimilarity(hits []repository.ChunkHit, threshold float32) []repository.ChunkHit {
	for i, h := range hits {
		if distanceToSimilarity(h.Distance) < threshold {
			return hits[:i]
		}
	}
	return hits
}

// applyPerDocCap 按 Chunk.DocID 限流:每个 doc_id 最多保留 maxPerDoc 条,保持输入顺序。
// nil chunk 跳过(计数不加,也不出现在结果里)。调用方负责保证 maxPerDoc > 0。
func applyPerDocCap(hits []repository.ChunkHit, maxPerDoc int) []repository.ChunkHit {
	perDoc := make(map[uint64]int, len(hits))
	out := make([]repository.ChunkHit, 0, len(hits))
	for _, h := range hits {
		if h.Chunk == nil {
			continue
		}
		if perDoc[h.Chunk.DocID] >= maxPerDoc {
			continue
		}
		perDoc[h.Chunk.DocID]++
		out = append(out, h)
	}
	return out
}

func (s *service) retrieveVector(
	ctx context.Context,
	orgID uint64,
	query string,
	topK int,
	filter *repository.ChunkSearchFilter,
) ([]repository.ChunkHit, error) {
	vec, err := s.embedQuery(ctx, query, orgID)
	if err != nil {
		return nil, err
	}
	hits, err := s.chunkRepo.SearchByVector(ctx, orgID, vec, topK, filter)
	if err != nil {
		s.logger.ErrorCtx(ctx, "chunk search: pg ANN failed", err, map[string]any{"org_id": orgID})
		return nil, errWrap(document.ErrDocumentInternal, err)
	}
	return hits, nil
}

func (s *service) retrieveBM25(
	ctx context.Context,
	orgID uint64,
	query string,
	topK int,
	filter *repository.ChunkSearchFilter,
) ([]repository.ChunkHit, error) {
	if s.tokenizer == nil {
		// 明确请求 BM25 但没 tokenizer —— 上层传错配置,返空而不是 error,让 A/B 仍能跑完全流程。
		s.logger.WarnCtx(ctx, "chunk search: bm25 requested but tokenizer nil; returning empty", map[string]any{"org_id": orgID})
		return nil, nil
	}
	tokens := s.tokenizer.TokensString(query)
	if tokens == "" {
		return nil, nil
	}
	hits, err := s.chunkRepo.SearchByBM25(ctx, orgID, tokens, topK, filter)
	if err != nil {
		s.logger.ErrorCtx(ctx, "chunk search: pg bm25 failed", err, map[string]any{"org_id": orgID})
		return nil, errWrap(document.ErrDocumentInternal, err)
	}
	return hits, nil
}

// retrieveHybrid 向量 + BM25 并发召回 → Reciprocal Rank Fusion 合并。
//
// RRF 公式:score(doc) = Σ 1 / (rrfK + rank_in_list_i)
// 每条在某路径召回的 chunk 按它在该路径的排名(1-based)贡献一个倒数;两路都命中的 chunk
// 贡献两次 → 天然排到前面。纯向量或纯字面强命中的也会保留。
//
// 并发策略:两路独立 PG 查询,goroutine 并跑 + sync.WaitGroup 等齐。任一路失败:
//   - vector 失败 → 整个 hybrid 失败(embed 不出 query 就是硬错)
//   - BM25 失败 → 降级只返 vector 结果(log warn,user 无感)
func (s *service) retrieveHybrid(
	ctx context.Context,
	orgID uint64,
	query string,
	topK int,
	filter *repository.ChunkSearchFilter,
) ([]repository.ChunkHit, error) {
	candK := topK * hybridCandidateMultiplier
	// tokenizer 可能为 nil(例如配置降级),此时直接走 vector,别强行 hybrid。
	if s.tokenizer == nil {
		return s.retrieveVector(ctx, orgID, query, topK, filter)
	}
	tokens := s.tokenizer.TokensString(query)

	// 并发两路。vec 走 embed + pg ANN,bm25 走 pg tsvector;互不依赖。
	type result struct {
		hits []repository.ChunkHit
		err  error
	}
	vecCh := make(chan result, 1)
	bmCh := make(chan result, 1)

	go func() {
		vec, err := s.embedQuery(ctx, query, orgID)
		if err != nil {
			vecCh <- result{nil, err}
			return
		}
		hits, err := s.chunkRepo.SearchByVector(ctx, orgID, vec, candK, filter)
		vecCh <- result{hits, err}
	}()
	go func() {
		if tokens == "" {
			bmCh <- result{nil, nil}
			return
		}
		hits, err := s.chunkRepo.SearchByBM25(ctx, orgID, tokens, candK, filter)
		bmCh <- result{hits, err}
	}()

	vRes := <-vecCh
	bRes := <-bmCh

	if vRes.err != nil {
		// vector 通路是"主干"—— 失败不降级。
		s.logger.ErrorCtx(ctx, "chunk search: hybrid vector leg failed", vRes.err, map[string]any{"org_id": orgID})
		return nil, vRes.err
	}
	vectorHits := vRes.hits
	var bm25Hits []repository.ChunkHit
	if bRes.err != nil {
		// BM25 失败不 fatal,warn + 降级(用户只拿到 vector 结果,搜索仍可用)。
		s.logger.WarnCtx(ctx, "chunk search: hybrid bm25 leg failed, degrading to vector", map[string]any{
			"org_id": orgID, "err": bRes.err.Error(),
		})
	} else {
		bm25Hits = bRes.hits
	}

	if len(bm25Hits) == 0 {
		// BM25 空(tokens 空 / 没命中 / 失败):直接返 vector 的 topK。避免 RRF 额外开销。
		if len(vectorHits) > topK {
			return vectorHits[:topK], nil
		}
		return vectorHits, nil
	}

	return fuseRRF(vectorHits, bm25Hits, topK), nil
}

// fuseRRF 经典 Reciprocal Rank Fusion。两路命中按 rank 加权求和,同一 chunk(按 id)被两路都命中时
// 贡献相加 → 天然排前。返回按融合分降序的 topK 条。
//
// 排位并列处理:分数相同时保留最先遇到的(vector 先入 map,BM25 后累加)—— 稳定排序不影响正确性。
func fuseRRF(vectorHits, bm25Hits []repository.ChunkHit, topK int) []repository.ChunkHit {
	type scored struct {
		hit   repository.ChunkHit
		score float64
	}
	byID := make(map[uint64]*scored, len(vectorHits)+len(bm25Hits))

	for i, h := range vectorHits {
		if h.Chunk == nil {
			continue
		}
		id := h.Chunk.ID
		s := 1.0 / (rrfK + float64(i+1))
		if existing, ok := byID[id]; ok {
			existing.score += s
		} else {
			byID[id] = &scored{hit: h, score: s}
		}
	}
	for i, h := range bm25Hits {
		if h.Chunk == nil {
			continue
		}
		id := h.Chunk.ID
		s := 1.0 / (rrfK + float64(i+1))
		if existing, ok := byID[id]; ok {
			existing.score += s
		} else {
			byID[id] = &scored{hit: h, score: s}
		}
	}

	out := make([]scored, 0, len(byID))
	for _, s := range byID {
		out = append(out, *s)
	}
	// 用 sort.Slice 而不是 heap —— 分数条数 ~60(2×topK×multiplier)量级小,排序常数因子小。
	sort.Slice(out, func(i, j int) bool { return out[i].score > out[j].score })
	if len(out) > topK {
		out = out[:topK]
	}
	ret := make([]repository.ChunkHit, len(out))
	for i, s := range out {
		ret[i] = s.hit
	}
	return ret
}

// embedQuery 共享给 vector/hybrid 路径的 query embed helper。失败按"搜索暂不可用"映射到 IndexFailed。
func (s *service) embedQuery(ctx context.Context, query string, orgID uint64) ([]float32, error) {
	embedCtx, cancel := context.WithTimeout(ctx, indexEmbedTimeout)
	defer cancel()
	vecs, err := s.embedder.Embed(embedCtx, []string{query})
	if err != nil {
		s.logger.WarnCtx(ctx, "chunk search: embed failed", map[string]any{"org_id": orgID, "err": err.Error()})
		return nil, fmt.Errorf("embed query: %w: %w", err, document.ErrDocumentIndexFailed)
	}
	if len(vecs) != 1 || len(vecs[0]) == 0 {
		return nil, fmt.Errorf("embed returned unexpected shape: %w", document.ErrDocumentInternal)
	}
	return vecs[0], nil
}

// emptyChunksResponse 统一构造 chunk 搜索空响应。
func emptyChunksResponse(topK int) *dto.ListChunksResponse {
	if topK <= 0 {
		topK = document.DefaultSemanticTopK
	}
	return &dto.ListChunksResponse{
		Items: []dto.ChunkSearchResult{},
		Total: 0,
		TopK:  topK,
	}
}

// distanceToSimilarity pgvector cosine distance(0=重合 / 1=正交 / 2=反向)→ [0,1] 相似度(1=最相关)。
// clamp 兜底极端值(理论上 d ∈ [0,2],防御性处理)。
func distanceToSimilarity(d float32) float32 {
	s := 1.0 - d/2.0
	switch {
	case s < 0:
		return 0
	case s > 1:
		return 1
	default:
		return s
	}
}

// truncateSnippet 按 rune 数截断,避免把多字节字符切断。
func truncateSnippet(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

// emptyListResponse 统一构造语义搜索的空响应。Size 给 topK 让前端知道"查的是多少上限",Total/items 均 0。
func emptyListResponse(topK int) *dto.ListDocumentsResponse {
	if topK <= 0 {
		topK = document.DefaultSemanticTopK
	}
	return &dto.ListDocumentsResponse{
		Items: []dto.DocumentResponse{},
		Total: 0,
		Page:  1,
		Size:  topK,
	}
}
