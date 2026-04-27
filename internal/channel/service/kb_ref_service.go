package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"gorm.io/gorm"

	chanerr "github.com/eyrihe999-stack/Synapse/internal/channel"
	"github.com/eyrihe999-stack/Synapse/internal/channel/model"
	"github.com/eyrihe999-stack/Synapse/internal/channel/repository"
	"github.com/eyrihe999-stack/Synapse/internal/common/eventbus"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
)

// KBRefService channel → KB 资源关联的业务接口。
//
// 用途:把 org knowledge base 里的资源(整个 knowledge_source 或单个 document)挂
// 到 channel,channel 成员默认可读。归档后自动失效(service 层按 channel.status
// 过滤;ListKBRefs 也会做这个过滤)。
//
// 权限:
//   - Add / Remove:channel owner(限制成员随手乱挂 KB,泄露面扩大)
//   - List:channel member(成员看到挂了什么)
//
// 存在性校验:**不**跨模块查 knowledge_sources / documents 是否真存在 —— 跨
// 模块耦合成本高,而 id 无效时 list 依然返,真正读具体资源再 404。MVP 简化。
type KBRefService interface {
	Add(ctx context.Context, channelID, actorUserID uint64, kbSourceID, kbDocumentID uint64) (*model.ChannelKBRef, error)
	Remove(ctx context.Context, channelID, refID, actorUserID uint64) error
	List(ctx context.Context, channelID, callerUserID uint64) ([]model.ChannelKBRef, error)
	// ListForPrincipal MCP 路径:直接用 principal 校验成员。
	ListForPrincipal(ctx context.Context, channelID, callerPrincipalID uint64) ([]model.ChannelKBRef, error)
}

type kbRefService struct {
	repo       repository.Repository
	orgChecker OrgMembershipChecker
	publisher  eventbus.Publisher
	streamKey  string
	logger     logger.LoggerInterface
}

func newKBRefService(repo repository.Repository, orgChecker OrgMembershipChecker, publisher eventbus.Publisher, streamKey string, log logger.LoggerInterface) KBRefService {
	return &kbRefService{repo: repo, orgChecker: orgChecker, publisher: publisher, streamKey: streamKey, logger: log}
}

// publishChannelEvent 同 member_service,XADD 到 synapse:channel:events;失败只 warn。
func (s *kbRefService) publishChannelEvent(ctx context.Context, fields map[string]any) {
	if s.publisher == nil || s.streamKey == "" {
		return
	}
	id, err := s.publisher.Publish(ctx, s.streamKey, fields)
	if err != nil {
		s.logger.WarnCtx(ctx, "channel: publish event failed", map[string]any{
			"event_type": fields["event_type"], "err": err.Error(),
		})
		return
	}
	s.logger.DebugCtx(ctx, "channel: published event", map[string]any{
		"event_type": fields["event_type"], "stream_id": id,
	})
}

// Add 给 channel 挂一个 KB 资源。kbSourceID / kbDocumentID 二选一(恰好一个非零)。
func (s *kbRefService) Add(ctx context.Context, channelID, actorUserID uint64, kbSourceID, kbDocumentID uint64) (*model.ChannelKBRef, error) {
	// 二选一校验
	hasSource := kbSourceID != 0
	hasDoc := kbDocumentID != 0
	if hasSource == hasDoc {
		return nil, chanerr.ErrKBRefInvalid
	}

	c, err := s.resolveOpenChannel(ctx, channelID)
	if err != nil {
		return nil, err
	}

	actorPrincipalID, err := s.lookupUserPrincipalID(ctx, actorUserID)
	if err != nil {
		return nil, err
	}
	if err := s.requireOwner(ctx, c.ID, actorPrincipalID); err != nil {
		return nil, err
	}

	row := &model.ChannelKBRef{
		ChannelID:    c.ID,
		KBSourceID:   kbSourceID,
		KBDocumentID: kbDocumentID,
		AddedBy:      actorPrincipalID,
		AddedAt:      time.Now().UTC(),
	}
	if err := s.repo.CreateKBRef(ctx, row); err != nil {
		return nil, fmt.Errorf("create kb ref: %w: %w", err, chanerr.ErrChannelInternal)
	}
	s.publishChannelEvent(ctx, map[string]any{
		"event_type":         "channel.kb_attached",
		"org_id":             strconv.FormatUint(c.OrgID, 10),
		"channel_id":         strconv.FormatUint(c.ID, 10),
		"actor_principal_id": strconv.FormatUint(actorPrincipalID, 10),
		"kb_ref_id":          strconv.FormatUint(row.ID, 10),
		"kb_source_id":       strconv.FormatUint(kbSourceID, 10),
		"kb_document_id":     strconv.FormatUint(kbDocumentID, 10),
	})
	return row, nil
}

