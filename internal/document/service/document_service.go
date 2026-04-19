// document_service.go DocumentService 默认实现核心(构造 + 元数据读/改/删 + uploader 名字回填)。
// 上传路径见 upload_service.go;语义检索见 search_service.go;预检见 precheck_service.go;索引流水线见 index_pipeline.go。
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/eyrihe999-stack/Synapse/internal/document"
	"github.com/eyrihe999-stack/Synapse/internal/document/dto"
	"github.com/eyrihe999-stack/Synapse/internal/document/model"
	"github.com/eyrihe999-stack/Synapse/internal/document/repository"
	"github.com/eyrihe999-stack/Synapse/pkg/chunker"
	"github.com/eyrihe999-stack/Synapse/pkg/embedding"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"github.com/eyrihe999-stack/Synapse/pkg/ossclient"
	"github.com/eyrihe999-stack/Synapse/pkg/reranker"
	"github.com/eyrihe999-stack/Synapse/pkg/tokenizer"
)

type service struct {
	cfg       Config
	docRepo   repository.Repository
	chunkRepo repository.ChunkRepository // 允许 nil(降级)
	oss       ossclient.Client
	embedder  embedding.Embedder  // 允许 nil(降级)
	chunkers  *chunker.Registry   // 允许 nil(降级);按 mime/filename 路由到 Profile
	tokenizer tokenizer.Tokenizer // 允许 nil(T1.1 BM25 通路降级到纯 vector)
	reranker  reranker.Reranker   // 允许 nil(T1.2 rerank 可选,不在会让 opts.Rerank 无效降级)
	orgPort   OrgPort
	logger    logger.LoggerInterface
}

// New 构造 DocumentService。
//
// 必填:DocRepo / OSS / OrgPort / Logger。
// 索引三元(ChunkRepo / Embedder / Chunker):全有(启用索引)或全无(降级);任一单独为 nil → 构造失败。
// 构造期校验,调用方通常让进程 fatal,这里不 log,让上层按需决定。
func New(cfg Config, deps Dependencies) (DocumentService, error) {
	if deps.DocRepo == nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, fmtErr("docRepo required")
	}
	if deps.OSS == nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, fmtErr("OSS client required")
	}
	if deps.OrgPort == nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, fmtErr("orgPort required")
	}
	if deps.Logger == nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, fmtErr("logger required")
	}

	indexDepsSet := 0
	if deps.ChunkRepo != nil {
		indexDepsSet++
	}
	if deps.Embedder != nil {
		indexDepsSet++
	}
	if deps.Chunkers != nil {
		indexDepsSet++
	}
	if indexDepsSet != 0 && indexDepsSet != 3 {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf(
			"document service: indexing deps must be all-or-nothing (chunkRepo=%v embedder=%v chunkers=%v): %w",
			deps.ChunkRepo != nil, deps.Embedder != nil, deps.Chunkers != nil,
			document.ErrDocumentInternal,
		)
	}
	if indexDepsSet == 3 {
		deps.Logger.Info("document service: indexing enabled", nil)
	} else {
		deps.Logger.Warn("document service: indexing disabled (chunkRepo/embedder/chunkers not provided)", nil)
	}

	if deps.Tokenizer == nil {
		deps.Logger.Warn("document service: tokenizer not provided, BM25 hybrid retrieval disabled (vector-only)", nil)
	} else {
		deps.Logger.Info("document service: BM25 hybrid retrieval enabled", nil)
	}
	if deps.Reranker == nil {
		deps.Logger.Warn("document service: reranker not provided, opts.Rerank will no-op", nil)
	} else {
		deps.Logger.Info("document service: reranker enabled", map[string]any{"provider": deps.Reranker.Name()})
	}

	return &service{
		cfg:       cfg,
		docRepo:   deps.DocRepo,
		chunkRepo: deps.ChunkRepo,
		oss:       deps.OSS,
		embedder:  deps.Embedder,
		chunkers:  deps.Chunkers,
		tokenizer: deps.Tokenizer,
		reranker:  deps.Reranker,
		orgPort:   deps.OrgPort,
		logger:    deps.Logger,
	}, nil
}

// ─── Get / List / Download ──────────────────────────────────────────────────

