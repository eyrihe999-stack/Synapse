// kb_query_service.go channel 视角下读 KB 内容(列表 / 单文档 / 语义检索)的业务层。
//
// 为什么单独成 service:
//   - MCP / 系统 agent 都需要这套能力,逻辑共享一份
//   - 校验链(channel 成员校验 + 可见集合判定)和数据访问(docrepo)放在 service 比放在
//     internal/mcp 的 adapter 更合适 —— mcp 包不是其他模块的依赖目标
//
// 可见集合定义(和 channel.kb_refs 一致):
//
//	可见 doc = (其 knowledge_source_id ∈ channel kb_refs 的 source 集合)
//	         ∪ (其 doc_id ∈ channel 直接挂的 doc 集合)
//
// 失败统一返 ErrForbidden(不暴露 doc 是否存在,防侧信道)。
package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"

	chanerr "github.com/eyrihe999-stack/Synapse/internal/channel"
	channelrepo "github.com/eyrihe999-stack/Synapse/internal/channel/repository"
	"github.com/eyrihe999-stack/Synapse/internal/common/embedding"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/common/ossupload"
	"github.com/eyrihe999-stack/Synapse/internal/document"
	docmodel "github.com/eyrihe999-stack/Synapse/internal/document/model"
	docrepo "github.com/eyrihe999-stack/Synapse/internal/document/repository"
)

// KBQueryService channel scope 下的 KB 读接口。所有方法都做 channel 成员校验 + 可见集合限制。
//
// 命名约定:`*ByPrincipal` —— 和 KBRefService.ListForPrincipal / MessageService.PostAsPrincipal 对齐,
// 底层校验"caller 是 channel 成员"。
type KBQueryService interface {
	// ListDocumentsByPrincipal 列 channel 经由 source 挂载范围内的可见 KB 文档元数据(分页)。
	// query 为 keyword(LIKE on title/file_name);beforeID 为上一页最末 doc.id(0 = 第一页)。
	ListDocumentsByPrincipal(ctx context.Context, channelID, callerPrincipalID uint64, query string, beforeID uint64, limit int) ([]*docmodel.Document, error)

	// GetDocumentByPrincipal 拉单文档元数据 + 全文内容。
	//
	// FullText 来源策略:
	//   - 文本类型(text/markdown / text/plain / .md/.txt) → 直接从 OSS 拉原文,无损
	//   - 其它类型(pdf/docx/二进制) → 回退到 chunks 按 idx 拼接(已抽取的文本)
	//
	// FullText 字节上限 KBContentMaxBytes(默认 200KB);超过则按 UTF-8 边界截断,Truncated=true。
	GetDocumentByPrincipal(ctx context.Context, channelID, docID, callerPrincipalID uint64) (*KBDocumentContent, error)

	// SearchByPrincipal 在 channel 可见 KB 上做语义检索,返回 top-K 命中(含 doc 元数据)。
	//
	//   - query 空串 → 返 ErrForbidden(避免无意义全表扫描)
	//   - topK ≤ 0 → 走默认 KBSearchDefaultTopK
	//   - 可见集为空(channel 没挂任何 source / doc) → 返空列表
	SearchByPrincipal(ctx context.Context, channelID, callerPrincipalID uint64, query string, topK int) ([]KBSearchHit, error)
}

// KBDocumentContent GetDocumentByPrincipal 的返回。
//
// FullTextSource 字段告诉调用方文本来源("oss" 还是 "chunks_join"),前端 / LLM 可据此判断
// 是否丢失格式(chunks_join 会丢一些 markdown 版面)。
type KBDocumentContent struct {
	Document       *docmodel.Document
	FullText       string
	ChunkCount     int
	FullTextSource string // "oss" | "chunks_join"
	Truncated      bool   // FullText 是否被 KBContentMaxBytes 截断
}