// Remove 解挂 KB 资源。ref.channel_id 必须和入参 channelID 匹配(防跨 channel 误删)。
func (s *kbRefService) Remove(ctx context.Context, channelID, refID, actorUserID uint64) error {
	c, err := s.resolveOpenChannel(ctx, channelID)
	if err != nil {
		return err
	}
	actorPrincipalID, err := s.lookupUserPrincipalID(ctx, actorUserID)
	if err != nil {
		return err
	}
	if err := s.requireOwner(ctx, c.ID, actorPrincipalID); err != nil {
		return err
	}
	ref, err := s.repo.FindKBRefByID(ctx, refID)
	if err != nil {
		return fmt.Errorf("find kb ref: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if ref == nil || ref.ChannelID != c.ID {
		return chanerr.ErrKBRefNotFound
	}
	if err := s.repo.DeleteKBRef(ctx, refID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return chanerr.ErrKBRefNotFound
		}
		return fmt.Errorf("delete kb ref: %w: %w", err, chanerr.ErrChannelInternal)
	}
	s.publishChannelEvent(ctx, map[string]any{
		"event_type":         "channel.kb_detached",
		"org_id":             strconv.FormatUint(c.OrgID, 10),
		"channel_id":         strconv.FormatUint(c.ID, 10),
		"actor_principal_id": strconv.FormatUint(actorPrincipalID, 10),
		"kb_ref_id":          strconv.FormatUint(refID, 10),
		"kb_source_id":       strconv.FormatUint(ref.KBSourceID, 10),
		"kb_document_id":     strconv.FormatUint(ref.KBDocumentID, 10),
	})
	return nil
}

// List HTTP 路径:反查 user → principal,调 listCore。
func (s *kbRefService) List(ctx context.Context, channelID, callerUserID uint64) ([]model.ChannelKBRef, error) {
	callerPrincipalID, err := s.lookupUserPrincipalID(ctx, callerUserID)
	if err != nil {
		return nil, err
	}
	return s.listCore(ctx, channelID, callerPrincipalID)
}

// ListForPrincipal MCP 路径:直接用 principal。
func (s *kbRefService) ListForPrincipal(ctx context.Context, channelID, callerPrincipalID uint64) ([]model.ChannelKBRef, error) {
	if callerPrincipalID == 0 {
		return nil, chanerr.ErrForbidden
	}
	return s.listCore(ctx, channelID, callerPrincipalID)
}

// listCore List / ListForPrincipal 共享实现。
//
// Channel archive 后返空列表(挂载关系实际还在 DB,但读权限绑定在 open 状态)。
func (s *kbRefService) listCore(ctx context.Context, channelID, callerPrincipalID uint64) ([]model.ChannelKBRef, error) {
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
	if err := s.requireMember(ctx, c.ID, callerPrincipalID); err != nil {
		return nil, err
	}
	if c.Status == chanerr.ChannelStatusArchived {
		return nil, nil
	}
	return s.repo.ListKBRefsByChannel(ctx, c.ID)
}

// ── helpers ─────────────────────────────────────────────────────────────

func (s *kbRefService) resolveOpenChannel(ctx context.Context, channelID uint64) (*model.Channel, error) {
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
	if c.Status == chanerr.ChannelStatusArchived {
		return nil, chanerr.ErrChannelArchived
	}
	return c, nil
}

func (s *kbRefService) requireMember(ctx context.Context, channelID, principalID uint64) error {
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

func (s *kbRefService) requireOwner(ctx context.Context, channelID, principalID uint64) error {
	mem, err := s.repo.FindMember(ctx, channelID, principalID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return chanerr.ErrForbidden
	}
	if err != nil {
		return fmt.Errorf("find member: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if mem == nil || mem.Role != chanerr.MemberRoleOwner {
		return chanerr.ErrForbidden
	}
	return nil
}

func (s *kbRefService) lookupUserPrincipalID(ctx context.Context, userID uint64) (uint64, error) {
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
