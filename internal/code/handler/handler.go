// Package handler code 模块的 HTTP 入口。
//
// 当前只暴露一个端点:列 org 下已同步的代码仓库(聚合概览)。
// 真正的代码检索(给 agent / 用户搜)留到下一阶段;MVP 先补"看得见同步结果"的闭环。
package handler

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/code/model"
	"github.com/eyrihe999-stack/Synapse/internal/code/repository"
	codesvc "github.com/eyrihe999-stack/Synapse/internal/code/service"
	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
)

// OrgResolver slug → orgID 转换。方法签名和 integration handler 的 OrgResolver 完全一致,
// 装配时注入同一个 orgSlugResolver(cmd/synapse/intg_port_adapter.go)即可复用。
type OrgResolver interface {
	GetOrgIDBySlug(ctx context.Context, slug string) (uint64, error)
}

// Handler code 模块 HTTP 入口。
// SearchService 可空 —— 没配置 embedder 时整个 search 路径不可用,/search 端点会返 503。
type Handler struct {
	repo        repository.Repository
	search      codesvc.SearchService // 可能为 nil
	orgResolver OrgResolver
	log         logger.LoggerInterface
}

// Config Handler 构造参数。
type Config struct {
	Repo          repository.Repository
	SearchService codesvc.SearchService // 可空
	OrgResolver   OrgResolver
	Logger        logger.LoggerInterface
}

// New 构造 Handler。
func New(cfg Config) *Handler {
	return &Handler{
		repo:        cfg.Repo,
		search:      cfg.SearchService,
		orgResolver: cfg.OrgResolver,
		log:         cfg.Logger,
	}
}

// repoSummaryResponse 单个 repo 的概览响应 —— 对齐 repository.RepoSummary 但字段名走 JSON 约定。
// 所有时间戳都是 unix seconds(前端用 formatTs),保持和其他端点对称。
type repoSummaryResponse struct {
	ID                uint64 `json:"id"`
	PathWithNamespace string `json:"path_with_namespace"`
	WebURL            string `json:"web_url,omitempty"`
	DefaultBranch     string `json:"default_branch"`
	LastSyncedAt      *int64 `json:"last_synced_at,omitempty"` // nil = 从未同步
	Archived          bool   `json:"archived"`
	CreatedAt         int64  `json:"created_at"`
	FileCount         int64  `json:"file_count"`
	ChunkCount        int64  `json:"chunk_count"`
	FailedChunkCount  int64  `json:"failed_chunk_count"`
}

// ListRepositories GET /api/v2/orgs/:slug/code/repositories
//
// 返 org 下所有已同步(或刚 upsert 还没跑完)的 repo 聚合视图。
// 权限:org member 即可(中间件保证);不要求当前用户 Connect GitLab,同事同步进来的也能看见。
func (h *Handler) ListRepositories(c *gin.Context) {
	_, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	slug := c.Param("slug")
	if slug == "" {
		response.BadRequest(c, "slug required", "")
		return
	}
	orgID, err := h.orgResolver.GetOrgIDBySlug(c.Request.Context(), slug)
	if err != nil {
		// orgResolver 应该返分类错误;MVP 统一 404,不泄漏 org 存在性
		response.NotFound(c, "organization not found", "")
		return
	}

	summaries, err := h.repo.Repos().ListSummariesByOrg(c.Request.Context(), orgID)
	if err != nil {
		h.log.ErrorCtx(c.Request.Context(), "code: list repo summaries failed", err, map[string]any{"org_id": orgID})
		response.InternalServerError(c, "failed to list repositories", err.Error())
		return
	}

	out := make([]repoSummaryResponse, 0, len(summaries))
	for _, s := range summaries {
		row := repoSummaryResponse{
			ID:                s.ID,
			PathWithNamespace: s.PathWithNamespace,
			WebURL:            s.WebURL,
			DefaultBranch:     s.DefaultBranch,
			Archived:          s.Archived,
			CreatedAt:         s.CreatedAt.Unix(),
			FileCount:         s.FileCount,
			ChunkCount:        s.ChunkCount,
			FailedChunkCount:  s.FailedChunkCount,
		}
		if s.LastSyncedAt != nil {
			ts := s.LastSyncedAt.Unix()
			row.LastSyncedAt = &ts
		}
		out = append(out, row)
	}

	c.JSON(http.StatusOK, response.BaseResponse{
		Code: http.StatusOK, Message: "ok",
		Result: gin.H{"repositories": out},
	})
}

// ─── Search ─────────────────────────────────────────────────────────────────

// chunkResponse 单个 chunk 的精简表示(去掉 embedding 等检索内部字段,只留 agent / 前端关心的)。
type chunkResponse struct {
	ID         uint64 `json:"id"`
	ChunkIdx   int    `json:"chunk_idx"`
	SymbolName string `json:"symbol_name,omitempty"`
	Signature  string `json:"signature,omitempty"`
	ChunkKind  string `json:"chunk_kind"`
	Language   string `json:"language"`
	LineStart  int    `json:"line_start"`
	LineEnd    int    `json:"line_end"`
	Content    string `json:"content"`
}

