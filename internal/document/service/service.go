// Package service document 模块业务逻辑层。
//
// 现阶段只有 upload 相关:判重 + 冲突检测 + 调 asyncjob。
// list / get / delete 直接走 repository,不经 service 层(逻辑薄,不引入一层空转)。
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/document"
	docmodel "github.com/eyrihe999-stack/Synapse/internal/document/model"
	docrepo "github.com/eyrihe999-stack/Synapse/internal/document/repository"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/common/idgen"
	"gorm.io/gorm"
)

// UploadOutcome PrepareUpload 的返回。按 Status 字段分支:
//
//	StatusAlreadyIndexed   内容已存在(同 org 同 hash)。DocID 是已有 doc 的 id。
//	StatusFilenameConflict 同 file_name 但不同 hash。调用方应返 409,等前端带 force=true 重试。
//	                       ExistingDocID / ExistingVersion 供前端展示"已有文件信息"。
//	StatusReady            无冲突 / 覆盖已确认。DocID 是本次该用的 id(新或已有)。调用方下一步 Schedule。
type UploadOutcome struct {
	Status           UploadStatus
	DocID            uint64
	ContentHash      string
	ExistingDocID    uint64
	ExistingFileName string
	ExistingVersion  string
}

// UploadStatus PrepareUpload 的三分支状态枚举,对应 already_indexed / filename_conflict / ready。
type UploadStatus string

const (
	StatusAlreadyIndexed   UploadStatus = "already_indexed"
	StatusFilenameConflict UploadStatus = "filename_conflict"
	StatusReady            UploadStatus = "ready"
)

// PrepareUploadInput 上传预检输入。Content 整段 byte 已在 handler 读入。
type PrepareUploadInput struct {
	OrgID      uint64
	UploaderID uint64
	FileName   string
	Content    []byte
	// Overwrite true 表示前端已用户确认"覆盖同名文件"—— 绕过 filename 冲突检查
	Overwrite bool
}

// UploadService 提供 upload 前的预检 + 决定 docID。
//
// 独立出 service 层的理由:handler 层不暴露 DB;replay/test 用 mock service 注入方便。
type UploadService interface {
	PrepareUpload(ctx context.Context, in PrepareUploadInput) (*UploadOutcome, error)
}

// uploadService 默认实现。
type uploadService struct {
	repo docrepo.Repository
	log  logger.LoggerInterface
	db   *gorm.DB // 直连 PG,做 "按 version / file_name 查找" 的短路查询
}

// NewUploadService 构造 upload service 实例。
//
// 参数:
//   - db:  直连 *gorm.DB,用于本层专属的 "按 hash 查重 / 按 file_name 查同名" 轻查询,
//     这些查询不值得在 Repository 接口里开专用方法。
//   - repo: document 数据访问接口,后续实际写入 doc/chunks 走它。
//   - log:  结构化日志器,所有 error 返回前写日志以便生产排查。
//
// 三个依赖都必填;nil 会在首次调用时 panic,由装配层保证不传 nil。
func NewUploadService(db *gorm.DB, repo docrepo.Repository, log logger.LoggerInterface) UploadService {
	return &uploadService{db: db, repo: repo, log: log}
}

