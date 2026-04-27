// document_service.go channel 共享文档(PR #9')的业务层。
//
// 协作模型:
//   - channel 成员都能读 / 创建 / 抢锁 / 编辑
//   - 删除:创建者本人 或 channel owner
//   - 强制解锁:channel owner 任何时候 / 普通成员仅在锁过期后
//   - channel archived 后所有写路径返 ErrChannelArchived;读仍可
//
// 锁语义:独占 + TTL(ChannelDocumentLockTTL) + 心跳续约。客户端断网/关页面
// 后最迟 10 分钟锁过期,任意成员可再抢。锁不绑设备,同一 user 跨终端冲突时
// 后到的设备需 force 后再抢(MVP 不优化此场景)。
//
// 版本幂等:相同 sha256 在同一文档上 save 多次,只插一行 version,document 的
// current_* 不变;返回已有的 version 行(InlineSummary 不更新),省 OSS PutObject。
package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	chanerr "github.com/eyrihe999-stack/Synapse/internal/channel"
	"github.com/eyrihe999-stack/Synapse/internal/channel/model"
	"github.com/eyrihe999-stack/Synapse/internal/channel/repository"
	"github.com/eyrihe999-stack/Synapse/internal/channel/uploadtoken"
	"github.com/eyrihe999-stack/Synapse/internal/common/eventbus"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/common/ossupload"
)

// PresignTTL OSS 直传 URL + commit token 的有效期。
//
// 5min 平衡了"大文件传输需要时间"和"窗口越短越安全"。1MB 文件即使 100KB/s 也只需 10s,
// 5min 给客户端充足缓冲(包括 LLM 在 PUT 和 commit 之间思考的时间)。
const PresignTTL = 5 * time.Minute

// uploadKeyRandLen OSS 直传时的随机部分(hex 字符数)。32 hex chars = 16 bytes random,
// 跟 UUID v4 量级,256-bit 空间冲突概率忽略。
const uploadKeyRandLen = 32

// DocumentWithLock 列表视图返回项:文档元数据 + 当前锁(可空)。
//
// 让前端列表能直接展示"X 编辑中"徽章,不必逐文档再发 N 次 GET。
type DocumentWithLock struct {
	Document model.ChannelDocument
	Lock     *model.ChannelDocumentLock
}

// DocumentService channel 共享文档对外接口。
//
// 双入口:
//   - by-user(HTTP 路径):反查 users.id → principal,适配 web token
//   - by-principal(MCP 路径):agent 直接以自己的 principal 操作,不反查 user
//
// MCP 第一版只暴露 6 个核心动作(Create / List / Get / GetContent / AcquireLock /
// SaveVersion / ReleaseLock);Heartbeat / ForceRelease / GetVersionContent / ListVersions
// / SoftDelete 仍只在 Web 端做 —— agent 不需要这些精细治理动作。
type DocumentService interface {
	Create(ctx context.Context, channelID, actorUserID uint64, title, contentKind string) (*model.ChannelDocument, error)
	List(ctx context.Context, channelID, callerUserID uint64) ([]DocumentWithLock, error)
	Get(ctx context.Context, channelID, docID, callerUserID uint64) (*DocumentDetail, error)
	GetContent(ctx context.Context, channelID, docID, callerUserID uint64) (*DocumentContent, error)
	GetVersionContent(ctx context.Context, channelID, docID, versionID, callerUserID uint64) (*DocumentContent, error)
	ListVersions(ctx context.Context, channelID, docID, callerUserID uint64) ([]model.ChannelDocumentVersion, error)
	SoftDelete(ctx context.Context, channelID, docID, actorUserID uint64) error

	AcquireLock(ctx context.Context, channelID, docID, actorUserID uint64) (*LockState, error)
	HeartbeatLock(ctx context.Context, channelID, docID, actorUserID uint64) (*LockState, error)
	ReleaseLock(ctx context.Context, channelID, docID, actorUserID uint64) error
	ForceReleaseLock(ctx context.Context, channelID, docID, actorUserID uint64) error

	SaveVersion(ctx context.Context, in SaveVersionInput) (*SaveVersionOutput, error)

	// ── by-principal(MCP 路径)─── 入参直接用 caller 的 principal_id,跳过
	// user_id → principal_id 反查;principal 必须是 channel 成员。
	CreateByPrincipal(ctx context.Context, channelID, actorPrincipalID uint64, title, contentKind string) (*model.ChannelDocument, error)
	ListByPrincipal(ctx context.Context, channelID, callerPrincipalID uint64) ([]DocumentWithLock, error)
	GetByPrincipal(ctx context.Context, channelID, docID, callerPrincipalID uint64) (*DocumentDetail, error)
	GetContentByPrincipal(ctx context.Context, channelID, docID, callerPrincipalID uint64) (*DocumentContent, error)
	AcquireLockByPrincipal(ctx context.Context, channelID, docID, actorPrincipalID uint64) (*LockState, error)
	ReleaseLockByPrincipal(ctx context.Context, channelID, docID, actorPrincipalID uint64) error
	SaveVersionByPrincipal(ctx context.Context, in SaveVersionByPrincipalInput) (*SaveVersionOutput, error)

	// ── OSS 直传 ── PresignUpload 生成预签名 PUT URL + commit_token;客户端 PUT
	// 完后调 CommitUpload 通知服务端 HEAD 拿 size + StreamGet 算 sha256 + 写 version 行。
	// 字节不经服务端,适合大文件 / Claude Code + Bash curl 等场景。
	//
	// PresignUpload 不要求持锁;CommitUpload 必持锁(同 SaveVersion)。
	//
	// baseVersion(可空)乐观锁:RMW 模式 client 应传 download 时拿到的 version,
	// commit 时校验 doc.current_version 是否仍 == base — 不等返
	// ErrChannelDocumentBaseVersionStale,client 应 re-download 重做。
	// 空 → 跳过校验(向后兼容 + "盲写"场景)。
	PresignUploadByPrincipal(ctx context.Context, channelID, docID, actorPrincipalID uint64, baseVersion string) (*PresignedUpload, error)
	CommitUploadByPrincipal(ctx context.Context, in CommitUploadByPrincipalInput) (*SaveVersionOutput, error)
	PresignUpload(ctx context.Context, channelID, docID, actorUserID uint64, baseVersion string) (*PresignedUpload, error)
	CommitUpload(ctx context.Context, in CommitUploadInput) (*SaveVersionOutput, error)

	// ── OSS 直拉 ── PresignDownload 生成预签名 GET URL,客户端 curl 直接下载到本地,
	// 字节不经服务端。配合 OSS 直传形成"读改写"零字节进 LLM 的完整闭环
	// (Claude Code + Bash + Edit tool):download → 改本地 → upload → commit。
	//
	// 不要求持锁(读路径);archived channel 仍可读。
	// 文档当前无版本(空文档)→ 返 ErrChannelDocumentContentEmpty。
	PresignDownloadByPrincipal(ctx context.Context, channelID, docID, callerPrincipalID uint64) (*PresignedDownload, error)
	PresignDownload(ctx context.Context, channelID, docID, callerUserID uint64) (*PresignedDownload, error)
}

// DocumentDetail Get 返回的视图:文档元数据 + 当前锁状态。
//
// Lock 为 nil 表示无锁;非 nil 时调用方按 ExpiresAt 判断是否过期。
type DocumentDetail struct {
	Document model.ChannelDocument
	Lock     *model.ChannelDocumentLock
}

// DocumentContent 读最新版 / 历史版的字节 + 元数据。
type DocumentContent struct {
	Document model.ChannelDocument
	Version  model.ChannelDocumentVersion
	Content  []byte
}

// LockState 抢/续锁返回:谁持锁 + 何时过期 + 是否本次抢到。
//
// Acquired=false 时 HeldByPrincipalID 是当前持锁人(不一定 = caller),
// 调用方据此渲染"被 X 锁住,X 分钟后过期"。
type LockState struct {
	HeldByPrincipalID uint64
	LockedAt          time.Time
	ExpiresAt         time.Time
	Acquired          bool
}

