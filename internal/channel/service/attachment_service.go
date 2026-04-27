// attachment_service.go channel 级附件(图片等)的业务层。
//
// 协作模型:
//   - 任何 channel 成员都能上传附件 / 读 attachment metadata
//   - 任何 channel 成员都能拉短期签名 GET URL 下载(读路径,允许 archived channel)
//   - 不做 lock(图片是不可变资产,改图就上传新图)
//   - 不做版本(同上)
//   - 去重:(channel_id, sha256) UNIQUE,同 channel 重传同字节复用现有行
//
// 上传链路镜像 channel 共享文档:
//   1. PresignUpload → presign PUT URL + commit_token
//   2. client PUT 字节到 OSS(必须带 Content-Type: <mime>,签名绑定)
//   3. CommitUpload → server HEAD/StreamGet 算 sha256 + dedup + 写 channel_attachments 行
//
// 鉴权链路:GET /attachments/:id 由 handler 层校 caller 是 channel 成员 → 调
// PresignDownload 拿短期签名 URL → 302 redirect。字节不经 server。
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"gorm.io/gorm"

	chanerr "github.com/eyrihe999-stack/Synapse/internal/channel"
	"github.com/eyrihe999-stack/Synapse/internal/channel/model"
	"github.com/eyrihe999-stack/Synapse/internal/channel/repository"
	"github.com/eyrihe999-stack/Synapse/internal/channel/uploadtoken"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/common/ossupload"
)

// AttachmentService channel 级附件对外接口。
//
// 与 DocumentService 一样双入口:
//   - by-user(HTTP):反查 users.id → principal
//   - by-principal(MCP):agent 直接以自己的 principal 操作
type AttachmentService interface {
	// PresignUpload 拿 OSS 直传预签名 URL + commit_token。
	// mime 必须在 chanerr.AllowedAttachmentMimeTypes 白名单内。
	// filename 可空,透传到 commit。
	PresignUpload(ctx context.Context, in PresignAttachmentUploadInput) (*PresignedAttachmentUpload, error)
	PresignUploadByPrincipal(ctx context.Context, in PresignAttachmentUploadByPrincipalInput) (*PresignedAttachmentUpload, error)

	// CommitUpload 通知服务端"OSS PUT 已完成,落 channel_attachments 行"。
	// server 验 token + 校验 caller 仍是 channel 成员 + HEAD/算 sha256 + dedup +
	// 写新行(或返已有)。
	CommitUpload(ctx context.Context, in CommitAttachmentUploadInput) (*CommitAttachmentUploadOutput, error)
	CommitUploadByPrincipal(ctx context.Context, in CommitAttachmentUploadByPrincipalInput) (*CommitAttachmentUploadOutput, error)

	// OpenForStream 校验 caller 是 channel 成员 + attachment 未软删 + 属于本 channel,
	// 返回 attachment 元数据 + OSS 字节流(ReadCloser,调用方必须 Close)。
	// handler 拿到流后 io.Copy 直接写到 HTTP 响应体(server-side stream proxy)——
	// 这样浏览器 <img> 全程同源,不依赖 OSS CORS,鉴权也走 server 自己的中间件。
	// 允许 archived channel(读路径)。
	OpenForStream(ctx context.Context, channelID, attachmentID, callerUserID uint64) (*AttachmentStream, error)
	OpenForStreamByPrincipal(ctx context.Context, channelID, attachmentID, callerPrincipalID uint64) (*AttachmentStream, error)
}

// PresignAttachmentUploadInput by-user 入参。
type PresignAttachmentUploadInput struct {
	ChannelID   uint64
	ActorUserID uint64
	MimeType    string
	Filename    string // 可空
}

// PresignAttachmentUploadByPrincipalInput by-principal 入参。
type PresignAttachmentUploadByPrincipalInput struct {
	ChannelID        uint64
	ActorPrincipalID uint64
	MimeType         string
	Filename         string // 可空
}

// PresignedAttachmentUpload PresignUpload 返:供客户端直传 OSS + 后续 commit 用。
//
// 客户端必须 PUT 时带 Content-Type: <ContentType>(签名时绑定),否则 OSS 返
// SignatureDoesNotMatch。
type PresignedAttachmentUpload struct {
	UploadURL   string
	CommitToken string
	OSSKey      string // server 内部用,客户端可忽略
	ContentType string // PUT 时的 Content-Type header(等于入参 MimeType)
	ExpiresAt   time.Time
	MaxByteSize int64
}