// hitResponse 单条命中的对外 shape。Distance 给前端 debug;agent 通常不关心,直接读 content。
type hitResponse struct {
	Chunk       chunkResponse           `json:"chunk"`
	File        *codesvc.FileRef        `json:"file,omitempty"`
	Repo        *codesvc.RepoRef        `json:"repo,omitempty"`
	Distance    float32                 `json:"distance"`
	MatchSource string                  `json:"match_source"`
	Siblings    []chunkResponse         `json:"siblings,omitempty"`
}

// searchResponse 整个搜索结果。stats 带回让前端 / agent 知道两路召回各贡献了多少。
type searchResponse struct {
	Query string             `json:"query"`
	Hits  []hitResponse      `json:"hits"`
	Stats codesvc.SearchStats `json:"stats"`
}

// Search GET /api/v2/orgs/:slug/code/search
//
// Query 参数:
//   q          (必填) 检索词 / 自然语言问题
//   language   (可选) 多次出现 = 多选,过滤 chunk.language(如 ?language=go&language=python)
//   repo_id    (可选) 多次出现 = 多选,过滤 chunk.repo_id
//   top_k      (可选) 默认 20,最大 50
//   no_siblings (可选,truthy = 不带 siblings) —— agent 链路要节省 token 时用
//
// 权限:org member 即可(中间件保证)。
// 503:search 服务未启用(部署没配 embedder)。
func (h *Handler) Search(c *gin.Context) {
	if _, ok := middleware.GetUserID(c); !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	if h.search == nil {
		// 部署没配 embedder → search 不可用。和飞书 /sync 端点 503 时同样语义。
		response.ServiceUnavailable(c, "code search not available on this deployment", "")
		return
	}
	slug := c.Param("slug")
	if slug == "" {
		response.BadRequest(c, "slug required", "")
		return
	}
	orgID, err := h.orgResolver.GetOrgIDBySlug(c.Request.Context(), slug)
	if err != nil {
		response.NotFound(c, "organization not found", "")
		return
	}

	query := strings.TrimSpace(c.Query("q"))
	if query == "" {
		response.BadRequest(c, "q required", "")
		return
	}

	opts := codesvc.SearchOptions{
		Languages:       c.QueryArray("language"),
		IncludeSiblings: !truthyQuery(c.Query("no_siblings")),
	}
	if raw := c.Query("top_k"); raw != "" {
		if n, perr := strconv.Atoi(raw); perr == nil {
			opts.TopK = n
		}
	}
	for _, raw := range c.QueryArray("repo_id") {
		if id, perr := strconv.ParseUint(raw, 10, 64); perr == nil && id > 0 {
			opts.RepoIDs = append(opts.RepoIDs, id)
		}
	}

	res, err := h.search.SearchCode(c.Request.Context(), orgID, query, opts)
	if err != nil {
		h.log.ErrorCtx(c.Request.Context(), "code search failed", err, map[string]any{
			"org_id": orgID, "query": query,
		})
		response.InternalServerError(c, "search failed", err.Error())
		return
	}

	out := searchResponse{
		Query: res.Query,
		Hits:  make([]hitResponse, 0, len(res.Hits)),
		Stats: res.Stats,
	}
	for _, hit := range res.Hits {
		row := hitResponse{
			Chunk:       toChunkResponse(hit.Chunk),
			File:        hit.File,
			Repo:        hit.Repo,
			Distance:    hit.Distance,
			MatchSource: hit.MatchSource,
		}
		if len(hit.Siblings) > 0 {
			row.Siblings = make([]chunkResponse, len(hit.Siblings))
			for i, sib := range hit.Siblings {
				row.Siblings[i] = toChunkResponse(sib)
			}
		}
		out.Hits = append(out.Hits, row)
	}

	c.JSON(http.StatusOK, response.BaseResponse{
		Code: http.StatusOK, Message: "ok",
		Result: out,
	})
}

// toChunkResponse 把 model.CodeChunk 投影成对外的精简 shape。
// 不返 embedding(几千字节没意义)、index_status / index_error(只有 indexed 会被检索到)、
// embedding_model / chunker_version(诊断字段,不暴露)。
func toChunkResponse(c *model.CodeChunk) chunkResponse {
	if c == nil {
		return chunkResponse{}
	}
	return chunkResponse{
		ID:         c.ID,
		ChunkIdx:   c.ChunkIdx,
		SymbolName: c.SymbolName,
		Signature:  c.Signature,
		ChunkKind:  c.ChunkKind,
		Language:   c.Language,
		LineStart:  c.LineStart,
		LineEnd:    c.LineEnd,
		Content:    c.Content,
	}
}

// truthyQuery 把 ?flag=1 / true / yes 视为开启。其他(空 / 0 / false)视为关闭。
func truthyQuery(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