// Get 按 (orgID, docID) 取单条文档元信息。单行读,无一致性问题。
//
// 失败场景:
//   - (orgID, docID) 在 MySQL 未命中 → ErrDocumentNotFound。
//   - DB 查询故障 → repo wrap 的原始 error(service 直接透传,不二次 wrap)。
func (s *service) Get(ctx context.Context, orgID, docID uint64) (*dto.DocumentResponse, error) {
	doc, err := s.docRepo.FindDocumentByID(ctx, orgID, docID)
	if err != nil {
		s.logger.WarnCtx(ctx, "document: get failed", map[string]any{"org_id": orgID, "doc_id": docID, "err": err.Error()})
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	r := documentToDTO(doc)
	s.fillUploaderName(ctx, &r)
	return &r, nil
}

// List 按 org 分页列出文档。Count + Find 的事务快照一致性由 repository 层保证,service 只做参数归一化 + DTO 转换。
//
// 失败场景:DB 查询故障 → ErrDocumentInternal(wrap 原 err 便于排查)。
func (s *service) List(ctx context.Context, orgID uint64, query string, page, size int) (*dto.ListDocumentsResponse, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > document.MaxPageSize {
		size = document.DefaultPageSize
	}
	query = strings.TrimSpace(query)
	if n := len([]rune(query)); n > document.MaxQueryLength {
		r := []rune(query)
		query = string(r[:document.MaxQueryLength])
	}
	docs, total, err := s.docRepo.ListDocumentsByOrg(ctx, orgID, query, page, size)
	if err != nil {
		s.logger.ErrorCtx(ctx, "document: list failed", err, map[string]any{"org_id": orgID})
		return nil, errWrap(document.ErrDocumentInternal, err)
	}
	items := make([]dto.DocumentResponse, 0, len(docs))
	for _, d := range docs {
		items = append(items, documentToDTO(d))
	}
	s.fillUploaderNames(ctx, items)
	return &dto.ListDocumentsResponse{
		Items: items, Total: total, Page: page, Size: size,
	}, nil
}

// Download 从 OSS 拉原始文件字节。DB 读 → OSS 读,中间若被并发 Delete,OSS Get 返回 404,wrap 成 StorageFailed;
// 用户看到一个明确的错误,没有脏读。
func (s *service) Download(ctx context.Context, orgID, docID uint64) (*DownloadResult, error) {
	doc, err := s.docRepo.FindDocumentByID(ctx, orgID, docID)
	if err != nil {
		s.logger.WarnCtx(ctx, "document: download target not found", map[string]any{"org_id": orgID, "doc_id": docID, "err": err.Error()})
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	body, err := s.oss.Get(ctx, doc.OSSKey)
	if err != nil {
		s.logger.ErrorCtx(ctx, "document: oss get failed", err, map[string]any{
			"doc_id": docID, "oss_key": doc.OSSKey,
		})
		return nil, errWrap(document.ErrDocumentStorageFailed, err)
	}
	return &DownloadResult{
		FileName:  doc.FileName,
		MIMEType:  doc.MIMEType,
		SizeBytes: doc.SizeBytes,
		Body:      body,
	}, nil
}

// ─── Delete ─────────────────────────────────────────────────────────────────

// Delete 跨库删除顺序:MySQL(权威)→ PG chunks → OSS。
//
// MySQL commit 为"这个 doc 是否存在"的权威信号。之后 PG + OSS 的清理失败只记 log,
// 产生的孤儿 chunks / 孤儿 OSS 对象由后台对账清。用户侧永远看不到半成品。
//
// 失败场景:
//   - 文档不存在 → ErrDocumentNotFound。
//   - MySQL DELETE 失败 → ErrDocumentInternal(此时 PG / OSS 未触及,状态和 pre-delete 一致)。
//   - PG / OSS 清理失败 → warn 日志,不影响返回。
func (s *service) Delete(ctx context.Context, orgID, docID uint64) error {
	doc, err := s.docRepo.DeleteDocumentAtomic(ctx, orgID, docID)
	if err != nil {
		if errors.Is(err, document.ErrDocumentNotFound) {
			s.logger.WarnCtx(ctx, "document: delete target not found", map[string]any{"org_id": orgID, "doc_id": docID})
			//sayso-lint:ignore sentinel-wrap
			return err
		}
		s.logger.ErrorCtx(ctx, "document: delete row failed", err, map[string]any{"doc_id": docID})
		return errWrap(document.ErrDocumentInternal, err)
	}

	// MySQL commit 之后,从用户视角这个 doc 已不存在。以下清理失败只 log,由后台对账兜底。
	if s.chunkRepo != nil {
		if err := s.chunkRepo.DeleteChunksByDocID(ctx, docID); err != nil {
			s.logger.WarnCtx(ctx, "document: delete chunks failed (orphan chunks leaked)", map[string]any{
				"doc_id": docID, "err": err.Error(),
			})
		}
	}
	if err := s.oss.Delete(ctx, doc.OSSKey); err != nil {
		s.logger.WarnCtx(ctx, "document: delete oss object failed (orphan leaked)", map[string]any{
			"doc_id": docID, "oss_key": doc.OSSKey, "err": err.Error(),
		})
	}
	return nil
}

// ─── UpdateMetadata ─────────────────────────────────────────────────────────

// UpdateMetadata 在事务内 SELECT FOR UPDATE → 校验 → UPDATE,消除 lost update。
//
// 校验(如 title 非法)在 tx 内 buildUpdates 回调里做,避免"校验通过后并发修改使当前值不再满足约束"的 TOCTOU。
//
// 失败场景:
//   - 文档不存在 → ErrDocumentNotFound。
//   - title 空或超长 → ErrDocumentTitleInvalid。
//   - DB 写入失败 → ErrDocumentInternal。
func (s *service) UpdateMetadata(ctx context.Context, orgID, docID uint64, req dto.UpdateMetadataRequest) (*dto.DocumentResponse, error) {
	updated, err := s.docRepo.UpdateDocumentFieldsAtomic(ctx, orgID, docID, func(current *model.Document) (map[string]any, error) {
		updates := make(map[string]any)
		if req.Title != nil {
			title := strings.TrimSpace(*req.Title)
			if title == "" || len([]rune(title)) > document.MaxTitleLength {
				//sayso-lint:ignore sentinel-wrap,log-coverage
				return nil, document.ErrDocumentTitleInvalid
			}
			if title != current.Title {
				updates["title"] = title
			}
		}
		return updates, nil
	})
	if err != nil {
		if errors.Is(err, document.ErrDocumentNotFound) || errors.Is(err, document.ErrDocumentTitleInvalid) {
			s.logger.WarnCtx(ctx, "document: update rejected", map[string]any{"doc_id": docID, "err": err.Error()})
			//sayso-lint:ignore sentinel-wrap
			return nil, err
		}
		s.logger.ErrorCtx(ctx, "document: update metadata failed", err, map[string]any{"doc_id": docID})
		return nil, errWrap(document.ErrDocumentInternal, err)
	}
	r := documentToDTO(updated)
	s.fillUploaderName(ctx, &r)
	return &r, nil
}

// ─── uploader 名字回填 ──────────────────────────────────────────────────────

// fillUploaderName 用 orgPort 查 users.display_name,就地回填 DTO。
// 查询失败或用户不存在留空,前端 fallback。
func (s *service) fillUploaderName(ctx context.Context, r *dto.DocumentResponse) {
	if r == nil || r.UploaderID == 0 {
		return
	}
	if name := s.orgPort.GetUserDisplayName(ctx, r.UploaderID); name != "" {
		r.UploaderDisplayName = name
	}
}

// fillUploaderNames 批量回填,缓存同一 uploader 的查询结果,避免 N+1 次 user 表查询。
// 没查到的 uploader_id 在 cache 里留空串,之后遇到同一个 id 直接跳过。
func (s *service) fillUploaderNames(ctx context.Context, items []dto.DocumentResponse) {
	if len(items) == 0 {
		return
	}
	cache := make(map[uint64]string, len(items))
	for i := range items {
		uid := items[i].UploaderID
		if uid == 0 {
			continue
		}
		name, ok := cache[uid]
		if !ok {
			name = s.orgPort.GetUserDisplayName(ctx, uid)
			cache[uid] = name
		}
		if name != "" {
			items[i].UploaderDisplayName = name
		}
	}
}