// CommitAttachmentUploadInput by-user commit 入参。
type CommitAttachmentUploadInput struct {
	ChannelID   uint64
	ActorUserID uint64
	CommitToken string
}

// CommitAttachmentUploadByPrincipalInput by-principal commit 入参。
type CommitAttachmentUploadByPrincipalInput struct {
	ChannelID        uint64
	ActorPrincipalID uint64
	CommitToken      string
}

// CommitAttachmentUploadOutput commit 结果。
//
// Reused=true 表示同 (channel_id, sha256) 已有行,本次未实际写新 attachment(返已有);
// 这种情况新上传的 OSS 对象会被 server 删掉避孤儿。
type CommitAttachmentUploadOutput struct {
	Attachment model.ChannelAttachment
	Reused     bool
}

// AttachmentStream OpenForStream 返:元数据 + OSS 字节流。
//
// 调用方(handler)负责 io.Copy(c.Writer, Body) 后 Body.Close()。
// MimeType / ByteSize 用于设响应头(Content-Type / Content-Length)。
type AttachmentStream struct {
	Attachment model.ChannelAttachment
	Body       io.ReadCloser
	MimeType   string
	ByteSize   int64
}

type attachmentService struct {
	repo         repository.Repository
	oss          ossupload.Client
	ossPrefix    string
	uploadSigner *uploadtoken.Signer // 可 nil:nil 时所有 Presign/Commit 路径返错
	logger       logger.LoggerInterface
}

func newAttachmentService(
	repo repository.Repository,
	oss ossupload.Client,
	ossPrefix string,
	uploadSigner *uploadtoken.Signer,
	log logger.LoggerInterface,
) AttachmentService {
	return &attachmentService{
		repo:         repo,
		oss:          oss,
		ossPrefix:    ossPrefix,
		uploadSigner: uploadSigner,
		logger:       log,
	}
}

// ── Presign ────────────────────────────────────────────────────────────────

func (s *attachmentService) PresignUpload(ctx context.Context, in PresignAttachmentUploadInput) (*PresignedAttachmentUpload, error) {
	if s.uploadSigner == nil || s.oss == nil {
		return nil, fmt.Errorf("attachment upload not configured: %w", chanerr.ErrChannelInternal)
	}
	if !chanerr.IsValidAttachmentMimeType(in.MimeType) {
		return nil, chanerr.ErrChannelAttachmentMimeInvalid
	}
	if len(in.Filename) > chanerr.ChannelAttachmentFilenameMaxLen {
		in.Filename = in.Filename[:chanerr.ChannelAttachmentFilenameMaxLen]
	}
	c, _, err := s.resolveOpenChannelMember(ctx, in.ChannelID, in.ActorUserID)
	if err != nil {
		return nil, err
	}
	return s.presignUploadCore(ctx, c, in.MimeType, in.Filename)
}

func (s *attachmentService) PresignUploadByPrincipal(ctx context.Context, in PresignAttachmentUploadByPrincipalInput) (*PresignedAttachmentUpload, error) {
	if s.uploadSigner == nil || s.oss == nil {
		return nil, fmt.Errorf("attachment upload not configured: %w", chanerr.ErrChannelInternal)
	}
	if !chanerr.IsValidAttachmentMimeType(in.MimeType) {
		return nil, chanerr.ErrChannelAttachmentMimeInvalid
	}
	if len(in.Filename) > chanerr.ChannelAttachmentFilenameMaxLen {
		in.Filename = in.Filename[:chanerr.ChannelAttachmentFilenameMaxLen]
	}
	c, err := s.resolveOpenChannelMemberByPrincipal(ctx, in.ChannelID, in.ActorPrincipalID)
	if err != nil {
		return nil, err
	}
	return s.presignUploadCore(ctx, c, in.MimeType, in.Filename)
}

