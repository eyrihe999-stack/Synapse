// upload_service.go DocumentService 上传路径实现(Upload 三分支 + 覆盖写 + OSS/MySQL/PG 一致性补偿)。
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"gorm.io/datatypes"

	"github.com/eyrihe999-stack/Synapse/internal/document"
	"github.com/eyrihe999-stack/Synapse/internal/document/dto"
	"github.com/eyrihe999-stack/Synapse/internal/document/model"
	"github.com/eyrihe999-stack/Synapse/pkg/chunker"
	"github.com/eyrihe999-stack/Synapse/pkg/utils"
)

// uploadReadLimitMargin io.LimitReader 比 MaxFileSizeBytes 多读 1 字节,用来分辨"刚好满=合法"和"超限"。
const uploadReadLimitMargin = 1

// Upload 按 dedup → explicit-overwrite → new 三分支分派,并保证跨库一致性。
//
// 三分支判定顺序:
//
//  1. **Dedup**:(org_id, content_hash) 已存在 → 返已有 DTO,无副作用(幂等)。
//     字节完全相同就是同一份内容,返回现成 doc,不管 TargetDocID 传了什么。
//  2. **Explicit Overwrite**:in.TargetDocID != 0 → 在该 doc 上原地覆盖内容
//     (OSS 同 key 覆写、PG chunks 原子换、MySQL metadata 更新)。
//     TargetDocID 不存在 / 跨 org → ErrDocumentNotFound,整单失败,无副作用。
//  3. **New**:TargetDocID == 0 且 hash 不命中 → snowflake 分配新 doc_id + saga 写三处。
//
// **重要:不再按 filename 自动 overwrite**。同名文档可多条并存,覆盖哪一条由前端通过
// TargetDocID 显式指定(前端通过 Precheck 的 ExistingList 让用户选)。
//
// **一致性保证:** 返回成功 →
//   - Dedup 分支:旧 doc 完整存在(就是返的那条)。
//   - New 分支:MySQL + OSS + (索引启用则) PG chunks 三者齐备。
//   - Overwrite 分支:MySQL metadata 已翻到新值 AND OSS 已是新内容 AND (索引启用则) PG chunks 已换成新;
//     参阅 overwriteExisting 的失败语义说明。
//
// 返回失败 → 除 Overwrite 中段失败产生的"MySQL 旧/OSS+PG 新"不一致外(可由重试自愈),其余路径"什么也没发生"。
func (s *service) Upload(ctx context.Context, in UploadInput) (*dto.DocumentResponse, error) {
	content, err := s.materializeContent(ctx, in)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, err
	}
	if !hasMeaningfulContent(content) {
		s.logger.WarnCtx(ctx, "document: upload rejected, empty or whitespace-only content",
			map[string]any{"org_id": in.OrgID, "uploader_id": in.UploaderID, "bytes": len(content)})
		return nil, document.ErrDocumentEmpty
	}
	if err := s.validateUpload(ctx, &in, int64(len(content))); err != nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, err
	}

	hashBytes := sha256.Sum256(content)
	contentHash := hex.EncodeToString(hashBytes[:])
	safeFile := sanitizeFileName(in.FileName)

	// 分支 1:按 content_hash 查重。命中 → 幂等返回,0 副作用。
	existingByHash, err := s.docRepo.FindByContentHash(ctx, in.OrgID, contentHash)
	if err != nil {
		s.logger.ErrorCtx(ctx, "document: find-by-hash failed", err, map[string]any{"org_id": in.OrgID})
		return nil, errWrap(document.ErrDocumentInternal, err)
	}
	if existingByHash != nil {
		s.logger.InfoCtx(ctx, "document: upload dedup hit (identical content)", map[string]any{
			"org_id": in.OrgID, "existing_doc_id": existingByHash.ID,
		})
		resp := documentToDTO(existingByHash)
		s.fillUploaderName(ctx, &resp)
		return &resp, nil
	}

	// 分支 1.5:按 (source_type, source_ref) 自动 upsert。
	// pull-based adapter(feishu / git / jira)每次 Sync 同一外部文件多次调 Upload,没 TargetDocID 时
	// 需要自动找到上次建的 doc 走 overwrite,否则会每次都新建。
	// 与 TargetDocID 的互斥规则:显式 TargetDocID 优先,adapter 自动查到的 ref 目标只在 TargetDocID=0 时生效。
	if in.TargetDocID == 0 && in.SourceType != "" && len(in.SourceRef) > 0 {
		existingByRef, err := s.docRepo.FindBySourceRef(ctx, in.OrgID, in.SourceType, in.SourceRef)
		if err != nil {
			s.logger.ErrorCtx(ctx, "document: find-by-source-ref failed", err, map[string]any{
				"org_id": in.OrgID, "source_type": in.SourceType,
			})
			return nil, errWrap(document.ErrDocumentInternal, err)
		}
		if existingByRef != nil {
			// 找到了:把 TargetDocID 设上,下面分支 2 自动走 overwriteExisting。
			// Log 标"source_ref upsert",运维排查时能一眼看出为什么走到 overwrite。
			s.logger.InfoCtx(ctx, "document: upload source_ref upsert → overwrite", map[string]any{
				"org_id": in.OrgID, "existing_doc_id": existingByRef.ID,
				"source_type": in.SourceType,
			})
			in.TargetDocID = existingByRef.ID
		}
	}

	// 分支 2:显式覆盖指定 doc。必须属于本 org,否则 NotFound。
	if in.TargetDocID != 0 {
		//sayso-lint:ignore err-shadow
		target, err := s.docRepo.FindDocumentByID(ctx, in.OrgID, in.TargetDocID)
		if err != nil {
			s.logger.WarnCtx(ctx, "document: upload target doc lookup failed", map[string]any{
				"org_id": in.OrgID, "target_doc_id": in.TargetDocID, "err": err.Error(),
			})
			//sayso-lint:ignore sentinel-wrap
			return nil, err
		}
		s.logger.InfoCtx(ctx, "document: upload explicit overwrite path", map[string]any{
			"org_id": in.OrgID, "target_doc_id": target.ID, "file_name": safeFile,
		})
		//sayso-lint:ignore sentinel-wrap
		return s.overwriteExisting(ctx, target, in, content, contentHash)
	}

	// 分支 3:全新 doc。
	//sayso-lint:ignore sentinel-wrap
	return s.uploadNew(ctx, in, content, contentHash, safeFile)
}

