// precheck_service.go 上传预检:对一批候选文件预演 Upload 的三分支判断,不做任何写。
package service

import (
	"context"
	"fmt"
	"sort"

	"github.com/eyrihe999-stack/Synapse/internal/document"
	"github.com/eyrihe999-stack/Synapse/internal/document/dto"
)

// PrecheckUpload 见 DocumentService 接口文档。
//
// 失败场景:
//   - orgID == 0 或 candidates 超过 MaxPrecheckBatch → ErrDocumentInvalidRequest(整批拒绝)。
//   - 单个候选的内部问题(hash 格式错等)→ 返回该行 Action=reject + ReasonCode,**不影响整批**。
//   - DB 查询失败 → ErrDocumentInternal,整批放弃(DB 异常时预检结果不可信,重试即可)。
func (s *service) PrecheckUpload(ctx context.Context, orgID uint64, candidates []PrecheckCandidate) ([]PrecheckResult, error) {
	if orgID == 0 {
		s.logger.WarnCtx(ctx, "document: precheck rejected, missing orgID", nil)
		return nil, document.ErrDocumentInvalidRequest
	}
	if len(candidates) == 0 {
		return []PrecheckResult{}, nil
	}
	if len(candidates) > document.MaxPrecheckBatch {
		s.logger.WarnCtx(ctx, "document: precheck rejected, batch too large",
			map[string]any{"org_id": orgID, "count": len(candidates), "limit": document.MaxPrecheckBatch})
		return nil, fmt.Errorf("precheck batch too large (%d > %d): %w",
			len(candidates), document.MaxPrecheckBatch, document.ErrDocumentInvalidRequest)
	}

	results := make([]PrecheckResult, len(candidates))
	for i, c := range candidates {
		r, err := s.evaluateCandidate(ctx, orgID, c)
		if err != nil {
			s.logger.ErrorCtx(ctx, "document: precheck query failed", err,
				map[string]any{"org_id": orgID, "file_name": c.FileName})
			return nil, errWrap(document.ErrDocumentInternal, err)
		}
		results[i] = r
	}
	return results, nil
}

// evaluateCandidate 对单个候选做"能否上传 / 将走哪条 Upload 分支"的判断。
//
// 判断顺序:
//  1. 本地校验:size / mime / hash 合法性 → Reject
//  2. (org_id, content_hash) 查重 → Duplicate(不管文件名是什么,返 Existing 单条)
//  3. (org_id, sanitized file_name) 列出所有同名 → Overwrite(返 ExistingList 候选列表,让用户选)
//  4. 都不命中 → Create
//
// 注意:action=overwrite 不代表"自动覆盖",而是"存在同名冲突,前端需让用户在候选里挑一条或选新建"。
// 真正的覆盖决策由前端在 Upload 请求里显式传 target_doc_id 表达。
func (s *service) evaluateCandidate(ctx context.Context, orgID uint64, c PrecheckCandidate) (PrecheckResult, error) {
	result := PrecheckResult{FileName: c.FileName}

	// 1a:空文件(Upload 会被 hasMeaningfulContent 拒,这里提前告诉前端)。
	if c.SizeBytes <= 0 {
		result.Action = document.PrecheckActionReject
		result.ReasonCode = document.PrecheckReasonEmptyFile
		return result, nil
	}
	// 1b:超限。
	if c.SizeBytes > s.cfg.MaxFileSizeBytes {
		result.Action = document.PrecheckActionReject
		result.ReasonCode = document.PrecheckReasonFileTooLarge
		return result, nil
	}
	// 1c:MIME 不支持。
	if _, ok := s.cfg.AllowedMIMETypes[c.MIMEType]; !ok {
		result.Action = document.PrecheckActionReject
		result.ReasonCode = document.PrecheckReasonMIMEUnsupported
		return result, nil
	}
	// 1d:hash 格式错 —— DTO 层的 validator 已拦 len=64+hex,但服务层再防一次,防绕过。
	if !isSha256Hex(c.ContentHash) {
		result.Action = document.PrecheckActionReject
		result.ReasonCode = document.PrecheckReasonInvalidContentHash
		return result, nil
	}

	// 2:dedup 查重。
	existingByHash, err := s.docRepo.FindByContentHash(ctx, orgID, c.ContentHash)
	if err != nil {
		//sayso-lint:ignore log-coverage
		return PrecheckResult{}, fmt.Errorf("find by content hash: %w", err)
	}
	if existingByHash != nil {
		r := documentToDTO(existingByHash)
		s.fillUploaderName(ctx, &r)
		result.Action = document.PrecheckActionDuplicate
		result.ReasonCode = document.PrecheckReasonIdenticalContentExists
		result.Existing = &r
		return result, nil
	}

	// 3:overwrite 查同名(全部候选)。key 用 sanitize 后的 file_name,和 Upload/存储里的一致。
	safeFile := sanitizeFileName(c.FileName)
	existingByName, err := s.docRepo.FindAllByFileName(ctx, orgID, safeFile)
	if err != nil {
		//sayso-lint:ignore log-coverage
		return PrecheckResult{}, fmt.Errorf("find all by file name: %w", err)
	}
	if len(existingByName) > 0 {
		list := make([]dto.DocumentResponse, len(existingByName))
		for i, d := range existingByName {
			list[i] = documentToDTO(d)
			s.fillUploaderName(ctx, &list[i])
		}
		result.Action = document.PrecheckActionOverwrite
		result.ReasonCode = document.PrecheckReasonSameFilenameNewContent
		result.ExistingList = list
		return result, nil
	}

	// 4:新增。
	result.Action = document.PrecheckActionCreate
	result.ReasonCode = document.PrecheckReasonNewFile
	return result, nil
}

// isSha256Hex 校验 64 字符十六进制串。
// DTO 层的 binding:"hexadecimal,len=64" 已在 handler 拦了,但调用方可能绕过 handler(tool / internal caller),
// 这里再兜一层。
func isSha256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

// GetUploadConfig 返回上传约束 + 语义搜索能力快照。
// AllowedMIMETypes 在 service.Config 里是 map[string]struct{},这里转 sorted slice 让前端渲染稳定。
func (s *service) GetUploadConfig() UploadConfig {
	mimes := make([]string, 0, len(s.cfg.AllowedMIMETypes))
	for k := range s.cfg.AllowedMIMETypes {
		mimes = append(mimes, k)
	}
	sort.Strings(mimes)
	return UploadConfig{
		MaxFileSizeBytes:      s.cfg.MaxFileSizeBytes,
		AllowedMIMETypes:      mimes,
		SemanticSearchEnabled: s.indexingEnabled(),
	}
}