// presignUploadCore by-user / by-principal 共享:生成 OSS key + presign + sign token。
//
// OSS key 模式:`<prefix>/<orgID>/channel-attachments/<channelID>/<rand>.<ext>`。
// rand 随机命名(没法在 PUT 前知道 sha256;commit 时算)。同 hash dedup 在 commit
// 阶段做(命中已有 → 删新对象 → 复用旧行)。
func (s *attachmentService) presignUploadCore(ctx context.Context, c *model.Channel, mime, filename string) (*PresignedAttachmentUpload, error) {
	rand16, err := genRandHex(uploadKeyRandLen / 2)
	if err != nil {
		return nil, fmt.Errorf("gen attachment upload random: %w: %w", err, chanerr.ErrChannelInternal)
	}
	ext := extForAttachmentMime(mime)
	key := s.buildAttachmentOSSKey(c.OrgID, c.ID, rand16, ext)

	url, err := s.oss.PresignPutURL(ctx, key, PresignTTL, mime)
	if err != nil {
		return nil, fmt.Errorf("presign attachment put url: %w: %w", err, chanerr.ErrChannelInternal)
	}
	expiresAt := time.Now().Add(PresignTTL).UTC()
	token, err := s.uploadSigner.Sign(uploadtoken.Payload{
		ChannelID: c.ID,
		OSSKey:    key,
		MimeType:  mime,
		Filename:  filename,
		ExpiresAt: expiresAt.Unix(),
	})
	if err != nil {
		return nil, fmt.Errorf("sign attachment token: %w: %w", err, chanerr.ErrChannelInternal)
	}
	return &PresignedAttachmentUpload{
		UploadURL:   url,
		CommitToken: token,
		OSSKey:      key,
		ContentType: mime,
		ExpiresAt:   expiresAt,
		MaxByteSize: chanerr.ChannelAttachmentMaxByteSize,
	}, nil
}

// ── Commit ─────────────────────────────────────────────────────────────────

func (s *attachmentService) CommitUpload(ctx context.Context, in CommitAttachmentUploadInput) (*CommitAttachmentUploadOutput, error) {
	if s.uploadSigner == nil || s.oss == nil {
		return nil, fmt.Errorf("attachment upload not configured: %w", chanerr.ErrChannelInternal)
	}
	c, actorPID, err := s.resolveOpenChannelMember(ctx, in.ChannelID, in.ActorUserID)
	if err != nil {
		return nil, err
	}
	return s.commitUploadCore(ctx, c, actorPID, in.CommitToken)
}

func (s *attachmentService) CommitUploadByPrincipal(ctx context.Context, in CommitAttachmentUploadByPrincipalInput) (*CommitAttachmentUploadOutput, error) {
	if s.uploadSigner == nil || s.oss == nil {
		return nil, fmt.Errorf("attachment upload not configured: %w", chanerr.ErrChannelInternal)
	}
	c, err := s.resolveOpenChannelMemberByPrincipal(ctx, in.ChannelID, in.ActorPrincipalID)
	if err != nil {
		return nil, err
	}
	return s.commitUploadCore(ctx, c, in.ActorPrincipalID, in.CommitToken)
}