// uploadNew 是 Upload 的"新建 doc"路径(原 Upload 主体,抽成独立方法让 Upload 的三分支清晰)。
// 流程和失败语义见 Upload 文档注释。
func (s *service) uploadNew(ctx context.Context, in UploadInput, content []byte, contentHash, safeFile string) (*dto.DocumentResponse, error) {
	snow, err := utils.GenerateID()
	if err != nil {
		s.logger.ErrorCtx(ctx, "document: snowflake id generation failed", err, nil)
		return nil, errWrap(document.ErrDocumentInternal, err)
	}
	docID := uint64(snow)
	ossKey := s.buildOSSKey(in.OrgID, docID, safeFile)

	// Source 空串走默认 user(通过 HTTP upload 的普通用户永远是 user;
	// generator adopt 路径显式传 DocSourceAIGenerated)。这里显式填值而不依赖
	// schema default,让 Go 侧写入的行在未走 DB 默认值路径时(如 mock、迁移前)
	// 也有明确 source,前端处理一致。
	source := in.Source
	if source == "" {
		source = document.DocSourceUser
	}
	// SourceType 空串默认 markdown_upload(HTTP multipart 主路径)。AI 写回或未来 adapter
	// 接入时应该显式传 source_type,确保分派键在 Adapter Registry 里能找得到。
	sourceType := in.SourceType
	if sourceType == "" {
		if source == document.DocSourceAIGenerated {
			// 旧 Source=ai-generated 自动映射到新 SourceType=ai_note,保持语义连续。
			sourceType = document.SourceTypeAINote
		} else {
			sourceType = document.SourceTypeMarkdownUpload
		}
	}
	var sourceRef datatypes.JSON
	if len(in.SourceRef) > 0 {
		sourceRef = datatypes.JSON(in.SourceRef)
	}
	doc := &model.Document{
		ID:          docID,
		OrgID:       in.OrgID,
		UploaderID:  in.UploaderID,
		Title:       in.Title,
		MIMEType:    in.MIMEType,
		FileName:    safeFile,
		SizeBytes:   int64(len(content)),
		OSSKey:      ossKey,
		ContentHash: contentHash,
		Source:      source,
		SourceType:  sourceType,
		SourceRef:   sourceRef,
	}

	if err := s.docRepo.CreateDocument(ctx, doc); err != nil {
		s.logger.ErrorCtx(ctx, "document: create row failed", err, map[string]any{"org_id": in.OrgID})
		return nil, errWrap(document.ErrDocumentInternal, err)
	}

	//sayso-lint:ignore err-swallow
	if _, err := s.oss.Put(ctx, ossKey, content, in.MIMEType); err != nil {
		s.logger.ErrorCtx(ctx, "document: oss put failed, rolling back", err, map[string]any{
			"org_id": in.OrgID, "doc_id": docID, "oss_key": ossKey,
		})
		s.compensateDeleteDoc(ctx, docID)
		return nil, errWrap(document.ErrDocumentStorageFailed, err)
	}

	if s.indexingEnabled() {
		if err := s.indexContent(ctx, docID, in.OrgID, string(content), in.MIMEType, in.FileName); err != nil {
			s.logger.ErrorCtx(ctx, "document: index failed, rolling back oss+mysql", err, map[string]any{
				"doc_id": docID, "oss_key": ossKey,
			})
			s.compensateDeleteOSS(ctx, ossKey)
			s.compensateDeleteDoc(ctx, docID)
			//sayso-lint:ignore sentinel-wrap
			return nil, err
		}
	}

	resp := documentToDTO(doc)
	s.fillUploaderName(ctx, &resp)
	return &resp, nil
}

