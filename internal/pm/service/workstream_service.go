package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/common/eventbus"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/pm"
	"github.com/eyrihe999-stack/Synapse/internal/pm/model"
	"github.com/eyrihe999-stack/Synapse/internal/pm/repository"
)

// WorkstreamService workstream 子领域业务接口。
//
// PR-A:Create 后通过 pm event publisher 发 workstream.created;channel 模块的
// pm event consumer(channel/pmevent)收到后 lazy-create kind=workstream channel
// 并 UPDATE workstream.channel_id 反指。
//
// PR-B:加 InviteToChannel 给 invite_to_workstream MCP tool 用。
type WorkstreamService interface {
	Create(ctx context.Context, initiativeID, actorUserID uint64, versionID *uint64, name, description string) (*model.Workstream, error)
	Get(ctx context.Context, id uint64) (*model.Workstream, error)
	ListByInitiative(ctx context.Context, initiativeID uint64, limit, offset int) ([]model.Workstream, error)
	ListByVersion(ctx context.Context, versionID uint64, limit, offset int) ([]model.Workstream, error)
	ListByProject(ctx context.Context, projectID uint64, limit, offset int) ([]model.Workstream, error)
	Update(ctx context.Context, id, actorUserID uint64, updates map[string]any) error
	// InviteToChannel 把一组 principal 加入 workstream 关联 channel(角色 member)。
	// actor 必须是 project 所属 org 成员;workstream 必须未归档 + channel 已 lazy-create。
	// 返回 (added_principal_ids, channel_id, err)。
	InviteToChannel(ctx context.Context, workstreamID, actorUserID uint64, principalIDs []uint64) ([]uint64, uint64, error)
}

type workstreamService struct {
	repo       repository.Repository
	orgChecker OrgMembershipChecker
	publisher  eventbus.Publisher
	streamKey  string
	logger     logger.LoggerInterface
}

func newWorkstreamService(repo repository.Repository, orgChecker OrgMembershipChecker, publisher eventbus.Publisher, streamKey string, log logger.LoggerInterface) WorkstreamService {
	return &workstreamService{repo: repo, orgChecker: orgChecker, publisher: publisher, streamKey: streamKey, logger: log}
}