// KBSearchHit 单条检索命中。LLM 拿到的最小信息集 —— 知道命中了哪个文档的哪段、相关性多高。
type KBSearchHit struct {
	DocID       uint64
	DocTitle    string
	DocFileName string
	DocMIMEType string
	ChunkIdx    int
	Content     string
	HeadingPath []string
	Distance    float32 // cosine 距离: 0 = 一致, 2 = 反向
}

const (
	// KBContentMaxBytes get_kb_document 单次返回 FullText 的硬上限。markdown ≥ 200KB 大概率
	// 是堆 changelog / 巨大附录的极端文档,塞给 LLM 既不实用又烧 token。截断 + Truncated=true 提示。
	KBContentMaxBytes = 200 * 1024

	// KBSearchDefaultTopK search_kb 不传 top_k 时的默认值。Azure embedding 单条 query 成本
	// 几乎一致,K 影响的是后续 LLM context 占用 —— 5 是经验值,够覆盖大多数场景。
	KBSearchDefaultTopK = 5
	// KBSearchMaxTopK 上限,防 LLM 写出 top_k=1000 灌爆 context。
	KBSearchMaxTopK = 20
)

type kbQueryService struct {
	channelRepo channelrepo.Repository
	docRepo     docrepo.Repository
	oss         ossupload.Client
	embedder    embedding.Embedder
	log         logger.LoggerInterface
}

// NewKBQueryService 构造。
//
// 任一依赖为 nil 都会让对应方法在调用时返 ErrChannelInternal —— 部分场景(单测 / dev 缺 OSS)
// 允许部分能力降级,不在构造期 fatal。
func NewKBQueryService(
	channelRepo channelrepo.Repository,
	docRepo docrepo.Repository,
	oss ossupload.Client,
	embedder embedding.Embedder,
	log logger.LoggerInterface,
) KBQueryService {
	return &kbQueryService{
		channelRepo: channelRepo,
		docRepo:     docRepo,
		oss:         oss,
		embedder:    embedder,
		log:         log,
	}
}