// overwriteExisting 覆盖更新路径。
//
// 顺序:
//  1. **锁前** embed(如索引启用)—— 这是慢操作(1-3s 网络),放锁外避免长时间持行锁阻塞并发。
//     致命 embed 错直接返回,不进 OverwriteDocumentContent,OSS/PG 状态不变。
//  2. **锁内**(MySQL tx + SELECT FOR UPDATE)执行 sideEffects:
//     a. OSS PUT 覆写同 key。
//     b. PG SwapChunksByDocID 原子换 chunks(旧删新插在单 PG tx 里)。
//  3. sideEffects 全 OK → UPDATE metadata(content_hash/size_bytes/mime_type/title) → COMMIT。
//
// 覆盖路径不改的字段:doc_id / oss_key / uploader_id / created_at(这些是文档"身份"相关,覆盖内容时不动)。
//
// **已知一致性窗口:** OSS PUT + PG swap 成功但 MySQL UPDATE 前 crash → MySQL 旧 hash/size,OSS/PG 是新内容。
// 自愈:用户重传同内容会再次命中 filename 分支走 OVERWRITE,OSS/PG 幂等(put 同字节、swap 同 chunks),MySQL 这次 UPDATE 成功,最终一致。
func (s *service) overwriteExisting(ctx context.Context, existing *model.Document, in UploadInput, newContent []byte, newContentHash string) (*dto.DocumentResponse, error) {
	// Step 1:锁外 embed(如启用),把慢 IO 尽量在锁外完成。
	// Profile 按新 content 的 mime/filename 选,和新上传路径保持一致的路由规则。
	var profile chunker.Profile
	var pieces []chunker.Piece
	var vecs [][]float32
	var embedErr error
	if s.indexingEnabled() {
		profile = s.chunkers.Pick(in.MIMEType, in.FileName)
		pieces = profile.Chunk(string(newContent))
		if len(pieces) > 0 {
			inputs := make([]string, len(pieces))
			for i, p := range pieces {
				inputs[i] = p.Content
			}
			// 用 IIFE 隔离 defer cancel(),确保 embed 超时 ctx 在进入 tx 前释放。
			func() {
				embedCtx, cancel := context.WithTimeout(ctx, indexEmbedTimeout)
				defer cancel()
				vecs, embedErr = s.embedder.Embed(embedCtx, inputs)
			}()
			if embedErr != nil && isFatalEmbedError(embedErr) {
				s.logger.ErrorCtx(ctx, "document: overwrite embed fatal", embedErr, map[string]any{"doc_id": existing.ID})
				return nil, fmt.Errorf("embed fatal: %w: %w", embedErr, document.ErrDocumentIndexFailed)
			}
		}
	}

	// Step 2+3:锁内做 OSS + PG 副作用,随后 UPDATE metadata 并 COMMIT。
	updated, err := s.docRepo.OverwriteDocumentContent(ctx, existing.OrgID, existing.ID,
		func(current *model.Document) error {
			// 2a:OSS 覆盖写同 key。
			//sayso-lint:ignore err-swallow
			if _, err := s.oss.Put(ctx, current.OSSKey, newContent, in.MIMEType); err != nil {
				return fmt.Errorf("oss put: %w: %w", err, document.ErrDocumentStorageFailed)
			}
			// 2b:PG 原子换 chunks(索引启用时)。
			if s.indexingEnabled() {
				rows := buildChunkRows(current.ID, current.OrgID, profile, pieces, vecs, embedErr, s.embedderModel(), s.tokenizer)
				if err := s.chunkRepo.SwapChunksByDocID(ctx, current.ID, rows); err != nil {
					//sayso-lint:ignore log-coverage
					return fmt.Errorf("swap chunks: %w: %w", err, document.ErrDocumentInternal)
				}
			}
			return nil
		},
		map[string]any{
			"content_hash": newContentHash,
			"size_bytes":   int64(len(newContent)),
			"mime_type":    in.MIMEType,
			"title":        in.Title,
		},
	)
	if err != nil {
		s.logger.ErrorCtx(ctx, "document: overwrite failed", err, map[string]any{
			"doc_id": existing.ID, "oss_key": existing.OSSKey,
		})
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}

	resp := documentToDTO(updated)
	s.fillUploaderName(ctx, &resp)
	return &resp, nil
}