// SaveVersionInput 保存新版本入参(by-user 入口)。
//
// EditSummary 可空。Content 必填且 ≤ ChannelDocumentMaxByteSize。
type SaveVersionInput struct {
	ChannelID    uint64
	DocumentID   uint64
	ActorUserID  uint64
	Content      []byte
	EditSummary  string
}

// SaveVersionByPrincipalInput SaveVersion 的 by-principal 变体(MCP 入口)。
//
// 与 SaveVersionInput 唯一差别:ActorPrincipalID 直接给 caller 的 principal,
// 跳过 user_id → principal 反查。
type SaveVersionByPrincipalInput struct {
	ChannelID        uint64
	DocumentID       uint64
	ActorPrincipalID uint64
	Content          []byte
	EditSummary      string
}

// PresignedUpload PresignUpload 返:供客户端直传 OSS + 后续 commit 用。
//
// 客户端必须 PUT 时带 Content-Type: <ContentType>(签名时绑定),否则 OSS 返
// SignatureDoesNotMatch。
type PresignedUpload struct {
	UploadURL   string
	CommitToken string
	OSSKey      string    // server 内部用,客户端可忽略
	ContentType string    // PUT 时的 Content-Type header
	ExpiresAt   time.Time
	MaxByteSize int64
}

// CommitUploadByPrincipalInput / CommitUploadInput commit 阶段入参。
//
// CommitToken 是 PresignUpload 返的 token;EditSummary 可空。
// server 验 token + 持锁 + HEAD/StreamGet OSS,然后写 version 行。
type CommitUploadByPrincipalInput struct {
	ChannelID        uint64
	DocumentID       uint64
	ActorPrincipalID uint64
	CommitToken      string
	EditSummary      string
}

type CommitUploadInput struct {
	ChannelID    uint64
	DocumentID   uint64
	ActorUserID  uint64
	CommitToken  string
	EditSummary  string
}

// PresignedDownload PresignDownload 返:供客户端直拉 OSS。
//
// Version / ByteSize 是当前 doc.current_version / current_byte_size 的快照,方便客户端
// 提前判断"我要下载的是哪个版本",避免 download 后再 get_channel_document 多走一遍。
type PresignedDownload struct {
	DownloadURL string
	Version     string
	ByteSize    int64
	ContentType string
	ExpiresAt   time.Time
}

// SaveVersionOutput 保存结果。
//
// Created=false 表示同 hash 已存在,本次未实际写新版(返回的 Version 是已有行);
// 这种情况 Document.UpdatedAt 不变,锁也不动。
type SaveVersionOutput struct {
	Document model.ChannelDocument
	Version  model.ChannelDocumentVersion
	Created  bool
}

type documentService struct {
	repo         repository.Repository
	oss          ossupload.Client
	publisher    eventbus.Publisher
	streamKey    string
	ossPrefix    string
	uploadSigner *uploadtoken.Signer // 可 nil:nil 时 Presign/Commit 方法返错
	logger       logger.LoggerInterface
}

func newDocumentService(
	repo repository.Repository,
	oss ossupload.Client,
	publisher eventbus.Publisher,
	streamKey string,
	ossPrefix string,
	uploadSigner *uploadtoken.Signer,
	log logger.LoggerInterface,
) DocumentService {
	return &documentService{
		repo:         repo,
		oss:          oss,
		publisher:    publisher,
		streamKey:    streamKey,
		ossPrefix:    ossPrefix,
		uploadSigner: uploadSigner,
		logger:       log,
	}
}

// ── 写路径 ──────────────────────────────────────────────────────────────────