// PrepareUpload 上传预检 + 决定 docID。
//
// 逻辑分支:
//  1. 参数校验失败(OrgID=0 / 空内容 / 空文件名)→ 返 ErrDocumentInvalidInput。
//  2. 按 (org_id, sha256) 命中已有 doc → StatusAlreadyIndexed,不重复 embed。
//  3. 按 (org_id, file_name) 命中且未 force → StatusFilenameConflict;force → 复用老 doc_id。
//  4. 全新文件 → 生成 snowflake doc_id 返 StatusReady。
//
// 可能的错误:
//   - ErrDocumentInvalidInput: 参数非法(OrgID=0 / 空内容 / 空文件名)。
//   - ErrDocumentInternal: DB 查询失败 / snowflake 生成失败。
func (s *uploadService) PrepareUpload(ctx context.Context, in PrepareUploadInput) (*UploadOutcome, error) {
	if in.OrgID == 0 {
		s.log.WarnCtx(ctx, "upload: OrgID is zero", nil)
		return nil, fmt.Errorf("upload: OrgID is zero: %w", document.ErrDocumentInvalidInput)
	}
	if len(in.Content) == 0 {
		s.log.WarnCtx(ctx, "upload: empty content", map[string]any{"org_id": in.OrgID, "file_name": in.FileName})
		return nil, fmt.Errorf("upload: empty content: %w", document.ErrDocumentInvalidInput)
	}
	if in.FileName == "" {
		s.log.WarnCtx(ctx, "upload: empty file name", map[string]any{"org_id": in.OrgID})
		return nil, fmt.Errorf("upload: empty file name: %w", document.ErrDocumentInvalidInput)
	}

	sum := sha256.Sum256(in.Content)
	hash := hex.EncodeToString(sum[:])

	// 1. 内容去重:同 org 同 version(hash) 命中 → 已索引,不重复 embed
	existing, err := s.findByVersion(ctx, in.OrgID, hash)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, err
	}
	if existing != nil {
		return &UploadOutcome{
			Status:      StatusAlreadyIndexed,
			DocID:       existing.ID,
			ContentHash: hash,
		}, nil
	}

	// 2. 同名不同内容:若命中且未 force → 409;若 force → 复用老 doc_id 走覆盖(Q 策略)
	sameName, err := s.findByFileName(ctx, in.OrgID, in.FileName)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, err
	}
	if sameName != nil {
		if !in.Overwrite {
			return &UploadOutcome{
				Status:           StatusFilenameConflict,
				ContentHash:      hash,
				ExistingDocID:    sameName.ID,
				ExistingFileName: sameName.FileName,
				ExistingVersion:  sameName.Version,
			}, nil
		}
		// 确认覆盖:使用老 doc_id
		return &UploadOutcome{
			Status:      StatusReady,
			DocID:       sameName.ID,
			ContentHash: hash,
		}, nil
	}

	// 3. 新文件:生成 snowflake 作为 docID
	id, err := idgen.GenerateID()
	if err != nil {
		s.log.ErrorCtx(ctx, "upload: generate doc id failed", err, map[string]any{"org_id": in.OrgID, "file_name": in.FileName})
		return nil, fmt.Errorf("upload: generate doc id: %w: %w", err, document.ErrDocumentInternal)
	}
	return &UploadOutcome{
		Status:      StatusReady,
		DocID:       uint64(id),
		ContentHash: hash,
	}, nil
}

// findByVersion 查 (org_id, version) 命中的首条 doc。nil 表示不存在。
func (s *uploadService) findByVersion(ctx context.Context, orgID uint64, version string) (*docmodel.Document, error) {
	var doc docmodel.Document
	err := s.db.WithContext(ctx).
		Where("org_id = ? AND version = ?", orgID, version).
		Take(&doc).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		s.log.ErrorCtx(ctx, "upload: find by version failed", err, map[string]any{"org_id": orgID})
		return nil, fmt.Errorf("find by version: %w: %w", err, document.ErrDocumentInternal)
	}
	return &doc, nil
}

// findByFileName 查 (org_id, file_name) 命中的首条 doc(同 org 内同名只做一个候选即可)。
func (s *uploadService) findByFileName(ctx context.Context, orgID uint64, fileName string) (*docmodel.Document, error) {
	var doc docmodel.Document
	err := s.db.WithContext(ctx).
		Where("org_id = ? AND file_name = ?", orgID, fileName).
		Take(&doc).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		s.log.ErrorCtx(ctx, "upload: find by file_name failed", err, map[string]any{"org_id": orgID, "file_name": fileName})
		return nil, fmt.Errorf("find by file_name: %w: %w", err, document.ErrDocumentInternal)
	}
	return &doc, nil
}