// commitUploadCore by-user / by-principal 共享。
//
// 完整校验链:
//  1. 验 token(签名 + 未过期),拿 OSS key + ChannelID + MimeType + Filename
//  2. token.ChannelID 必须 == 当前 channel(防跨 channel 误用)
//  3. HEAD OSS 拿 size,> Max 拒并删 OSS 临时对象;0 拒
//  4. StreamGet OSS 算 sha256
//  5. 查 (channel_id, sha256) 是否已存在:
//     - 是:返已有 + Reused=true,**删新上传对象**避孤儿
//     - 否:写新行;若并发撞 UNIQUE 再查一次返已有
func (s *attachmentService) commitUploadCore(ctx context.Context, c *model.Channel, actorPID uint64, token string) (*CommitAttachmentUploadOutput, error) {
	// 1. 验 token
	payload, err := s.uploadSigner.Verify(token)
	if err != nil {
		return nil, fmt.Errorf("verify attachment token: %w", err)
	}
	if payload.ChannelID != c.ID {
		// 跨 channel 误用 — 当成 invalid token 处理(handler 层翻译成 token invalid)
		return nil, fmt.Errorf("token channel mismatch: %w", uploadtoken.ErrInvalidToken)
	}
	if payload.OSSKey == "" || payload.MimeType == "" {
		return nil, fmt.Errorf("token missing attachment fields: %w", uploadtoken.ErrInvalidToken)
	}

	// 2. HEAD OSS
	size, err := s.oss.HeadObject(ctx, payload.OSSKey)
	if err != nil {
		return nil, fmt.Errorf("head oss attachment %s: %w: %w", payload.OSSKey, err, chanerr.ErrChannelInternal)
	}
	if size <= 0 {
		return nil, chanerr.ErrChannelAttachmentEmpty
	}
	if size > chanerr.ChannelAttachmentMaxByteSize {
		if delErr := s.oss.DeleteObject(ctx, payload.OSSKey); delErr != nil {
			s.logger.WarnCtx(ctx, "channel: delete oversized attachment failed", map[string]any{
				"oss_key": payload.OSSKey, "size": size, "err": delErr.Error(),
			})
		}
		return nil, chanerr.ErrChannelAttachmentTooLarge
	}

	// 3. StreamGet 算 sha256
	hash, err := s.streamSha256(ctx, payload.OSSKey)
	if err != nil {
		return nil, fmt.Errorf("stream sha256 attachment %s: %w: %w", payload.OSSKey, err, chanerr.ErrChannelInternal)
	}

	// 4. 同 (channel_id, sha256) 已存在 → 复用 + 删新上传对象避孤儿
	if existing, err := s.repo.FindChannelAttachmentByChannelAndHash(ctx, c.ID, hash); err == nil && existing != nil {
		if delErr := s.oss.DeleteObject(ctx, payload.OSSKey); delErr != nil {
			s.logger.WarnCtx(ctx, "channel: delete dup attachment upload failed", map[string]any{
				"oss_key": payload.OSSKey, "err": delErr.Error(),
			})
		}
		return &CommitAttachmentUploadOutput{Attachment: *existing, Reused: true}, nil
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("find attachment by hash: %w: %w", err, chanerr.ErrChannelInternal)
	}

	// 5. 写新行
	att := &model.ChannelAttachment{
		ChannelID:             c.ID,
		OrgID:                 c.OrgID,
		OSSKey:                payload.OSSKey,
		MimeType:              payload.MimeType,
		Filename:              payload.Filename,
		ByteSize:              size,
		Sha256:                hash,
		UploadedByPrincipalID: actorPID,
		CreatedAt:             time.Now().UTC(),
	}
	if err := s.repo.CreateChannelAttachment(ctx, att); err != nil {
		// 并发场景:两个 commit 竞态都没命中 dedup,后到的撞 UNIQUE。再查一次返已有,
		// 删新对象。
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			existing, qerr := s.repo.FindChannelAttachmentByChannelAndHash(ctx, c.ID, hash)
			if qerr == nil && existing != nil {
				if delErr := s.oss.DeleteObject(ctx, payload.OSSKey); delErr != nil {
					s.logger.WarnCtx(ctx, "channel: delete dup attachment upload after race failed", map[string]any{
						"oss_key": payload.OSSKey, "err": delErr.Error(),
					})
				}
				return &CommitAttachmentUploadOutput{Attachment: *existing, Reused: true}, nil
			}
		}
		s.logger.WarnCtx(ctx, "channel: orphan OSS attachment on create failure", map[string]any{
			"oss_key": payload.OSSKey, "channel_id": c.ID, "err": err.Error(),
		})
		return nil, fmt.Errorf("create attachment: %w: %w", err, chanerr.ErrChannelInternal)
	}
	return &CommitAttachmentUploadOutput{Attachment: *att, Reused: false}, nil
}

// ── Stream(server-side proxy)──────────────────────────────────────────────

func (s *attachmentService) OpenForStream(ctx context.Context, channelID, attachmentID, callerUserID uint64) (*AttachmentStream, error) {
	if s.oss == nil {
		return nil, fmt.Errorf("oss not configured: %w", chanerr.ErrChannelInternal)
	}
	c, _, err := s.resolveChannelMember(ctx, channelID, callerUserID)
	if err != nil {
		return nil, err
	}
	return s.openForStreamCore(ctx, c, attachmentID)
}