// Create 在 initiative 下新建 workstream;可选挂 version。
//
// 校验链:initiative 存在且未归档 → version 存在(若给)且属于同 project →
// actor 是该 project 所属 org 成员。
func (s *workstreamService) Create(ctx context.Context, initiativeID, actorUserID uint64, versionID *uint64, name, description string) (*model.Workstream, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > pm.WorkstreamNameMaxLen {
		return nil, pm.ErrWorkstreamNameInvalid
	}
	if len(description) > pm.WorkstreamDescriptionMaxLen {
		return nil, pm.ErrWorkstreamNameInvalid
	}

	init, err := s.repo.FindInitiativeByID(ctx, initiativeID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, pm.ErrWorkstreamInitiativeInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("find initiative: %w: %w", err, pm.ErrPMInternal)
	}
	if init.ArchivedAt != nil {
		return nil, pm.ErrInitiativeArchived
	}

	p, err := s.repo.FindProjectByID(ctx, init.ProjectID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, pm.ErrProjectNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find project: %w: %w", err, pm.ErrPMInternal)
	}
	if p.ArchivedAt != nil {
		return nil, pm.ErrProjectArchived
	}
	ok, err := s.orgChecker.IsMember(ctx, p.OrgID, actorUserID)
	if err != nil {
		return nil, fmt.Errorf("check org membership: %w: %w", err, pm.ErrPMInternal)
	}
	if !ok {
		return nil, pm.ErrForbidden
	}

	if versionID != nil {
		v, err := s.repo.FindVersionByID(ctx, *versionID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, pm.ErrWorkstreamVersionInvalid
		}
		if err != nil {
			return nil, fmt.Errorf("find version: %w: %w", err, pm.ErrPMInternal)
		}
		if v.ProjectID != init.ProjectID {
			return nil, pm.ErrWorkstreamVersionInvalid
		}
	}

	w := &model.Workstream{
		InitiativeID: initiativeID,
		VersionID:    versionID,
		ProjectID:    init.ProjectID,
		Name:         name,
		Description:  description,
		Status:       pm.WorkstreamStatusDraft,
		CreatedBy:    actorUserID,
	}
	if err := s.repo.CreateWorkstream(ctx, w); err != nil {
		// (initiative_id, name_active) UNIQUE 撞库 → 友好错误回 LLM/前端,
		// LLM 拿到 dup 自然知道"这个 ws 已经存在",跳过重复 create。
		if isUniqueViolation(err) {
			return nil, pm.ErrWorkstreamNameDup
		}
		return nil, fmt.Errorf("create workstream: %w: %w", err, pm.ErrPMInternal)
	}
	s.logger.InfoCtx(ctx, "pm: workstream created", map[string]any{
		"workstream_id": w.ID, "initiative_id": initiativeID, "version_id": versionID, "actor": actorUserID,
	})
	versionIDStr := "0"
	if versionID != nil {
		versionIDStr = strconv.FormatUint(*versionID, 10)
	}
	// workstream.created 是关键事件:channel 模块的 pm consumer 收到后会
	// lazy-create 一个 kind=workstream channel,把 workstream.channel_id 反指。
	publishPMEvent(ctx, s.publisher, s.streamKey, s.logger, map[string]any{
		"event_type":    EventWorkstreamCreated,
		"org_id":        strconv.FormatUint(p.OrgID, 10),
		"project_id":    strconv.FormatUint(init.ProjectID, 10),
		"initiative_id": strconv.FormatUint(initiativeID, 10),
		"version_id":    versionIDStr,
		"workstream_id": strconv.FormatUint(w.ID, 10),
		"actor_user_id": strconv.FormatUint(actorUserID, 10),
		"name":          w.Name,
	})
	return w, nil
}

// Get 返回 workstream。
func (s *workstreamService) Get(ctx context.Context, id uint64) (*model.Workstream, error) {
	w, err := s.repo.FindWorkstreamByID(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, pm.ErrWorkstreamNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find workstream: %w: %w", err, pm.ErrPMInternal)
	}
	return w, nil
}

func (s *workstreamService) ListByInitiative(ctx context.Context, initiativeID uint64, limit, offset int) ([]model.Workstream, error) {
	if limit <= 0 {
		limit = pm.ListDefaultLimit
	}
	if limit > pm.ListMaxLimit {
		limit = pm.ListMaxLimit
	}
	return s.repo.ListWorkstreamsByInitiative(ctx, initiativeID, limit, offset)
}

func (s *workstreamService) ListByVersion(ctx context.Context, versionID uint64, limit, offset int) ([]model.Workstream, error) {
	if limit <= 0 {
		limit = pm.ListDefaultLimit
	}
	if limit > pm.ListMaxLimit {
		limit = pm.ListMaxLimit
	}
	return s.repo.ListWorkstreamsByVersion(ctx, versionID, limit, offset)
}

func (s *workstreamService) ListByProject(ctx context.Context, projectID uint64, limit, offset int) ([]model.Workstream, error) {
	if limit <= 0 {
		limit = pm.ListDefaultLimit
	}
	if limit > pm.ListMaxLimit {
		limit = pm.ListMaxLimit
	}
	return s.repo.ListWorkstreamsByProject(ctx, projectID, limit, offset)
}

