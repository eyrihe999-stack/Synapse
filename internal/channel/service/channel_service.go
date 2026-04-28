package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	chanerr "github.com/eyrihe999-stack/Synapse/internal/channel"
	"github.com/eyrihe999-stack/Synapse/internal/channel/model"
	"github.com/eyrihe999-stack/Synapse/internal/channel/repository"
	"github.com/eyrihe999-stack/Synapse/internal/common/eventbus"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
)

// ChannelService channel 子领域业务接口。
//
// AttachVersion / DetachVersion / ListVersions 三个老方法已废弃 ——
// channel ↔ version 多对多关联(channel_versions 表)整体退役。新模型里
// channel.workstream_id → workstream.version_id 是单向引用,通过 workstream
// 表反查即可,不需要 channel 模块自己维护关系层。
type ChannelService interface {
	Create(ctx context.Context, projectID, actorUserID uint64, name, purpose string) (*model.Channel, error)
	Get(ctx context.Context, id uint64) (*model.Channel, error)
	ListByProject(ctx context.Context, projectID uint64, limit, offset int) ([]model.Channel, error)
	// ListByPrincipal 列 principal 作为成员的 channel。MCP list_channels tool 用。
	// 不做额外 org 校验:channel_members 行本身就是权限源。
	ListByPrincipal(ctx context.Context, principalID uint64, limit, offset int) ([]model.Channel, error)
	Archive(ctx context.Context, id, actorUserID uint64) error
}

type channelService struct {
	repo       repository.Repository
	orgChecker OrgMembershipChecker
	publisher  eventbus.Publisher
	streamKey  string
	logger     logger.LoggerInterface
}

func newChannelService(repo repository.Repository, orgChecker OrgMembershipChecker, publisher eventbus.Publisher, streamKey string, log logger.LoggerInterface) ChannelService {
	return &channelService{repo: repo, orgChecker: orgChecker, publisher: publisher, streamKey: streamKey, logger: log}
}