func (s *documentService) Create(ctx context.Context, channelID, actorUserID uint64, title, contentKind string) (*model.ChannelDocument, error) {
	title = strings.TrimSpace(title)
	if title == "" || len(title) > chanerr.ChannelDocumentTitleMaxLen {
		return nil, chanerr.ErrChannelDocumentTitleInvalid
	}
	if !chanerr.IsValidChannelDocumentKind(contentKind) {
		return nil, chanerr.ErrChannelDocumentKindInvalid
	}

	c, actorPID, err := s.resolveOpenChannelMember(ctx, channelID, actorUserID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	doc := &model.ChannelDocument{
		ChannelID:            c.ID,
		OrgID:                c.OrgID,
		Title:                title,
		ContentKind:          contentKind,
		CurrentOSSKey:        "",
		CurrentVersion:       "",
		CurrentByteSize:      0,
		CreatedByPrincipalID: actorPID,
		UpdatedByPrincipalID: actorPID,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if err := s.repo.CreateChannelDocument(ctx, doc); err != nil {
		return nil, fmt.Errorf("create channel document: %w: %w", err, chanerr.ErrChannelInternal)
	}
	s.publishDocEvent(ctx, "channel_document.created", c, actorPID, doc, map[string]any{
		"document_title": doc.Title,
		"content_kind":   doc.ContentKind,
	})
	return doc, nil
}

func (s *documentService) SoftDelete(ctx context.Context, channelID, docID, actorUserID uint64) error {
	c, actorPID, err := s.resolveOpenChannelMember(ctx, channelID, actorUserID)
	if err != nil {
		return err
	}
	doc, err := s.loadDocInChannel(ctx, c.ID, docID)
	if err != nil {
		return err
	}
	if doc.DeletedAt != nil {
		return nil // 幂等
	}
	if err := s.requireOwnerOrCreator(ctx, c.ID, actorPID, doc.CreatedByPrincipalID); err != nil {
		return err
	}

	// 删前先把锁强制释放,避免持锁人之后调 heartbeat 误以为还能写
	if _, err := s.repo.ForceReleaseChannelDocumentLock(ctx, doc.ID); err != nil {
		s.logger.WarnCtx(ctx, "channel: force release lock on delete failed", map[string]any{
			"document_id": doc.ID, "err": err.Error(),
		})
	}
	if err := s.repo.SoftDeleteChannelDocument(ctx, doc.ID, time.Now().UTC()); err != nil {
		return fmt.Errorf("soft delete document: %w: %w", err, chanerr.ErrChannelInternal)
	}
	s.publishDocEvent(ctx, "channel_document.deleted", c, actorPID, doc, map[string]any{
		"document_title": doc.Title,
	})
	return nil
}

// ── 锁路径 ──────────────────────────────────────────────────────────────────

func (s *documentService) AcquireLock(ctx context.Context, channelID, docID, actorUserID uint64) (*LockState, error) {
	c, actorPID, err := s.resolveOpenChannelMember(ctx, channelID, actorUserID)
	if err != nil {
		return nil, err
	}
	doc, err := s.loadActiveDocInChannel(ctx, c.ID, docID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	// 抢锁前先看现状,用于判断是否要发 channel_document.locked 事件:
	//   prev=nil  → 无锁 → 抢到就发 locked(首次进入编辑)
	//   prev 已过期 → 任何人接力 → 发 locked(holder 实际变了)
	//   prev 同人未过期 → 续约 → **不发**(避免心跳/重复点击刷屏)
	prev, _ := s.repo.FindChannelDocumentLock(ctx, doc.ID)

	heldBy, expiresAt, acquired, err := s.repo.AcquireChannelDocumentLock(ctx, doc.ID, actorPID, chanerr.ChannelDocumentLockTTL, now)
	if err != nil {
		return nil, fmt.Errorf("acquire lock: %w: %w", err, chanerr.ErrChannelInternal)
	}
	state := &LockState{
		HeldByPrincipalID: heldBy,
		LockedAt:          now,
		ExpiresAt:         expiresAt,
		Acquired:          acquired,
	}
	if !acquired {
		// 别人持锁未过期 —— 不报 error,handler 翻成 409 并把状态返客户端
		return state, chanerr.ErrChannelDocumentLockHeld
	}
	holderTransitioned := prev == nil || prev.ExpiresAt.Before(now) || prev.LockedByPrincipalID != actorPID
	if !holderTransitioned {
		// 同人续约 —— 不发事件,直接返
		return state, nil
	}
	s.publishDocEvent(ctx, "channel_document.locked", c, actorPID, doc, map[string]any{
		"document_title": doc.Title,
		"expires_at":     expiresAt.Format(time.RFC3339),
	})
	return state, nil
}

func (s *documentService) HeartbeatLock(ctx context.Context, channelID, docID, actorUserID uint64) (*LockState, error) {
	c, actorPID, err := s.resolveOpenChannelMember(ctx, channelID, actorUserID)
	if err != nil {
		return nil, err
	}
	doc, err := s.loadActiveDocInChannel(ctx, c.ID, docID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	// 心跳本质 = "同人续锁",AcquireChannelDocumentLock 的语义已经覆盖;
	// 但 caller 不应"心跳出"一个新锁(没拿过锁直接心跳应该报错)。先查当前锁。
	current, err := s.repo.FindChannelDocumentLock(ctx, doc.ID)
	if err != nil {
		return nil, fmt.Errorf("find lock: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if current == nil || current.LockedByPrincipalID != actorPID || current.ExpiresAt.Before(now) {
		return nil, chanerr.ErrChannelDocumentLockNotHeld
	}

	heldBy, expiresAt, acquired, err := s.repo.AcquireChannelDocumentLock(ctx, doc.ID, actorPID, chanerr.ChannelDocumentLockTTL, now)
	if err != nil {
		return nil, fmt.Errorf("heartbeat lock: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if !acquired {
		// 极小概率:刚 FindLock 后被别人 force unlock 又抢了 —— 报错
		return &LockState{HeldByPrincipalID: heldBy, ExpiresAt: expiresAt, Acquired: false},
			chanerr.ErrChannelDocumentLockNotHeld
	}
	return &LockState{
		HeldByPrincipalID: actorPID,
		LockedAt:          now,
		ExpiresAt:         expiresAt,
		Acquired:          true,
	}, nil
}

func (s *documentService) ReleaseLock(ctx context.Context, channelID, docID, actorUserID uint64) error {
	c, actorPID, err := s.resolveOpenChannelMember(ctx, channelID, actorUserID)
	if err != nil {
		return err
	}
	doc, err := s.loadActiveDocInChannel(ctx, c.ID, docID)
	if err != nil {
		return err
	}
	released, err := s.repo.ReleaseChannelDocumentLock(ctx, doc.ID, actorPID)
	if err != nil {
		return fmt.Errorf("release lock: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if released {
		s.publishDocEvent(ctx, "channel_document.unlocked", c, actorPID, doc, map[string]any{
			"document_title": doc.Title,
		})
	}
	return nil
}

func (s *documentService) ForceReleaseLock(ctx context.Context, channelID, docID, actorUserID uint64) error {
	c, actorPID, err := s.resolveOpenChannelMember(ctx, channelID, actorUserID)
	if err != nil {
		return err
	}
	doc, err := s.loadActiveDocInChannel(ctx, c.ID, docID)
	if err != nil {
		return err
	}

	current, err := s.repo.FindChannelDocumentLock(ctx, doc.ID)
	if err != nil {
		return fmt.Errorf("find lock: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if current == nil {
		return nil // 无锁,幂等
	}

	now := time.Now().UTC()
	isOwner := s.checkOwner(ctx, c.ID, actorPID)
	expired := current.ExpiresAt.Before(now)
	// channel owner 任何时候可强制;普通成员仅在锁过期后可强制
	if !isOwner && !expired {
		return chanerr.ErrForbidden
	}

	released, err := s.repo.ForceReleaseChannelDocumentLock(ctx, doc.ID)
	if err != nil {
		return fmt.Errorf("force release lock: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if released {
		s.publishDocEvent(ctx, "channel_document.unlocked", c, actorPID, doc, map[string]any{
			"document_title":         doc.Title,
			"forced":                 "true",
			"prior_holder_principal": strconv.FormatUint(current.LockedByPrincipalID, 10),
		})
	}
	return nil
}

// ── 版本路径 ────────────────────────────────────────────────────────────────

func (s *documentService) SaveVersion(ctx context.Context, in SaveVersionInput) (*SaveVersionOutput, error) {
	if len(in.Content) == 0 {
		return nil, chanerr.ErrChannelDocumentContentEmpty
	}
	if int64(len(in.Content)) > chanerr.ChannelDocumentMaxByteSize {
		return nil, chanerr.ErrChannelDocumentContentTooLarge
	}
	if len(in.EditSummary) > chanerr.ChannelDocumentEditSummaryMaxLen {
		in.EditSummary = in.EditSummary[:chanerr.ChannelDocumentEditSummaryMaxLen]
	}
	if s.oss == nil {
		return nil, fmt.Errorf("oss client not configured: %w", chanerr.ErrChannelInternal)
	}

	c, actorPID, err := s.resolveOpenChannelMember(ctx, in.ChannelID, in.ActorUserID)
	if err != nil {
		return nil, err
	}
	doc, err := s.loadActiveDocInChannel(ctx, c.ID, in.DocumentID)
	if err != nil {
		return nil, err
	}

	// 必须持有未过期锁
	now := time.Now().UTC()
	current, err := s.repo.FindChannelDocumentLock(ctx, doc.ID)
	if err != nil {
		return nil, fmt.Errorf("find lock: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if current == nil || current.LockedByPrincipalID != actorPID || current.ExpiresAt.Before(now) {
		return nil, chanerr.ErrChannelDocumentLockNotHeld
	}

	// 算 sha256 → 查同 hash 是否已写过 → 是则幂等返已有
	hash := sha256Hex(in.Content)
	if existing, err := s.repo.FindChannelDocumentVersionByHash(ctx, doc.ID, hash); err == nil && existing != nil {
		return &SaveVersionOutput{Document: *doc, Version: *existing, Created: false}, nil
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("find version by hash: %w: %w", err, chanerr.ErrChannelInternal)
	}

	// 上传 OSS,key 含 hash 天然按内容去重(同 hash 不同 doc 在不同 key 下,各自独立)
	ext := extForKind(doc.ContentKind)
	key := s.buildOSSKey(c.OrgID, doc.ID, hash, ext)
	contentType := contentTypeForKind(doc.ContentKind)
	if _, err := s.oss.PutObject(ctx, key, in.Content, contentType); err != nil {
		return nil, fmt.Errorf("oss put: %w: %w", err, chanerr.ErrChannelInternal)
	}

	version := &model.ChannelDocumentVersion{
		DocumentID:          doc.ID,
		Version:             hash,
		OSSKey:              key,
		ByteSize:            int64(len(in.Content)),
		EditedByPrincipalID: actorPID,
		EditSummary:         in.EditSummary,
		CreatedAt:           now,
	}
	updates := map[string]any{
		"current_oss_key":         key,
		"current_version":         hash,
		"current_byte_size":       int64(len(in.Content)),
		"updated_by_principal_id": actorPID,
		"updated_at":              now,
	}
	err = s.repo.WithTx(ctx, func(tx repository.Repository) error {
		if err := tx.CreateChannelDocumentVersion(ctx, version); err != nil {
			return err
		}
		return tx.UpdateChannelDocumentFields(ctx, doc.ID, updates)
	})
	if err != nil {
		// OSS 已 put,DB 失败 —— 异步孤儿对象,后续 GC 兜底
		s.logger.WarnCtx(ctx, "channel: orphan OSS object on save failure", map[string]any{
			"oss_key": key, "document_id": doc.ID, "err": err.Error(),
		})
		return nil, fmt.Errorf("save version tx: %w: %w", err, chanerr.ErrChannelInternal)
	}

	updated := *doc
	updated.CurrentOSSKey = key
	updated.CurrentVersion = hash
	updated.CurrentByteSize = int64(len(in.Content))
	updated.UpdatedByPrincipalID = actorPID
	updated.UpdatedAt = now

	s.publishDocEvent(ctx, "channel_document.updated", c, actorPID, &updated, map[string]any{
		"document_title": updated.Title,
		"version":        hash,
		"byte_size":      strconv.FormatInt(int64(len(in.Content)), 10),
		"edit_summary":   in.EditSummary,
	})

	return &SaveVersionOutput{Document: updated, Version: *version, Created: true}, nil
}

// ── 读路径 ──────────────────────────────────────────────────────────────────

func (s *documentService) List(ctx context.Context, channelID, callerUserID uint64) ([]DocumentWithLock, error) {
	c, _, err := s.resolveChannelMember(ctx, channelID, callerUserID)
	if err != nil {
		return nil, err
	}
	docs, err := s.repo.ListChannelDocumentsByChannel(ctx, c.ID)
	if err != nil {
		return nil, fmt.Errorf("list documents: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if len(docs) == 0 {
		return nil, nil
	}
	docIDs := make([]uint64, 0, len(docs))
	for i := range docs {
		docIDs = append(docIDs, docs[i].ID)
	}
	locks, err := s.repo.ListChannelDocumentLocksByDocIDs(ctx, docIDs)
	if err != nil {
		// 查锁失败不阻断列表 —— 退化为"全无锁"展示
		s.logger.WarnCtx(ctx, "channel: list document locks failed, returning without locks", map[string]any{
			"channel_id": c.ID, "err": err.Error(),
		})
		locks = nil
	}
	lockByDocID := make(map[uint64]*model.ChannelDocumentLock, len(locks))
	for i := range locks {
		lockByDocID[locks[i].DocumentID] = &locks[i]
	}
	out := make([]DocumentWithLock, 0, len(docs))
	for i := range docs {
		out = append(out, DocumentWithLock{
			Document: docs[i],
			Lock:     lockByDocID[docs[i].ID],
		})
	}
	return out, nil
}

func (s *documentService) Get(ctx context.Context, channelID, docID, callerUserID uint64) (*DocumentDetail, error) {
	c, _, err := s.resolveChannelMember(ctx, channelID, callerUserID)
	if err != nil {
		return nil, err
	}
	doc, err := s.loadDocInChannel(ctx, c.ID, docID)
	if err != nil {
		return nil, err
	}
	lock, err := s.repo.FindChannelDocumentLock(ctx, doc.ID)
	if err != nil {
		return nil, fmt.Errorf("find lock: %w: %w", err, chanerr.ErrChannelInternal)
	}
	return &DocumentDetail{Document: *doc, Lock: lock}, nil
}

func (s *documentService) GetContent(ctx context.Context, channelID, docID, callerUserID uint64) (*DocumentContent, error) {
	c, _, err := s.resolveChannelMember(ctx, channelID, callerUserID)
	if err != nil {
		return nil, err
	}
	doc, err := s.loadDocInChannel(ctx, c.ID, docID)
	if err != nil {
		return nil, err
	}
	if doc.CurrentOSSKey == "" {
		// 空文档:从未保存过版本,返空内容
		return &DocumentContent{Document: *doc, Version: model.ChannelDocumentVersion{}, Content: nil}, nil
	}
	if s.oss == nil {
		return nil, fmt.Errorf("oss client not configured: %w", chanerr.ErrChannelInternal)
	}
	bytes, err := s.oss.GetObject(ctx, doc.CurrentOSSKey)
	if err != nil {
		return nil, fmt.Errorf("oss get: %w: %w", err, chanerr.ErrChannelInternal)
	}
	// 找 current_version 对应的 version 行(要的是元数据如 byte_size / created_at)
	v, _ := s.repo.FindChannelDocumentVersionByHash(ctx, doc.ID, doc.CurrentVersion)
	if v == nil {
		v = &model.ChannelDocumentVersion{
			DocumentID: doc.ID, Version: doc.CurrentVersion, OSSKey: doc.CurrentOSSKey,
			ByteSize: doc.CurrentByteSize,
		}
	}
	return &DocumentContent{Document: *doc, Version: *v, Content: bytes}, nil
}

func (s *documentService) GetVersionContent(ctx context.Context, channelID, docID, versionID, callerUserID uint64) (*DocumentContent, error) {
	c, _, err := s.resolveChannelMember(ctx, channelID, callerUserID)
	if err != nil {
		return nil, err
	}
	doc, err := s.loadDocInChannel(ctx, c.ID, docID)
	if err != nil {
		return nil, err
	}
	v, err := s.repo.FindChannelDocumentVersionByID(ctx, versionID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, chanerr.ErrChannelDocumentVersionNotFound
		}
		return nil, fmt.Errorf("find version: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if v.DocumentID != doc.ID {
		return nil, chanerr.ErrChannelDocumentVersionNotFound
	}
	if s.oss == nil {
		return nil, fmt.Errorf("oss client not configured: %w", chanerr.ErrChannelInternal)
	}
	bytes, err := s.oss.GetObject(ctx, v.OSSKey)
	if err != nil {
		return nil, fmt.Errorf("oss get: %w: %w", err, chanerr.ErrChannelInternal)
	}
	return &DocumentContent{Document: *doc, Version: *v, Content: bytes}, nil
}

func (s *documentService) ListVersions(ctx context.Context, channelID, docID, callerUserID uint64) ([]model.ChannelDocumentVersion, error) {
	c, _, err := s.resolveChannelMember(ctx, channelID, callerUserID)
	if err != nil {
		return nil, err
	}
	doc, err := s.loadDocInChannel(ctx, c.ID, docID)
	if err != nil {
		return nil, err
	}
	return s.repo.ListChannelDocumentVersions(ctx, doc.ID)
}

// ── MCP by-principal 路径(实现) ─────────────────────────────────────────

// CreateByPrincipal MCP 入口:agent 直接以 principal 身份创建共享文档。语义同 Create。
func (s *documentService) CreateByPrincipal(ctx context.Context, channelID, actorPrincipalID uint64, title, contentKind string) (*model.ChannelDocument, error) {
	title = strings.TrimSpace(title)
	if title == "" || len(title) > chanerr.ChannelDocumentTitleMaxLen {
		return nil, chanerr.ErrChannelDocumentTitleInvalid
	}
	if !chanerr.IsValidChannelDocumentKind(contentKind) {
		return nil, chanerr.ErrChannelDocumentKindInvalid
	}
	c, err := s.resolveOpenChannelMemberByPrincipal(ctx, channelID, actorPrincipalID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	doc := &model.ChannelDocument{
		ChannelID:            c.ID,
		OrgID:                c.OrgID,
		Title:                title,
		ContentKind:          contentKind,
		CreatedByPrincipalID: actorPrincipalID,
		UpdatedByPrincipalID: actorPrincipalID,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if err := s.repo.CreateChannelDocument(ctx, doc); err != nil {
		return nil, fmt.Errorf("create channel document: %w: %w", err, chanerr.ErrChannelInternal)
	}
	s.publishDocEvent(ctx, "channel_document.created", c, actorPrincipalID, doc, map[string]any{
		"document_title": doc.Title,
		"content_kind":   doc.ContentKind,
	})
	return doc, nil
}

// ListByPrincipal 列 channel 下未删共享文档(含锁状态)。允许 archived channel 读。
func (s *documentService) ListByPrincipal(ctx context.Context, channelID, callerPrincipalID uint64) ([]DocumentWithLock, error) {
	c, err := s.resolveChannelMemberByPrincipal(ctx, channelID, callerPrincipalID)
	if err != nil {
		return nil, err
	}
	docs, err := s.repo.ListChannelDocumentsByChannel(ctx, c.ID)
	if err != nil {
		return nil, fmt.Errorf("list documents: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if len(docs) == 0 {
		return nil, nil
	}
	docIDs := make([]uint64, 0, len(docs))
	for i := range docs {
		docIDs = append(docIDs, docs[i].ID)
	}
	locks, err := s.repo.ListChannelDocumentLocksByDocIDs(ctx, docIDs)
	if err != nil {
		s.logger.WarnCtx(ctx, "channel: list document locks failed, returning without locks", map[string]any{
			"channel_id": c.ID, "err": err.Error(),
		})
		locks = nil
	}
	lockByDocID := make(map[uint64]*model.ChannelDocumentLock, len(locks))
	for i := range locks {
		lockByDocID[locks[i].DocumentID] = &locks[i]
	}
	out := make([]DocumentWithLock, 0, len(docs))
	for i := range docs {
		out = append(out, DocumentWithLock{
			Document: docs[i],
			Lock:     lockByDocID[docs[i].ID],
		})
	}
	return out, nil
}

// GetByPrincipal 拿单文档元数据 + 当前锁。语义同 Get。
func (s *documentService) GetByPrincipal(ctx context.Context, channelID, docID, callerPrincipalID uint64) (*DocumentDetail, error) {
	c, err := s.resolveChannelMemberByPrincipal(ctx, channelID, callerPrincipalID)
	if err != nil {
		return nil, err
	}
	doc, err := s.loadDocInChannel(ctx, c.ID, docID)
	if err != nil {
		return nil, err
	}
	lock, err := s.repo.FindChannelDocumentLock(ctx, doc.ID)
	if err != nil {
		return nil, fmt.Errorf("find lock: %w: %w", err, chanerr.ErrChannelInternal)
	}
	return &DocumentDetail{Document: *doc, Lock: lock}, nil
}

// GetContentByPrincipal 拿当前版本字节 + 元数据。语义同 GetContent。
func (s *documentService) GetContentByPrincipal(ctx context.Context, channelID, docID, callerPrincipalID uint64) (*DocumentContent, error) {
	c, err := s.resolveChannelMemberByPrincipal(ctx, channelID, callerPrincipalID)
	if err != nil {
		return nil, err
	}
	doc, err := s.loadDocInChannel(ctx, c.ID, docID)
	if err != nil {
		return nil, err
	}
	if doc.CurrentOSSKey == "" {
		return &DocumentContent{Document: *doc, Version: model.ChannelDocumentVersion{}, Content: nil}, nil
	}
	if s.oss == nil {
		return nil, fmt.Errorf("oss client not configured: %w", chanerr.ErrChannelInternal)
	}
	bytes, err := s.oss.GetObject(ctx, doc.CurrentOSSKey)
	if err != nil {
		return nil, fmt.Errorf("oss get: %w: %w", err, chanerr.ErrChannelInternal)
	}
	v, _ := s.repo.FindChannelDocumentVersionByHash(ctx, doc.ID, doc.CurrentVersion)
	if v == nil {
		v = &model.ChannelDocumentVersion{
			DocumentID: doc.ID, Version: doc.CurrentVersion, OSSKey: doc.CurrentOSSKey,
			ByteSize: doc.CurrentByteSize,
		}
	}
	return &DocumentContent{Document: *doc, Version: *v, Content: bytes}, nil
}

// AcquireLockByPrincipal 抢/续锁。语义同 AcquireLock(含事件发布约束)。
func (s *documentService) AcquireLockByPrincipal(ctx context.Context, channelID, docID, actorPrincipalID uint64) (*LockState, error) {
	c, err := s.resolveOpenChannelMemberByPrincipal(ctx, channelID, actorPrincipalID)
	if err != nil {
		return nil, err
	}
	doc, err := s.loadActiveDocInChannel(ctx, c.ID, docID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	prev, _ := s.repo.FindChannelDocumentLock(ctx, doc.ID)
	heldBy, expiresAt, acquired, err := s.repo.AcquireChannelDocumentLock(ctx, doc.ID, actorPrincipalID, chanerr.ChannelDocumentLockTTL, now)
	if err != nil {
		return nil, fmt.Errorf("acquire lock: %w: %w", err, chanerr.ErrChannelInternal)
	}
	state := &LockState{
		HeldByPrincipalID: heldBy,
		LockedAt:          now,
		ExpiresAt:         expiresAt,
		Acquired:          acquired,
	}
	if !acquired {
		return state, chanerr.ErrChannelDocumentLockHeld
	}
	holderTransitioned := prev == nil || prev.ExpiresAt.Before(now) || prev.LockedByPrincipalID != actorPrincipalID
	if !holderTransitioned {
		return state, nil
	}
	s.publishDocEvent(ctx, "channel_document.locked", c, actorPrincipalID, doc, map[string]any{
		"document_title": doc.Title,
		"expires_at":     expiresAt.Format(time.RFC3339),
	})
	return state, nil
}

// ReleaseLockByPrincipal 主动释放(只允许持锁人;否则幂等无副作用)。语义同 ReleaseLock。
func (s *documentService) ReleaseLockByPrincipal(ctx context.Context, channelID, docID, actorPrincipalID uint64) error {
	c, err := s.resolveOpenChannelMemberByPrincipal(ctx, channelID, actorPrincipalID)
	if err != nil {
		return err
	}
	doc, err := s.loadActiveDocInChannel(ctx, c.ID, docID)
	if err != nil {
		return err
	}
	released, err := s.repo.ReleaseChannelDocumentLock(ctx, doc.ID, actorPrincipalID)
	if err != nil {
		return fmt.Errorf("release lock: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if released {
		s.publishDocEvent(ctx, "channel_document.unlocked", c, actorPrincipalID, doc, map[string]any{
			"document_title": doc.Title,
		})
	}
	return nil
}

// SaveVersionByPrincipal 保存新版本(必持锁,同 hash 幂等)。语义同 SaveVersion。
func (s *documentService) SaveVersionByPrincipal(ctx context.Context, in SaveVersionByPrincipalInput) (*SaveVersionOutput, error) {
	if len(in.Content) == 0 {
		return nil, chanerr.ErrChannelDocumentContentEmpty
	}
	if int64(len(in.Content)) > chanerr.ChannelDocumentMaxByteSize {
		return nil, chanerr.ErrChannelDocumentContentTooLarge
	}
	if len(in.EditSummary) > chanerr.ChannelDocumentEditSummaryMaxLen {
		in.EditSummary = in.EditSummary[:chanerr.ChannelDocumentEditSummaryMaxLen]
	}
	if s.oss == nil {
		return nil, fmt.Errorf("oss client not configured: %w", chanerr.ErrChannelInternal)
	}
	c, err := s.resolveOpenChannelMemberByPrincipal(ctx, in.ChannelID, in.ActorPrincipalID)
	if err != nil {
		return nil, err
	}
	doc, err := s.loadActiveDocInChannel(ctx, c.ID, in.DocumentID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	current, err := s.repo.FindChannelDocumentLock(ctx, doc.ID)
	if err != nil {
		return nil, fmt.Errorf("find lock: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if current == nil || current.LockedByPrincipalID != in.ActorPrincipalID || current.ExpiresAt.Before(now) {
		return nil, chanerr.ErrChannelDocumentLockNotHeld
	}
	hash := sha256Hex(in.Content)
	if existing, err := s.repo.FindChannelDocumentVersionByHash(ctx, doc.ID, hash); err == nil && existing != nil {
		return &SaveVersionOutput{Document: *doc, Version: *existing, Created: false}, nil
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("find version by hash: %w: %w", err, chanerr.ErrChannelInternal)
	}
	ext := extForKind(doc.ContentKind)
	key := s.buildOSSKey(c.OrgID, doc.ID, hash, ext)
	contentType := contentTypeForKind(doc.ContentKind)
	if _, err := s.oss.PutObject(ctx, key, in.Content, contentType); err != nil {
		return nil, fmt.Errorf("oss put: %w: %w", err, chanerr.ErrChannelInternal)
	}
	version := &model.ChannelDocumentVersion{
		DocumentID:          doc.ID,
		Version:             hash,
		OSSKey:              key,
		ByteSize:            int64(len(in.Content)),
		EditedByPrincipalID: in.ActorPrincipalID,
		EditSummary:         in.EditSummary,
		CreatedAt:           now,
	}
	updates := map[string]any{
		"current_oss_key":         key,
		"current_version":         hash,
		"current_byte_size":       int64(len(in.Content)),
		"updated_by_principal_id": in.ActorPrincipalID,
		"updated_at":              now,
	}
	err = s.repo.WithTx(ctx, func(tx repository.Repository) error {
		if err := tx.CreateChannelDocumentVersion(ctx, version); err != nil {
			return err
		}
		return tx.UpdateChannelDocumentFields(ctx, doc.ID, updates)
	})
	if err != nil {
		s.logger.WarnCtx(ctx, "channel: orphan OSS object on save failure", map[string]any{
			"oss_key": key, "document_id": doc.ID, "err": err.Error(),
		})
		return nil, fmt.Errorf("save version tx: %w: %w", err, chanerr.ErrChannelInternal)
	}
	updated := *doc
	updated.CurrentOSSKey = key
	updated.CurrentVersion = hash
	updated.CurrentByteSize = int64(len(in.Content))
	updated.UpdatedByPrincipalID = in.ActorPrincipalID
	updated.UpdatedAt = now
	s.publishDocEvent(ctx, "channel_document.updated", c, in.ActorPrincipalID, &updated, map[string]any{
		"document_title": updated.Title,
		"version":        hash,
		"byte_size":      strconv.FormatInt(int64(len(in.Content)), 10),
		"edit_summary":   in.EditSummary,
	})
	return &SaveVersionOutput{Document: updated, Version: *version, Created: true}, nil
}

// ── OSS 直传:Presign + Commit ─────────────────────────────────────────────

// PresignUploadByPrincipal MCP 入口:agent 直接以 principal 抢预签名 URL。
//
// 不要求持锁(预签名只生成 URL,真正写在 commit);commit 必持锁。
//
// baseVersion 用于乐观锁,空 = 跳过校验。RMW 模式必传(用 download 时拿到的 version)。
func (s *documentService) PresignUploadByPrincipal(ctx context.Context, channelID, docID, actorPrincipalID uint64, baseVersion string) (*PresignedUpload, error) {
	if s.uploadSigner == nil || s.oss == nil {
		return nil, fmt.Errorf("upload not configured: %w", chanerr.ErrChannelInternal)
	}
	c, err := s.resolveOpenChannelMemberByPrincipal(ctx, channelID, actorPrincipalID)
	if err != nil {
		return nil, err
	}
	doc, err := s.loadActiveDocInChannel(ctx, c.ID, docID)
	if err != nil {
		return nil, err
	}
	return s.presignUploadCore(ctx, c, doc, baseVersion)
}

// PresignUpload HTTP 入口(by-user)。
func (s *documentService) PresignUpload(ctx context.Context, channelID, docID, actorUserID uint64, baseVersion string) (*PresignedUpload, error) {
	if s.uploadSigner == nil || s.oss == nil {
		return nil, fmt.Errorf("upload not configured: %w", chanerr.ErrChannelInternal)
	}
	c, _, err := s.resolveOpenChannelMember(ctx, channelID, actorUserID)
	if err != nil {
		return nil, err
	}
	doc, err := s.loadActiveDocInChannel(ctx, c.ID, docID)
	if err != nil {
		return nil, err
	}
	return s.presignUploadCore(ctx, c, doc, baseVersion)
}

// presignUploadCore by-user / by-principal 共享:生成 OSS key + 调 OSS SignURL + 签 token。
//
// baseVersion 签进 token,commit 时校验。
func (s *documentService) presignUploadCore(ctx context.Context, c *model.Channel, doc *model.ChannelDocument, baseVersion string) (*PresignedUpload, error) {
	rand16, err := genRandHex(uploadKeyRandLen / 2)
	if err != nil {
		return nil, fmt.Errorf("gen upload random: %w: %w", err, chanerr.ErrChannelInternal)
	}
	ext := extForKind(doc.ContentKind)
	key := s.buildUploadOSSKey(c.OrgID, doc.ID, rand16, ext)
	contentType := contentTypeForKind(doc.ContentKind)

	url, err := s.oss.PresignPutURL(ctx, key, PresignTTL, contentType)
	if err != nil {
		return nil, fmt.Errorf("presign put url: %w: %w", err, chanerr.ErrChannelInternal)
	}
	expiresAt := time.Now().Add(PresignTTL).UTC()
	token, err := s.uploadSigner.Sign(uploadtoken.Payload{
		DocumentID:  doc.ID,
		OSSKey:      key,
		ExpiresAt:   expiresAt.Unix(),
		BaseVersion: baseVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("sign upload token: %w: %w", err, chanerr.ErrChannelInternal)
	}
	return &PresignedUpload{
		UploadURL:   url,
		CommitToken: token,
		OSSKey:      key,
		ContentType: contentType,
		ExpiresAt:   expiresAt,
		MaxByteSize: chanerr.ChannelDocumentMaxByteSize,
	}, nil
}

// CommitUploadByPrincipal MCP 入口:agent 持锁 + commit 之前 PUT 到 OSS 的对象。
//
// 完整校验链:
//  1. 验 token(签名 + 未过期),拿 OSS key
//  2. caller 是 channel 成员 + channel 未归档
//  3. doc 存在(active)+ 属于该 channel
//  4. caller 持锁(同 SaveVersion)
//  5. HEAD OSS 拿 size,> MaxByteSize 拒并删 OSS 临时对象
//  6. StreamGet OSS 算 sha256(读 ≤ MaxByteSize+1 字节后停)
//  7. 检查 sha256 已存在(同 doc):
//     - 是:返已有 version Created=false,**删新上传的 OSS 对象**(避孤儿)
//     - 否:写 versions 行(用上传 OSS key)+ 更新 channel_documents 指针 + 发 channel_document.updated 事件
//
// 失败的 commit 会在 OSS 留对象 — 由后续 lifecycle rule 自动清(uploaded/ 前缀
// 7 天后删)。当前不写 reaper,接受短期孤儿。
func (s *documentService) CommitUploadByPrincipal(ctx context.Context, in CommitUploadByPrincipalInput) (*SaveVersionOutput, error) {
	if s.uploadSigner == nil || s.oss == nil {
		return nil, fmt.Errorf("upload not configured: %w", chanerr.ErrChannelInternal)
	}
	c, err := s.resolveOpenChannelMemberByPrincipal(ctx, in.ChannelID, in.ActorPrincipalID)
	if err != nil {
		return nil, err
	}
	return s.commitUploadCore(ctx, c, in.DocumentID, in.ActorPrincipalID, in.CommitToken, in.EditSummary)
}

// CommitUpload HTTP 入口(by-user)。
func (s *documentService) CommitUpload(ctx context.Context, in CommitUploadInput) (*SaveVersionOutput, error) {
	if s.uploadSigner == nil || s.oss == nil {
		return nil, fmt.Errorf("upload not configured: %w", chanerr.ErrChannelInternal)
	}
	c, actorPID, err := s.resolveOpenChannelMember(ctx, in.ChannelID, in.ActorUserID)
	if err != nil {
		return nil, err
	}
	return s.commitUploadCore(ctx, c, in.DocumentID, actorPID, in.CommitToken, in.EditSummary)
}

// commitUploadCore by-user / by-principal 共享。actorPID 已经从 caller 反查出。
func (s *documentService) commitUploadCore(ctx context.Context, c *model.Channel, docID, actorPID uint64, token, editSummary string) (*SaveVersionOutput, error) {
	// 1. 验 token
	payload, err := s.uploadSigner.Verify(token)
	if err != nil {
		// token 过期 vs 篡改 — service 层都翻译成同一类业务错(handler 层可决定 HTTP code)
		return nil, fmt.Errorf("verify commit token: %w", err)
	}
	if payload.DocumentID != docID {
		// 跨 doc 误用
		return nil, fmt.Errorf("token doc mismatch: %w", uploadtoken.ErrInvalidToken)
	}

	// 2. doc 存在 + 属于本 channel + 未软删
	doc, err := s.loadActiveDocInChannel(ctx, c.ID, docID)
	if err != nil {
		return nil, err
	}

	// 2.5 乐观锁 base_version 校验 — 防 lost update
	// payload.BaseVersion 空 → 跳过(向后兼容 / 盲写场景);非空 → 严格匹配 current_version
	if payload.BaseVersion != "" && payload.BaseVersion != doc.CurrentVersion {
		return nil, chanerr.ErrChannelDocumentBaseVersionStale
	}

	// 3. caller 持锁
	now := time.Now().UTC()
	current, err := s.repo.FindChannelDocumentLock(ctx, doc.ID)
	if err != nil {
		return nil, fmt.Errorf("find lock: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if current == nil || current.LockedByPrincipalID != actorPID || current.ExpiresAt.Before(now) {
		return nil, chanerr.ErrChannelDocumentLockNotHeld
	}

	// 4. HEAD OSS 拿 size
	size, err := s.oss.HeadObject(ctx, payload.OSSKey)
	if err != nil {
		// OSS 对象不存在 / HEAD 失败 — 客户端可能没真正 PUT
		return nil, fmt.Errorf("head oss object %s: %w: %w", payload.OSSKey, err, chanerr.ErrChannelInternal)
	}
	if size <= 0 {
		return nil, chanerr.ErrChannelDocumentContentEmpty
	}
	if size > chanerr.ChannelDocumentMaxByteSize {
		// 超限 — 删 OSS 对象避孤儿
		if delErr := s.oss.DeleteObject(ctx, payload.OSSKey); delErr != nil {
			s.logger.WarnCtx(ctx, "channel: delete oversized upload failed", map[string]any{
				"oss_key": payload.OSSKey, "size": size, "err": delErr.Error(),
			})
		}
		return nil, chanerr.ErrChannelDocumentContentTooLarge
	}

	// 5. StreamGet 算 sha256(MaxByteSize 上限,几百 KB 几十 ms)
	hash, err := s.streamSha256(ctx, payload.OSSKey)
	if err != nil {
		return nil, fmt.Errorf("stream sha256 %s: %w: %w", payload.OSSKey, err, chanerr.ErrChannelInternal)
	}

	// 6. 同 hash 已存在 → 幂等返已有,删新上传对象避孤儿
	if existing, err := s.repo.FindChannelDocumentVersionByHash(ctx, doc.ID, hash); err == nil && existing != nil {
		if delErr := s.oss.DeleteObject(ctx, payload.OSSKey); delErr != nil {
			s.logger.WarnCtx(ctx, "channel: delete dup upload failed", map[string]any{
				"oss_key": payload.OSSKey, "err": delErr.Error(),
			})
		}
		return &SaveVersionOutput{Document: *doc, Version: *existing, Created: false}, nil
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("find version by hash: %w: %w", err, chanerr.ErrChannelInternal)
	}

	// 7. 写新 version + 更新 doc 指针(用上传 OSS key,不 rename)
	if len(editSummary) > chanerr.ChannelDocumentEditSummaryMaxLen {
		editSummary = editSummary[:chanerr.ChannelDocumentEditSummaryMaxLen]
	}
	version := &model.ChannelDocumentVersion{
		DocumentID:          doc.ID,
		Version:             hash,
		OSSKey:              payload.OSSKey,
		ByteSize:            size,
		EditedByPrincipalID: actorPID,
		EditSummary:         editSummary,
		CreatedAt:           now,
	}
	updates := map[string]any{
		"current_oss_key":         payload.OSSKey,
		"current_version":         hash,
		"current_byte_size":       size,
		"updated_by_principal_id": actorPID,
		"updated_at":              now,
	}
	err = s.repo.WithTx(ctx, func(tx repository.Repository) error {
		if err := tx.CreateChannelDocumentVersion(ctx, version); err != nil {
			return err
		}
		return tx.UpdateChannelDocumentFields(ctx, doc.ID, updates)
	})
	if err != nil {
		s.logger.WarnCtx(ctx, "channel: orphan OSS upload on commit failure", map[string]any{
			"oss_key": payload.OSSKey, "document_id": doc.ID, "err": err.Error(),
		})
		return nil, fmt.Errorf("commit upload tx: %w: %w", err, chanerr.ErrChannelInternal)
	}

	updated := *doc
	updated.CurrentOSSKey = payload.OSSKey
	updated.CurrentVersion = hash
	updated.CurrentByteSize = size
	updated.UpdatedByPrincipalID = actorPID
	updated.UpdatedAt = now

	s.publishDocEvent(ctx, "channel_document.updated", c, actorPID, &updated, map[string]any{
		"document_title": updated.Title,
		"version":        hash,
		"byte_size":      strconv.FormatInt(size, 10),
		"edit_summary":   editSummary,
		"upload_kind":    "direct", // 区分 SaveVersion 与 OSS 直传,审计 / 监控可分流
	})
	return &SaveVersionOutput{Document: updated, Version: *version, Created: true}, nil
}

// PresignDownloadByPrincipal MCP 入口:agent 拿 OSS GET 预签名 URL,直拉 doc 当前版本。
//
// 不要求持锁(读路径)。允许 archived channel(只读语义)。doc 必须未软删 + 已有版本。
func (s *documentService) PresignDownloadByPrincipal(ctx context.Context, channelID, docID, callerPrincipalID uint64) (*PresignedDownload, error) {
	if s.oss == nil {
		return nil, fmt.Errorf("oss client not configured: %w", chanerr.ErrChannelInternal)
	}
	c, err := s.resolveChannelMemberByPrincipal(ctx, channelID, callerPrincipalID)
	if err != nil {
		return nil, err
	}
	doc, err := s.loadActiveDocInChannel(ctx, c.ID, docID)
	if err != nil {
		return nil, err
	}
	return s.presignDownloadCore(ctx, doc)
}

// PresignDownload HTTP 入口(by-user)。
func (s *documentService) PresignDownload(ctx context.Context, channelID, docID, callerUserID uint64) (*PresignedDownload, error) {
	if s.oss == nil {
		return nil, fmt.Errorf("oss client not configured: %w", chanerr.ErrChannelInternal)
	}
	c, _, err := s.resolveChannelMember(ctx, channelID, callerUserID)
	if err != nil {
		return nil, err
	}
	doc, err := s.loadActiveDocInChannel(ctx, c.ID, docID)
	if err != nil {
		return nil, err
	}
	return s.presignDownloadCore(ctx, doc)
}

// presignDownloadCore 共享:doc 必须有 current_oss_key(空文档拒)。
func (s *documentService) presignDownloadCore(ctx context.Context, doc *model.ChannelDocument) (*PresignedDownload, error) {
	if doc.CurrentOSSKey == "" {
		return nil, chanerr.ErrChannelDocumentContentEmpty
	}
	url, err := s.oss.PresignGetURL(ctx, doc.CurrentOSSKey, PresignTTL)
	if err != nil {
		return nil, fmt.Errorf("presign get url: %w: %w", err, chanerr.ErrChannelInternal)
	}
	return &PresignedDownload{
		DownloadURL: url,
		Version:     doc.CurrentVersion,
		ByteSize:    doc.CurrentByteSize,
		ContentType: contentTypeForKind(doc.ContentKind),
		ExpiresAt:   time.Now().Add(PresignTTL).UTC(),
	}, nil
}

// streamSha256 拉 OSS 对象并算 sha256(单读全流;md/text ≤ 1MB,几百 ms)。
func (s *documentService) streamSha256(ctx context.Context, key string) (string, error) {
	body, err := s.oss.StreamGet(ctx, key, chanerr.ChannelDocumentMaxByteSize+1)
	if err != nil {
		return "", err
	}
	defer body.Close()
	h := sha256.New()
	if _, err := io.Copy(h, body); err != nil {
		return "", fmt.Errorf("read stream: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// buildUploadOSSKey 直传场景的 OSS key:`<prefix>/<orgID>/channel-docs/<docID>/uploaded/<rand>.<ext>`。
//
// 与 SaveVersion 的 `<sha256>.<ext>` 相区分:直传不在 PUT 前知道 sha256(server 在 commit
// 时才算),所以用随机命名;commit 后不 rename(rename 在 OSS 是 copy+delete,昂贵)。
// 同 hash dedup 在 service 层做(找到已有 version 直接删新对象)。
func (s *documentService) buildUploadOSSKey(orgID, docID uint64, rand string, ext string) string {
	prefix := s.ossPrefix
	if prefix == "" {
		prefix = "synapse"
	}
	prefix = strings.Trim(prefix, "/")
	return fmt.Sprintf("%s/%d/channel-docs/%d/uploaded/%s%s", prefix, orgID, docID, rand, ext)
}

// genRandHex 生成 hex 编码的随机串(n 字节随机 → 2n hex chars)。
func genRandHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ── helpers ─────────────────────────────────────────────────────────────────

// resolveChannelMember 校验 channel 存在 + caller 是 member;返回 channel 和 caller principal_id。
// 不限 channel.status —— 读路径允许 archived(只读)。
//
// 内部走 by-principal helper(先 lookup user → principal,再调 by-principal 版本)
// 让 by-user / by-principal 两条入口共享同一份 channel + member 校验逻辑。
func (s *documentService) resolveChannelMember(ctx context.Context, channelID, userID uint64) (*model.Channel, uint64, error) {
	pid, err := s.lookupUserPrincipalID(ctx, userID)
	if err != nil {
		return nil, 0, err
	}
	c, err := s.resolveChannelMemberByPrincipal(ctx, channelID, pid)
	if err != nil {
		return nil, 0, err
	}
	return c, pid, nil
}

// resolveOpenChannelMember 同 resolveChannelMember 但额外要求 channel 未归档(写路径)。
func (s *documentService) resolveOpenChannelMember(ctx context.Context, channelID, userID uint64) (*model.Channel, uint64, error) {
	pid, err := s.lookupUserPrincipalID(ctx, userID)
	if err != nil {
		return nil, 0, err
	}
	c, err := s.resolveOpenChannelMemberByPrincipal(ctx, channelID, pid)
	if err != nil {
		return nil, 0, err
	}
	return c, pid, nil
}

// resolveChannelMemberByPrincipal MCP 路径:caller 直接给 principal_id,无须反查 user。
// principalID == 0 视为未授权直接返 ErrForbidden。
func (s *documentService) resolveChannelMemberByPrincipal(ctx context.Context, channelID, principalID uint64) (*model.Channel, error) {
	if principalID == 0 {
		return nil, chanerr.ErrForbidden
	}
	c, err := s.repo.FindChannelByID(ctx, channelID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, chanerr.ErrChannelNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find channel: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if c == nil {
		return nil, chanerr.ErrChannelNotFound
	}
	if err := s.requireMember(ctx, c.ID, principalID); err != nil {
		return nil, err
	}
	return c, nil
}

// resolveOpenChannelMemberByPrincipal 同 resolveChannelMemberByPrincipal 但要求 channel 未归档(写路径)。
func (s *documentService) resolveOpenChannelMemberByPrincipal(ctx context.Context, channelID, principalID uint64) (*model.Channel, error) {
	c, err := s.resolveChannelMemberByPrincipal(ctx, channelID, principalID)
	if err != nil {
		return nil, err
	}
	if c.Status == chanerr.ChannelStatusArchived {
		return nil, chanerr.ErrChannelArchived
	}
	return c, nil
}

// loadDocInChannel 拿文档(含已软删)+ 校验属于该 channel。
func (s *documentService) loadDocInChannel(ctx context.Context, channelID, docID uint64) (*model.ChannelDocument, error) {
	doc, err := s.repo.FindChannelDocumentByID(ctx, docID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, chanerr.ErrChannelDocumentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find document: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if doc == nil || doc.ChannelID != channelID {
		return nil, chanerr.ErrChannelDocumentNotFound
	}
	return doc, nil
}

// loadActiveDocInChannel 同 loadDocInChannel 但拒已软删(写路径)。
func (s *documentService) loadActiveDocInChannel(ctx context.Context, channelID, docID uint64) (*model.ChannelDocument, error) {
	doc, err := s.loadDocInChannel(ctx, channelID, docID)
	if err != nil {
		return nil, err
	}
	if doc.DeletedAt != nil {
		return nil, chanerr.ErrChannelDocumentNotFound
	}
	return doc, nil
}

func (s *documentService) requireMember(ctx context.Context, channelID, principalID uint64) error {
	mem, err := s.repo.FindMember(ctx, channelID, principalID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return chanerr.ErrForbidden
	}
	if err != nil {
		return fmt.Errorf("find member: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if mem == nil {
		return chanerr.ErrForbidden
	}
	return nil
}

// requireOwnerOrCreator 删除路径权限:channel owner 或文档创建者本人。
func (s *documentService) requireOwnerOrCreator(ctx context.Context, channelID, actorPID, creatorPID uint64) error {
	if actorPID == creatorPID {
		return nil
	}
	if s.checkOwner(ctx, channelID, actorPID) {
		return nil
	}
	return chanerr.ErrForbidden
}

// checkOwner 不报错变体;FindMember 失败/不存在均当作非 owner。
func (s *documentService) checkOwner(ctx context.Context, channelID, principalID uint64) bool {
	mem, err := s.repo.FindMember(ctx, channelID, principalID)
	if err != nil || mem == nil {
		return false
	}
	return mem.Role == chanerr.MemberRoleOwner
}

func (s *documentService) lookupUserPrincipalID(ctx context.Context, userID uint64) (uint64, error) {
	pid, err := s.repo.LookupUserPrincipalID(ctx, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, chanerr.ErrForbidden
		}
		return 0, fmt.Errorf("lookup user principal: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if pid == 0 {
		return 0, chanerr.ErrForbidden
	}
	return pid, nil
}

// publishDocEvent 共享文档事件统一发到 channel stream;extra 是 event-specific 字段。
func (s *documentService) publishDocEvent(
	ctx context.Context,
	eventType string,
	c *model.Channel,
	actorPID uint64,
	doc *model.ChannelDocument,
	extra map[string]any,
) {
	if s.publisher == nil || s.streamKey == "" {
		return
	}
	fields := map[string]any{
		"event_type":         eventType,
		"org_id":             strconv.FormatUint(c.OrgID, 10),
		"channel_id":         strconv.FormatUint(c.ID, 10),
		"actor_principal_id": strconv.FormatUint(actorPID, 10),
		"document_id":        strconv.FormatUint(doc.ID, 10),
	}
	for k, v := range extra {
		fields[k] = v
	}
	id, err := s.publisher.Publish(ctx, s.streamKey, fields)
	if err != nil {
		s.logger.WarnCtx(ctx, "channel: publish doc event failed", map[string]any{
			"event_type": eventType, "err": err.Error(),
		})
		return
	}
	s.logger.DebugCtx(ctx, "channel: published doc event", map[string]any{
		"event_type": eventType, "stream_id": id,
	})
}

// buildOSSKey 共享文档版本的 OSS key:`<prefix>/<orgID>/channel-docs/<docID>/<hash><.ext>`。
// 同 hash 同 doc 自然 dedup;不同 doc 互不干扰。
func (s *documentService) buildOSSKey(orgID, docID uint64, hash, ext string) string {
	prefix := s.ossPrefix
	if prefix == "" {
		prefix = "synapse"
	}
	prefix = strings.Trim(prefix, "/")
	return fmt.Sprintf("%s/%d/channel-docs/%d/%s%s", prefix, orgID, docID, hash, ext)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func extForKind(kind string) string {
	switch kind {
	case chanerr.ChannelDocumentKindMarkdown:
		return ".md"
	case chanerr.ChannelDocumentKindText:
		return ".txt"
	default:
		return ""
	}
}

func contentTypeForKind(kind string) string {
	switch kind {
	case chanerr.ChannelDocumentKindMarkdown:
		return "text/markdown; charset=utf-8"
	default:
		return "text/plain; charset=utf-8"
	}
}
