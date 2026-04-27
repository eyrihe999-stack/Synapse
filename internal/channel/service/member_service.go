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

// MemberService channel 成员管理。
type MemberService interface {
	Add(ctx context.Context, channelID, actorUserID, targetPrincipalID uint64, role string) (*model.ChannelMember, error)
	Remove(ctx context.Context, channelID, actorUserID, targetPrincipalID uint64) error
	UpdateRole(ctx context.Context, channelID, actorUserID, targetPrincipalID uint64, role string) error
	List(ctx context.Context, channelID uint64) ([]model.ChannelMember, error)
	// ListWithProfile 列 channel 成员 + JOIN users/agents 带回 display_name + kind。
	// 无权限检查,仅供进程内 service 层(如 orchestrator 组 prompt)调用。
	// 不经 HTTP 暴露 —— 若将来暴露,需要先加 caller 是 channel 成员的校验。
	ListWithProfile(ctx context.Context, channelID uint64) ([]repository.MemberWithProfile, error)
	// ListWithProfileByPrincipal 同 ListWithProfile,但要求 caller 必须是 channel 成员。
	// MCP list_channel_members tool 用 —— agent 想知道"channel 里都有谁"以便 @ /
	// 派任务,只在自己也在 channel 里时才返。非成员返 ErrForbidden。
	ListWithProfileByPrincipal(ctx context.Context, channelID, callerPrincipalID uint64) ([]repository.MemberWithProfile, error)
}

type memberService struct {
	repo              repository.Repository
	orgChecker        OrgMembershipChecker
	principalResolver PrincipalOrgResolver
	publisher         eventbus.Publisher // 可 nil 降级不发事件
	streamKey         string             // synapse:channel:events
	logger            logger.LoggerInterface
}

func newMemberService(repo repository.Repository, orgChecker OrgMembershipChecker, principalResolver PrincipalOrgResolver, publisher eventbus.Publisher, streamKey string, log logger.LoggerInterface) MemberService {
	return &memberService{
		repo:              repo,
		orgChecker:        orgChecker,
		principalResolver: principalResolver,
		publisher:         publisher,
		streamKey:         streamKey,
		logger:            log,
	}
}

// Add 往 channel 加一个成员。actor 必须是 channel owner;target principal 必须
// 属于 channel 所在 org。
func (s *memberService) Add(ctx context.Context, channelID, actorUserID, targetPrincipalID uint64, role string) (*model.ChannelMember, error) {
	if !isValidMemberRole(role) {
		return nil, chanerr.ErrMemberRoleInvalid
	}
	c, err := s.getChannelForWrite(ctx, channelID)
	if err != nil {
		return nil, err
	}
	actorPID, err := s.requireChannelOwner(ctx, channelID, actorUserID)
	if err != nil {
		return nil, err
	}

	ok, err := s.principalResolver.IsPrincipalInOrg(ctx, targetPrincipalID, c.OrgID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, chanerr.ErrPrincipalNotInOrg
	}

	m := &model.ChannelMember{
		ChannelID:   channelID,
		PrincipalID: targetPrincipalID,
		Role:        role,
		JoinedAt:    time.Now(),
	}
	if err := s.repo.AddMember(ctx, m); err != nil {
		if isUniqueViolation(err) {
			return nil, chanerr.ErrMemberAlreadyExists
		}
		return nil, fmt.Errorf("add member: %w: %w", err, chanerr.ErrChannelInternal)
	}
	s.logger.InfoCtx(ctx, "channel: member added", map[string]any{
		"channel_id": channelID, "principal_id": targetPrincipalID, "role": role, "actor": actorUserID,
	})
	s.publishChannelEvent(ctx, map[string]any{
		"event_type":          "channel.member_added",
		"org_id":              strconv.FormatUint(c.OrgID, 10),
		"channel_id":          strconv.FormatUint(channelID, 10),
		"actor_principal_id":  strconv.FormatUint(actorPID, 10),
		"target_principal_id": strconv.FormatUint(targetPrincipalID, 10),
		"role":                role,
	})
	return m, nil
}

// Remove 移除成员。actor 必须是 channel owner。
// 最后一个 owner 不能被移除 —— 防止 channel 陷入"无人能 archive"。
func (s *memberService) Remove(ctx context.Context, channelID, actorUserID, targetPrincipalID uint64) error {
	c, err := s.getChannelForWrite(ctx, channelID)
	if err != nil {
		return err
	}
	actorPID, err := s.requireChannelOwner(ctx, channelID, actorUserID)
	if err != nil {
		return err
	}

	m, err := s.repo.FindMember(ctx, channelID, targetPrincipalID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return chanerr.ErrMemberNotFound
	}
	if err != nil {
		return fmt.Errorf("find member: %w: %w", err, chanerr.ErrChannelInternal)
	}

	if m.Role == chanerr.MemberRoleOwner {
		n, err := s.repo.CountOwners(ctx, channelID)
		if err != nil {
			return fmt.Errorf("count owners: %w: %w", err, chanerr.ErrChannelInternal)
		}
		if n <= 1 {
			return chanerr.ErrMemberLastOwner
		}
	}

	if err := s.repo.RemoveMember(ctx, channelID, targetPrincipalID); err != nil {
		return fmt.Errorf("remove member: %w: %w", err, chanerr.ErrChannelInternal)
	}
	s.logger.InfoCtx(ctx, "channel: member removed", map[string]any{
		"channel_id": channelID, "principal_id": targetPrincipalID, "actor": actorUserID,
	})
	s.publishChannelEvent(ctx, map[string]any{
		"event_type":          "channel.member_removed",
		"org_id":              strconv.FormatUint(c.OrgID, 10),
		"channel_id":          strconv.FormatUint(channelID, 10),
		"actor_principal_id":  strconv.FormatUint(actorPID, 10),
		"target_principal_id": strconv.FormatUint(targetPrincipalID, 10),
	})
	return nil
}