// compensateDeleteDoc 最佳努力删 Upload 刚插的 MySQL 行。失败只 log(留孤儿给后台对账清)。
// 专用跳过 org 校验的 DeleteDocumentByID,因为本方法调用前 Upload 明确知道这个 id 是自己插的。
func (s *service) compensateDeleteDoc(ctx context.Context, docID uint64) {
	//sayso-lint:ignore err-swallow
	if err := s.docRepo.DeleteDocumentByID(ctx, docID); err != nil {
		s.logger.ErrorCtx(ctx, "document: compensate delete mysql row failed (orphan leaked)", err, map[string]any{"doc_id": docID})
	}
}

// compensateDeleteOSS 最佳努力清 OSS 对象。失败只 log(由 OSS 对账任务兜底)。
func (s *service) compensateDeleteOSS(ctx context.Context, ossKey string) {
	//sayso-lint:ignore err-swallow
	if err := s.oss.Delete(ctx, ossKey); err != nil {
		s.logger.WarnCtx(ctx, "document: compensate delete oss object failed (orphan leaked)", map[string]any{
			"oss_key": ossKey, "err": err.Error(),
		})
	}
}

// materializeContent 把 UploadInput 的 Content / ContentReader 归一化成字节切片,同时做大小上限校验。
func (s *service) materializeContent(ctx context.Context, in UploadInput) ([]byte, error) {
	if len(in.Content) > 0 {
		if int64(len(in.Content)) > s.cfg.MaxFileSizeBytes {
			s.logger.WarnCtx(ctx, "document: upload rejected, content too large", map[string]any{"size": len(in.Content), "limit": s.cfg.MaxFileSizeBytes})
			return nil, document.ErrDocumentFileTooLarge
		}
		return in.Content, nil
	}
	if in.ContentReader == nil {
		s.logger.WarnCtx(ctx, "document: upload rejected, no content provided", nil)
		return nil, document.ErrDocumentEmpty
	}
	limit := s.cfg.MaxFileSizeBytes
	buf, err := io.ReadAll(io.LimitReader(in.ContentReader, limit+uploadReadLimitMargin))
	if err != nil {
		s.logger.ErrorCtx(ctx, "document: read upload body failed", err, nil)
		return nil, errWrap(document.ErrDocumentInternal, err)
	}
	if int64(len(buf)) > limit {
		s.logger.WarnCtx(ctx, "document: upload rejected, stream too large", map[string]any{"limit": limit})
		return nil, document.ErrDocumentFileTooLarge
	}
	return buf, nil
}