// publishChannelEvent XADD 到 synapse:channel:events,失败仅 warn。
func (s *channelService) publishChannelEvent(ctx context.Context, fields map[string]any) {
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

// Create 在 project 下新建 channel,并自动把创建者加为 owner。
//
// 调用者必须是 project 所属 org 的成员。project 归档后不允许新建 channel。
// 整个过程走事务:channel 行和创建者的 channel_members(role=owner) 行同成或同败。
func (s *channelService) Create(ctx context.Context, projectID, actorUserID uint64, name, purpose string) (*model.Channel, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > chanerr.NameMaxLen {
		return nil, chanerr.ErrChannelNameInvalid
	}
	if len(purpose) > chanerr.PurposeMaxLen {
		return nil, chanerr.ErrChannelNameInvalid
	}

	p, err := s.repo.FindProjectInfo(ctx, projectID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, chanerr.ErrProjectNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find project: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if p.ArchivedAt != nil {
		return nil, chanerr.ErrProjectArchived
	}

	ok, err := s.orgChecker.IsMember(ctx, p.OrgID, actorUserID)
	if err != nil {
		return nil, fmt.Errorf("check org membership: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if !ok {
		return nil, chanerr.ErrForbidden
	}

	// 需要查 actor 的 principal_id 才能把他作为 channel owner。
	// 直接从 users 表反查 —— 简单明确,不经过中间抽象。
	actorPrincipalID, err := s.lookupUserPrincipalID(ctx, actorUserID)
	if err != nil {
		return nil, err
	}

	var created *model.Channel
	err = s.repo.WithTx(ctx, func(tx repository.Repository) error {
		c := &model.Channel{
			OrgID:     p.OrgID,
			ProjectID: projectID,
			Name:      name,
			Purpose:   purpose,
			Status:    chanerr.ChannelStatusOpen,
			CreatedBy: actorUserID,
		}
		if err := tx.CreateChannel(ctx, c); err != nil {
			return fmt.Errorf("create channel: %w", err)
		}
		// 创建者自动成为 owner
		if err := tx.AddMember(ctx, &model.ChannelMember{
			ChannelID:   c.ID,
			PrincipalID: actorPrincipalID,
			Role:        chanerr.MemberRoleOwner,
			JoinedAt:    time.Now(),
		}); err != nil {
			return fmt.Errorf("add creator as owner: %w", err)
		}
		// auto-include:把 auto_include_in_new_channels=TRUE 的 agents 拉进来
		// (全局 org_id=0 的顶级 agent + 本 org 的 auto_include 专项 agent)。
		// 失败只 log,不 abort channel 创建 —— channel 能用比"顶级 agent 缺席"更重要。
		if err := s.autoIncludeAgents(ctx, tx, c.ID, p.OrgID, actorPrincipalID); err != nil {
			s.logger.WarnCtx(ctx, "channel: auto-include agents failed; channel still created", map[string]any{
				"channel_id": c.ID, "org_id": p.OrgID, "err": err.Error(),
			})
		}
		created = c
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", err, chanerr.ErrChannelInternal)
	}
	s.logger.InfoCtx(ctx, "channel: channel created", map[string]any{
		"channel_id": created.ID, "project_id": projectID, "actor": actorUserID,
	})
	return created, nil
}

func (s *channelService) Get(ctx context.Context, id uint64) (*model.Channel, error) {
	c, err := s.repo.FindChannelByID(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, chanerr.ErrChannelNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find channel: %w: %w", err, chanerr.ErrChannelInternal)
	}
	return c, nil
}

func (s *channelService) ListByProject(ctx context.Context, projectID uint64, limit, offset int) ([]model.Channel, error) {
	if limit <= 0 {
		limit = chanerr.ListDefaultLimit
	}
	if limit > chanerr.ListMaxLimit {
		limit = chanerr.ListMaxLimit
	}
	return s.repo.ListChannelsByProject(ctx, projectID, limit, offset)
}

// ListByPrincipal 列 principal(user 或 agent 都行)作为成员的所有 channel。
func (s *channelService) ListByPrincipal(ctx context.Context, principalID uint64, limit, offset int) ([]model.Channel, error) {
	if principalID == 0 {
		return nil, chanerr.ErrForbidden
	}
	if limit <= 0 {
		limit = chanerr.ListDefaultLimit
	}
	if limit > chanerr.ListMaxLimit {
		limit = chanerr.ListMaxLimit
	}
	return s.repo.ListChannelsByPrincipal(ctx, principalID, limit, offset)
}

// Archive 归档 channel。调用者必须是 channel owner。幂等:已归档返 nil。
//
// PR #4 落地后,这里要触发 artifact 晋升 KB(通过事件或直接调用 artifact svc);
// 暂时只改状态。
func (s *channelService) Archive(ctx context.Context, id, actorUserID uint64) error {
	c, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if c.ArchivedAt != nil {
		return nil
	}
	actorPID, err := s.requireChannelOwner(ctx, id, actorUserID)
	if err != nil {
		return err
	}
	now := time.Now()
	if err := s.repo.UpdateChannelFields(ctx, id, map[string]any{
		"status":      chanerr.ChannelStatusArchived,
		"archived_at": now,
	}); err != nil {
		return fmt.Errorf("archive channel: %w: %w", err, chanerr.ErrChannelInternal)
	}
	s.logger.InfoCtx(ctx, "channel: channel archived", map[string]any{
		"channel_id": id, "actor": actorUserID,
	})
	s.publishChannelEvent(ctx, map[string]any{
		"event_type":         "channel.archived",
		"org_id":             strconv.FormatUint(c.OrgID, 10),
		"channel_id":         strconv.FormatUint(c.ID, 10),
		"actor_principal_id": strconv.FormatUint(actorPID, 10),
	})
	return nil
}

// autoIncludeAgents 把 auto_include_in_new_channels=TRUE 的 agents 作为 member
// 加进新建的 channel。
//
// 查询条件(见 repository.LookupAutoIncludeAgentPrincipals):
//   - agents.auto_include_in_new_channels = TRUE
//   - agents.enabled = TRUE
//   - agents.org_id = 0 (全局内嵌)  OR  agents.org_id = <channel.org_id>(本 org 专项)
//
// skipPrincipalID 跳过该 id(理论上创建者已经是 owner,避免撞主键)。
// 单个 agent add 失败只 log,继续下一个;整体失败会抛给调用方,由调用方决定(当前
// channel_service.Create 会吞掉并 warn,不阻塞 channel 创建)。
func (s *channelService) autoIncludeAgents(ctx context.Context, tx repository.Repository, channelID, orgID, skipPrincipalID uint64) error {
	principals, err := tx.LookupAutoIncludeAgentPrincipals(ctx, orgID)
	if err != nil {
		return fmt.Errorf("lookup auto-include agents: %w", err)
	}
	now := time.Now()
	for _, pid := range principals {
		if pid == 0 || pid == skipPrincipalID {
			continue
		}
		mem := &model.ChannelMember{
			ChannelID:   channelID,
			PrincipalID: pid,
			Role:        chanerr.MemberRoleMember,
			JoinedAt:    now,
		}
		if err := tx.AddMember(ctx, mem); err != nil {
			s.logger.WarnCtx(ctx, "channel: auto-include single agent failed", map[string]any{
				"channel_id": channelID, "principal_id": pid, "err": err.Error(),
			})
			// 不中断循环 —— 一个 agent 加失败不影响其他 agent;也不影响 channel 本身
			continue
		}
	}
	if len(principals) > 0 {
		s.logger.InfoCtx(ctx, "channel: auto-included agents", map[string]any{
			"channel_id": channelID, "count": len(principals),
		})
	}
	return nil
}

// requireChannelOwner 校验 actor 是 channel 的 owner;返 actor principal_id 供 publish 使用。
func (s *channelService) requireChannelOwner(ctx context.Context, channelID, actorUserID uint64) (uint64, error) {
	pid, err := s.lookupUserPrincipalID(ctx, actorUserID)
	if err != nil {
		return 0, err
	}
	m, err := s.repo.FindMember(ctx, channelID, pid)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, chanerr.ErrForbidden
	}
	if err != nil {
		return 0, fmt.Errorf("lookup channel member: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if m.Role != chanerr.MemberRoleOwner {
		return 0, chanerr.ErrForbidden
	}
	return pid, nil
}

// requireChannelMember 校验 actor 是 channel 任一角色的成员。
func (s *channelService) requireChannelMember(ctx context.Context, channelID, actorUserID uint64) error {
	pid, err := s.lookupUserPrincipalID(ctx, actorUserID)
	if err != nil {
		return err
	}
	_, err = s.repo.FindMember(ctx, channelID, pid)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return chanerr.ErrForbidden
	}
	if err != nil {
		return fmt.Errorf("lookup channel member: %w: %w", err, chanerr.ErrChannelInternal)
	}
	return nil
}

// lookupUserPrincipalID 从 users.id 反查 principal_id。
//
// 为什么不让 handler 层直接传 principal_id:handler 拿到的 JWT 里只有 user_id
// (sub),service 层自己补齐 principal_id 对上游零侵入。PR #1 之后每个 user 都
// 保证有 principal_id,查不到视为内部错误。
func (s *channelService) lookupUserPrincipalID(ctx context.Context, userID uint64) (uint64, error) {
	pid, err := s.repo.LookupUserPrincipalID(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("lookup user %d principal_id: %w: %w", userID, err, chanerr.ErrChannelInternal)
	}
	if pid == 0 {
		return 0, fmt.Errorf("user %d has no principal_id: %w", userID, chanerr.ErrChannelInternal)
	}
	return pid, nil
}