// ListDocumentsByPrincipal 见接口注释。
func (s *kbQueryService) ListDocumentsByPrincipal(
	ctx context.Context,
	channelID, callerPrincipalID uint64,
	query string,
	beforeID uint64,
	limit int,
) ([]*docmodel.Document, error) {
	if callerPrincipalID == 0 {
		return nil, chanerr.ErrForbidden
	}
	c, err := s.requireChannelMember(ctx, channelID, callerPrincipalID)
	if err != nil {
		return nil, err
	}
	sourceIDs, err := s.channelRepo.ListKBSourceIDsForChannel(ctx, c.ID)
	if err != nil {
		return nil, fmt.Errorf("list kb source ids: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if len(sourceIDs) == 0 {
		return nil, nil
	}
	docs, err := s.docRepo.ListByOrg(ctx, c.OrgID, docrepo.ListOptions{
		Query:              strings.TrimSpace(query),
		Limit:              limit,
		BeforeID:           beforeID,
		KnowledgeSourceIDs: sourceIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("list docs by org: %w: %w", err, chanerr.ErrChannelInternal)
	}
	return docs, nil
}

// GetDocumentByPrincipal 见接口注释。
func (s *kbQueryService) GetDocumentByPrincipal(
	ctx context.Context,
	channelID, docID, callerPrincipalID uint64,
) (*KBDocumentContent, error) {
	if callerPrincipalID == 0 {
		return nil, chanerr.ErrForbidden
	}
	c, err := s.requireChannelMember(ctx, channelID, callerPrincipalID)
	if err != nil {
		return nil, err
	}
	doc, err := s.docRepo.GetByID(ctx, c.OrgID, docID)
	if err != nil {
		if errors.Is(err, document.ErrDocumentNotFound) {
			return nil, chanerr.ErrForbidden
		}
		return nil, fmt.Errorf("get doc: %w: %w", err, chanerr.ErrChannelInternal)
	}
	visible, err := s.isDocVisibleInChannel(ctx, c.ID, doc)
	if err != nil {
		return nil, err
	}
	if !visible {
		return nil, chanerr.ErrForbidden
	}

	// 文本类 → OSS 优先(无损);失败 / 二进制 → chunks 拼接回退
	if isTextLikeDoc(doc) && doc.OSSKey != "" && s.oss != nil {
		raw, ossErr := s.oss.GetObject(ctx, doc.OSSKey)
		if ossErr == nil {
			text := string(raw)
			truncated := false
			if len(text) > KBContentMaxBytes {
				text = truncateUTF8Boundary(text, KBContentMaxBytes)
				truncated = true
			}
			return &KBDocumentContent{
				Document:       doc,
				FullText:       text,
				ChunkCount:     doc.ChunkCount,
				FullTextSource: "oss",
				Truncated:      truncated,
			}, nil
		}
		// OSS 失败不报错,降级到 chunks 拼接;只 warn,LLM 仍能拿到文本
		s.log.WarnCtx(ctx, "kb: oss get fallback to chunks", map[string]any{
			"doc_id":  doc.ID,
			"oss_key": doc.OSSKey,
			"err":     ossErr.Error(),
		})
	}

	chunks, err := s.docRepo.ListChunksByDocOrdered(ctx, c.OrgID, doc.ID)
	if err != nil {
		return nil, fmt.Errorf("list chunks: %w: %w", err, chanerr.ErrChannelInternal)
	}
	text := joinChunkContent(chunks)
	truncated := false
	if len(text) > KBContentMaxBytes {
		text = truncateUTF8Boundary(text, KBContentMaxBytes)
		truncated = true
	}
	return &KBDocumentContent{
		Document:       doc,
		FullText:       text,
		ChunkCount:     len(chunks),
		FullTextSource: "chunks_join",
		Truncated:      truncated,
	}, nil
}

// SearchByPrincipal 见接口注释。
func (s *kbQueryService) SearchByPrincipal(
	ctx context.Context,
	channelID, callerPrincipalID uint64,
	query string,
	topK int,
) ([]KBSearchHit, error) {
	if callerPrincipalID == 0 {
		return nil, chanerr.ErrForbidden
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, fmt.Errorf("kb search: empty query: %w", chanerr.ErrForbidden)
	}
	if topK <= 0 {
		topK = KBSearchDefaultTopK
	}
	if topK > KBSearchMaxTopK {
		topK = KBSearchMaxTopK
	}
	if s.embedder == nil {
		return nil, fmt.Errorf("kb search: embedder unavailable: %w", chanerr.ErrChannelInternal)
	}

	c, err := s.requireChannelMember(ctx, channelID, callerPrincipalID)
	if err != nil {
		return nil, err
	}
	sourceIDs, err := s.channelRepo.ListKBSourceIDsForChannel(ctx, c.ID)
	if err != nil {
		return nil, fmt.Errorf("list kb source ids: %w: %w", err, chanerr.ErrChannelInternal)
	}
	docIDs, err := s.channelRepo.ListKBDocumentIDsForChannel(ctx, c.ID)
	if err != nil {
		return nil, fmt.Errorf("list kb doc ids: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if len(sourceIDs) == 0 && len(docIDs) == 0 {
		return nil, nil
	}

	vecs, err := s.embedder.Embed(ctx, []string{q})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if len(vecs) != 1 {
		return nil, fmt.Errorf("embed query: unexpected vec count %d: %w", len(vecs), chanerr.ErrChannelInternal)
	}

	rows, err := s.docRepo.SearchByEmbedding(ctx, c.OrgID, sourceIDs, docIDs, vecs[0], topK)
	if err != nil {
		return nil, fmt.Errorf("search chunks: %w: %w", err, chanerr.ErrChannelInternal)
	}

	hits := make([]KBSearchHit, 0, len(rows))
	for _, r := range rows {
		hits = append(hits, KBSearchHit{
			DocID:       r.DocID,
			DocTitle:    r.DocTitle,
			DocFileName: r.DocFileName,
			DocMIMEType: r.DocMIMEType,
			ChunkIdx:    r.ChunkIdx,
			Content:     r.Content,
			HeadingPath: r.HeadingPath,
			Distance:    r.Distance,
		})
	}
	return hits, nil
}

// ─── helpers ────────────────────────────────────────────────────────────────

// requireChannelMember 校验 channel 存在 + caller 是成员。返 channel 元数据(用 OrgID)。
//
// channel 不存在 / caller 非成员都返 ErrForbidden(不区分,防侧信道枚举)。
func (s *kbQueryService) requireChannelMember(
	ctx context.Context,
	channelID, principalID uint64,
) (*channelrepoChannel, error) {
	c, err := s.channelRepo.FindChannelByID(ctx, channelID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, chanerr.ErrForbidden
	}
	if err != nil {
		return nil, fmt.Errorf("find channel: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if c == nil {
		return nil, chanerr.ErrForbidden
	}
	mem, err := s.channelRepo.FindMember(ctx, c.ID, principalID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, chanerr.ErrForbidden
	}
	if err != nil {
		return nil, fmt.Errorf("find member: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if mem == nil {
		return nil, chanerr.ErrForbidden
	}
	return &channelrepoChannel{ID: c.ID, OrgID: c.OrgID}, nil
}

// channelrepoChannel 仅本文件用,避免 GetDocumentByPrincipal 之类外部消费方依赖完整 channel
// 模型;只关心 ID + OrgID。
type channelrepoChannel struct {
	ID    uint64
	OrgID uint64
}

// isDocVisibleInChannel 检查 doc 是否在 channel 可见集 = source 集 ∪ 直接挂的 doc 集。
func (s *kbQueryService) isDocVisibleInChannel(
	ctx context.Context,
	channelID uint64,
	doc *docmodel.Document,
) (bool, error) {
	if doc.KnowledgeSourceID != 0 {
		sourceIDs, err := s.channelRepo.ListKBSourceIDsForChannel(ctx, channelID)
		if err != nil {
			return false, fmt.Errorf("list kb source ids: %w: %w", err, chanerr.ErrChannelInternal)
		}
		if slices.Contains(sourceIDs, doc.KnowledgeSourceID) {
			return true, nil
		}
	}
	docIDs, err := s.channelRepo.ListKBDocumentIDsForChannel(ctx, channelID)
	if err != nil {
		return false, fmt.Errorf("list kb document ids: %w: %w", err, chanerr.ErrChannelInternal)
	}
	return slices.Contains(docIDs, doc.ID), nil
}

// isTextLikeDoc 判断 doc 是否是"原文 LLM 可直接读"的文本类。
//
// 先看 mime 前缀(text/markdown / text/plain 等),再退化到文件名扩展名兜底
// (老数据可能没填 mime,只有 file_name)。
func isTextLikeDoc(doc *docmodel.Document) bool {
	mime := strings.ToLower(doc.MIMEType)
	if strings.HasPrefix(mime, "text/") || mime == "application/json" {
		return true
	}
	name := strings.ToLower(doc.FileName)
	for _, ext := range []string{".md", ".markdown", ".mdx", ".txt", ".json", ".yaml", ".yml"} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

// joinChunkContent 把 chunks 按 idx ASC 拼成全文。chunks 已由 ListChunksByDocOrdered 排好序。
func joinChunkContent(chunks []docmodel.DocumentChunk) string {
	if len(chunks) == 0 {
		return ""
	}
	parts := make([]string, 0, len(chunks))
	for i := range chunks {
		parts = append(parts, chunks[i].Content)
	}
	return strings.Join(parts, "\n\n")
}

// truncateUTF8Boundary 按 UTF-8 rune 边界裁到不超过 maxBytes。
func truncateUTF8Boundary(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// 退到上一个 rune 起始字节
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}