// validateUpload 归一化并校验 UploadInput 字段。
func (s *service) validateUpload(ctx context.Context, in *UploadInput, size int64) error {
	if in.OrgID == 0 || in.UploaderID == 0 {
		s.logger.WarnCtx(ctx, "document: upload rejected, missing org/uploader", map[string]any{"org_id": in.OrgID, "uploader_id": in.UploaderID})
		return document.ErrDocumentInvalidRequest
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" || len([]rune(in.Title)) > document.MaxTitleLength {
		s.logger.WarnCtx(ctx, "document: upload rejected, invalid title", map[string]any{"title_len": len([]rune(in.Title))})
		return document.ErrDocumentTitleInvalid
	}
	if _, ok := s.cfg.AllowedMIMETypes[in.MIMEType]; !ok {
		s.logger.WarnCtx(ctx, "document: upload rejected, mime unsupported", map[string]any{"mime": in.MIMEType})
		return document.ErrDocumentMIMETypeUnsupported
	}
	if size > s.cfg.MaxFileSizeBytes {
		s.logger.WarnCtx(ctx, "document: upload rejected, size too large", map[string]any{"size": size, "limit": s.cfg.MaxFileSizeBytes})
		return document.ErrDocumentFileTooLarge
	}
	return nil
}

// buildOSSKey 构造对象存储 key:{path_prefix}/{org_id}/{doc_id}/{file_name}。
func (s *service) buildOSSKey(orgID, docID uint64, fileName string) string {
	parts := make([]string, 0, 4)
	prefix := strings.Trim(s.oss.PathPrefix(), "/")
	if prefix != "" {
		parts = append(parts, prefix)
	}
	parts = append(parts,
		fmt.Sprintf("%d", orgID),
		fmt.Sprintf("%d", docID),
		fileName,
	)
	return strings.Join(parts, "/")
}

// hasMeaningfulContent 文件必须至少含一个非空白 rune 才能落盘。
//
// 和 chunker 的"空白返回 0 片"语义对齐 —— 全空白文件落盘后 indexContent 会跳过 chunks 写入,
// 产生 "MySQL+OSS 有但 PG 没" 的不变式违例。此处在 Upload 早期就拒掉,统一按 ErrDocumentEmpty 处理。
//
// 使用 utf8.DecodeRune + unicode.IsSpace:覆盖 ASCII 空白 + CJK 全角空格(U+3000)等 Unicode 空白;
// 找到第一个非空白字符立即返回 true,长文件不会全扫。
func hasMeaningfulContent(b []byte) bool {
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		if !unicode.IsSpace(r) {
			return true
		}
		b = b[size:]
	}
	return false
}

// sanitizeFileName 去掉路径 + 控制字符,确保 OSS key 安全。
func sanitizeFileName(name string) string {
	name = path.Base(name)
	if name == "" || name == "." || name == "/" {
		return "document"
	}
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 {
			continue
		}
		if r == '/' || r == '\\' {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "document"
	}
	if len([]rune(out)) > 200 {
		runes := []rune(out)
		out = string(runes[:200])
	}
	return out
}