func (s *attachmentService) OpenForStreamByPrincipal(ctx context.Context, channelID, attachmentID, callerPrincipalID uint64) (*AttachmentStream, error) {
	if s.oss == nil {
		return nil, fmt.Errorf("oss not configured: %w", chanerr.ErrChannelInternal)
	}
	c, err := s.resolveChannelMemberByPrincipal(ctx, channelID, callerPrincipalID)
	if err != nil {
		return nil, err
	}
	return s.openForStreamCore(ctx, c, attachmentID)
}

func (s *attachmentService) openForStreamCore(ctx context.Context, c *model.Channel, attachmentID uint64) (*AttachmentStream, error) {
	att, err := s.loadActiveAttachmentInChannel(ctx, c.ID, attachmentID)
	if err != nil {
		return nil, err
	}
	body, err := s.oss.StreamGet(ctx, att.OSSKey, 0)
	if err != nil {
		return nil, fmt.Errorf("stream attachment %s: %w: %w", att.OSSKey, err, chanerr.ErrChannelInternal)
	}
	return &AttachmentStream{
		Attachment: *att,
		Body:       body,
		MimeType:   att.MimeType,
		ByteSize:   att.ByteSize,
	}, nil
}

// ── helpers(部分逻辑与 documentService 同;保持模块隔离选择 copy)─────────

// loadActiveAttachmentInChannel 校验 attachment 存在 + 未软删 + 属于该 channel。
func (s *attachmentService) loadActiveAttachmentInChannel(ctx context.Context, channelID, attID uint64) (*model.ChannelAttachment, error) {
	att, err := s.repo.FindChannelAttachmentByID(ctx, attID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, chanerr.ErrChannelAttachmentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find attachment: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if att == nil || att.ChannelID != channelID || att.DeletedAt != nil {
		return nil, chanerr.ErrChannelAttachmentNotFound
	}
	return att, nil
}

// resolveChannelMember 校 channel 存在 + caller 是 member;返 channel + caller principal_id。
// 不限 channel.status —— 读路径允许 archived。
func (s *attachmentService) resolveChannelMember(ctx context.Context, channelID, userID uint64) (*model.Channel, uint64, error) {
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

// resolveOpenChannelMember 同上但要求 channel 未归档(写路径)。
func (s *attachmentService) resolveOpenChannelMember(ctx context.Context, channelID, userID uint64) (*model.Channel, uint64, error) {
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

func (s *attachmentService) resolveChannelMemberByPrincipal(ctx context.Context, channelID, principalID uint64) (*model.Channel, error) {
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
	mem, err := s.repo.FindMember(ctx, c.ID, principalID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, chanerr.ErrForbidden
	}
	if err != nil {
		return nil, fmt.Errorf("find member: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if mem == nil {
		return nil, chanerr.ErrForbidden
	}
	return c, nil
}

func (s *attachmentService) resolveOpenChannelMemberByPrincipal(ctx context.Context, channelID, principalID uint64) (*model.Channel, error) {
	c, err := s.resolveChannelMemberByPrincipal(ctx, channelID, principalID)
	if err != nil {
		return nil, err
	}
	if c.Status == chanerr.ChannelStatusArchived {
		return nil, chanerr.ErrChannelArchived
	}
	return c, nil
}

func (s *attachmentService) lookupUserPrincipalID(ctx context.Context, userID uint64) (uint64, error) {
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

func (s *attachmentService) streamSha256(ctx context.Context, key string) (string, error) {
	body, err := s.oss.StreamGet(ctx, key, chanerr.ChannelAttachmentMaxByteSize+1)
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

// buildAttachmentOSSKey `<prefix>/<orgID>/channel-attachments/<channelID>/<rand><.ext>`。
func (s *attachmentService) buildAttachmentOSSKey(orgID, channelID uint64, rand string, ext string) string {
	prefix := s.ossPrefix
	if prefix == "" {
		prefix = "synapse"
	}
	prefix = strings.Trim(prefix, "/")
	return fmt.Sprintf("%s/%d/channel-attachments/%d/%s%s", prefix, orgID, channelID, rand, ext)
}

// extForAttachmentMime 给 OSS key 拼后缀。未知 MIME 走 IsValidAttachmentMimeType
// 早就拒了,这里只覆盖白名单 4 项。
func extForAttachmentMime(m string) string {
	switch m {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ""
	}
}