// UpdateRole 改成员角色。actor 必须 owner。最后一个 owner 不能降级。
func (s *memberService) UpdateRole(ctx context.Context, channelID, actorUserID, targetPrincipalID uint64, role string) error {
	if !isValidMemberRole(role) {
		return chanerr.ErrMemberRoleInvalid
	}
	c, err := s.getChannelForWrite(ctx, channelID)
	if err != nil {
		return err
	}
	actorPID, err := s.requireChannelOwner(ctx, channelID, actorUserID)
	if err != nil {
		return err
	}

	cur, err := s.repo.FindMember(ctx, channelID, targetPrincipalID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return chanerr.ErrMemberNotFound
	}
	if err != nil {
		return fmt.Errorf("find member: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if cur.Role == chanerr.MemberRoleOwner && role != chanerr.MemberRoleOwner {
		n, err := s.repo.CountOwners(ctx, channelID)
		if err != nil {
			return fmt.Errorf("count owners: %w: %w", err, chanerr.ErrChannelInternal)
		}
		if n <= 1 {
			return chanerr.ErrMemberLastOwner
		}
	}
	oldRole := cur.Role
	if err := s.repo.UpdateMemberRole(ctx, channelID, targetPrincipalID, role); err != nil {
		return fmt.Errorf("update member role: %w: %w", err, chanerr.ErrChannelInternal)
	}
	s.publishChannelEvent(ctx, map[string]any{
		"event_type":          "channel.member_role_changed",
		"org_id":              strconv.FormatUint(c.OrgID, 10),
		"channel_id":          strconv.FormatUint(channelID, 10),
		"actor_principal_id":  strconv.FormatUint(actorPID, 10),
		"target_principal_id": strconv.FormatUint(targetPrincipalID, 10),
		"old_role":            oldRole,
		"new_role":            role,
	})
	return nil
}

// List 列出 channel 所有成员。需要 actor 是成员(任意角色),handler 层校验。
func (s *memberService) List(ctx context.Context, channelID uint64) ([]model.ChannelMember, error) {
	return s.repo.ListMembers(ctx, channelID)
}

// ListWithProfile 给进程内 orchestrator 组 prompt 用。详见接口注释。
func (s *memberService) ListWithProfile(ctx context.Context, channelID uint64) ([]repository.MemberWithProfile, error) {
	return s.repo.ListMembersWithProfile(ctx, channelID)
}

// ListWithProfileByPrincipal MCP 路径:caller 必须是 channel 成员。
func (s *memberService) ListWithProfileByPrincipal(ctx context.Context, channelID, callerPrincipalID uint64) ([]repository.MemberWithProfile, error) {
	if callerPrincipalID == 0 {
		return nil, chanerr.ErrForbidden
	}
	if _, err := s.repo.FindMember(ctx, channelID, callerPrincipalID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, chanerr.ErrForbidden
		}
		return nil, fmt.Errorf("lookup channel member: %w: %w", err, chanerr.ErrChannelInternal)
	}
	return s.repo.ListMembersWithProfile(ctx, channelID)
}

// getChannelForWrite 写操作前取 channel 并检查未归档。
func (s *memberService) getChannelForWrite(ctx context.Context, channelID uint64) (*model.Channel, error) {
	c, err := s.repo.FindChannelByID(ctx, channelID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, chanerr.ErrChannelNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find channel: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if c.ArchivedAt != nil {
		return nil, chanerr.ErrChannelArchived
	}
	return c, nil
}

// requireChannelOwner 校验 actor(uint64 user_id)是 channel owner。返 actor 的
// principal_id 供调用方 publish 事件时填 actor_principal_id。
func (s *memberService) requireChannelOwner(ctx context.Context, channelID, actorUserID uint64) (uint64, error) {
	pid, err := s.repo.LookupUserPrincipalID(ctx, actorUserID)
	if err != nil {
		return 0, fmt.Errorf("lookup actor principal_id: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if pid == 0 {
		return 0, chanerr.ErrForbidden
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

// publishChannelEvent XADD 到 synapse:channel:events(fields 约定同 message.posted)。
// 任何失败都只 warn,DB 是真相源。
func (s *memberService) publishChannelEvent(ctx context.Context, fields map[string]any) {
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

func isValidMemberRole(r string) bool {
	switch r {
	case chanerr.MemberRoleOwner, chanerr.MemberRoleMember, chanerr.MemberRoleObserver:
		return true
	}
	return false
}