// Update 改 workstream 字段;支持改 name / description / status / version_id /
// channel_id。version_id 改成 nil = 移到 backlog;改成新 ID 校验同 project。
func (s *workstreamService) Update(ctx context.Context, id, actorUserID uint64, updates map[string]any) error {
	w, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if w.ArchivedAt != nil {
		return pm.ErrWorkstreamNotFound // 归档当不存在,避免暴露 archived 语义给前端做无意义 retry
	}
	p, err := s.repo.FindProjectByID(ctx, w.ProjectID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return pm.ErrProjectNotFound
	}
	if err != nil {
		return fmt.Errorf("find project: %w: %w", err, pm.ErrPMInternal)
	}
	ok, err := s.orgChecker.IsMember(ctx, p.OrgID, actorUserID)
	if err != nil {
		return fmt.Errorf("check org membership: %w: %w", err, pm.ErrPMInternal)
	}
	if !ok {
		return pm.ErrForbidden
	}

	// 校验 status 枚举
	if status, ok := updates["status"].(string); ok {
		if !pm.IsValidWorkstreamStatus(status) {
			return pm.ErrWorkstreamStatusInvalid
		}
	}
	// 校验 version_id 同 project(允许传 0 / nil 表示移到 backlog)
	if rawVer, ok := updates["version_id"]; ok && rawVer != nil {
		if vid, ok := rawVer.(uint64); ok && vid != 0 {
			v, err := s.repo.FindVersionByID(ctx, vid)
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return pm.ErrWorkstreamVersionInvalid
			}
			if err != nil {
				return fmt.Errorf("find version: %w: %w", err, pm.ErrPMInternal)
			}
			if v.ProjectID != w.ProjectID {
				return pm.ErrWorkstreamVersionInvalid
			}
		}
	}
	// 校验 name
	if name, ok := updates["name"].(string); ok {
		name = strings.TrimSpace(name)
		if name == "" || len(name) > pm.WorkstreamNameMaxLen {
			return pm.ErrWorkstreamNameInvalid
		}
		updates["name"] = name
	}

	if err := s.repo.UpdateWorkstreamFields(ctx, id, updates); err != nil {
		return fmt.Errorf("update workstream: %w: %w", err, pm.ErrPMInternal)
	}
	publishPMEvent(ctx, s.publisher, s.streamKey, s.logger, map[string]any{
		"event_type":    EventWorkstreamUpdated,
		"org_id":        strconv.FormatUint(p.OrgID, 10),
		"project_id":    strconv.FormatUint(w.ProjectID, 10),
		"workstream_id": strconv.FormatUint(id, 10),
		"actor_user_id": strconv.FormatUint(actorUserID, 10),
	})
	return nil
}

// InviteToChannel 见接口注释。
func (s *workstreamService) InviteToChannel(ctx context.Context, workstreamID, actorUserID uint64, principalIDs []uint64) ([]uint64, uint64, error) {
	if len(principalIDs) == 0 {
		return nil, 0, nil
	}
	w, err := s.Get(ctx, workstreamID)
	if err != nil {
		return nil, 0, err
	}
	if w.ArchivedAt != nil {
		return nil, 0, pm.ErrWorkstreamNotFound
	}
	p, err := s.repo.FindProjectByID(ctx, w.ProjectID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, 0, pm.ErrProjectNotFound
	}
	if err != nil {
		return nil, 0, fmt.Errorf("find project: %w: %w", err, pm.ErrPMInternal)
	}
	ok, err := s.orgChecker.IsMember(ctx, p.OrgID, actorUserID)
	if err != nil {
		return nil, 0, fmt.Errorf("check org membership: %w: %w", err, pm.ErrPMInternal)
	}
	if !ok {
		return nil, 0, pm.ErrForbidden
	}
	added, channelID, err := s.repo.AddMembersToWorkstreamChannel(ctx, workstreamID, principalIDs)
	if err != nil {
		return nil, 0, fmt.Errorf("add members: %w: %w", err, pm.ErrPMInternal)
	}
	s.logger.InfoCtx(ctx, "pm: workstream members invited", map[string]any{
		"workstream_id": workstreamID, "channel_id": channelID,
		"actor_user_id": actorUserID, "count": len(added),
	})
	return added, channelID, nil
}
