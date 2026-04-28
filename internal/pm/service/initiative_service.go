package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/common/eventbus"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/pm"
	"github.com/eyrihe999-stack/Synapse/internal/pm/model"
	"github.com/eyrihe999-stack/Synapse/internal/pm/repository"
)

// InitiativeService initiative 子领域业务接口。
type InitiativeService interface {
	Create(ctx context.Context, projectID, actorUserID uint64, name, description, targetOutcome string) (*model.Initiative, error)
	Get(ctx context.Context, id uint64) (*model.Initiative, error)
	List(ctx context.Context, projectID uint64, limit, offset int) ([]model.Initiative, error)
	Update(ctx context.Context, id, actorUserID uint64, updates map[string]any) error
	Archive(ctx context.Context, id, actorUserID uint64) error
}

type initiativeService struct {
	repo       repository.Repository
	orgChecker OrgMembershipChecker
	publisher  eventbus.Publisher
	streamKey  string
	logger     logger.LoggerInterface
}

func newInitiativeService(repo repository.Repository, orgChecker OrgMembershipChecker, publisher eventbus.Publisher, streamKey string, log logger.LoggerInterface) InitiativeService {
	return &initiativeService{repo: repo, orgChecker: orgChecker, publisher: publisher, streamKey: streamKey, logger: log}
}

// Create 在 project 下新建 initiative。
func (s *initiativeService) Create(ctx context.Context, projectID, actorUserID uint64, name, description, targetOutcome string) (*model.Initiative, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > pm.InitiativeNameMaxLen {
		return nil, pm.ErrInitiativeNameInvalid
	}
	if len(description) > pm.InitiativeDescriptionMaxLen {
		return nil, pm.ErrInitiativeNameInvalid
	}
	if len(targetOutcome) > pm.InitiativeOutcomeMaxLen {
		return nil, pm.ErrInitiativeNameInvalid
	}

	p, err := s.requireProjectActive(ctx, projectID, actorUserID)
	if err != nil {
		return nil, err
	}
	_ = p

	// 应用层预检名字重复
	n, err := s.repo.CountActiveInitiativeByName(ctx, projectID, name)
	if err != nil {
		return nil, fmt.Errorf("count initiative by name: %w: %w", err, pm.ErrPMInternal)
	}
	if n > 0 {
		return nil, pm.ErrInitiativeNameDup
	}

	init := &model.Initiative{
		ProjectID:     projectID,
		Name:          name,
		Description:   description,
		TargetOutcome: targetOutcome,
		Status:        pm.InitiativeStatusPlanned,
		CreatedBy:     actorUserID,
	}
	if err := s.repo.CreateInitiative(ctx, init); err != nil {
		if isUniqueViolation(err) {
			return nil, pm.ErrInitiativeNameDup
		}
		return nil, fmt.Errorf("create initiative: %w: %w", err, pm.ErrPMInternal)
	}
	s.logger.InfoCtx(ctx, "pm: initiative created", map[string]any{
		"initiative_id": init.ID, "project_id": projectID, "actor": actorUserID,
	})
	publishPMEvent(ctx, s.publisher, s.streamKey, s.logger, map[string]any{
		"event_type":    EventInitiativeCreated,
		"org_id":        strconv.FormatUint(p.OrgID, 10),
		"project_id":    strconv.FormatUint(projectID, 10),
		"initiative_id": strconv.FormatUint(init.ID, 10),
		"actor_user_id": strconv.FormatUint(actorUserID, 10),
	})
	return init, nil
}

// Get 返回 initiative。
func (s *initiativeService) Get(ctx context.Context, id uint64) (*model.Initiative, error) {
	i, err := s.repo.FindInitiativeByID(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, pm.ErrInitiativeNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find initiative: %w: %w", err, pm.ErrPMInternal)
	}
	return i, nil
}

// List 列 project 下的 initiative。
func (s *initiativeService) List(ctx context.Context, projectID uint64, limit, offset int) ([]model.Initiative, error) {
	if limit <= 0 {
		limit = pm.ListDefaultLimit
	}
	if limit > pm.ListMaxLimit {
		limit = pm.ListMaxLimit
	}
	return s.repo.ListInitiativesByProject(ctx, projectID, limit, offset)
}

// Update 更新 initiative 字段。is_system 的 default initiative 不允许改名 / 删 / archive。
//
// 当前 v0 支持改 status / description / target_outcome 三类字段;name 走单独
// 路径(避开 is_system 守卫场景的歧义)。前端如要改名,先校验非 is_system,然后
// 把 name 字段塞进 updates 调本方法。
func (s *initiativeService) Update(ctx context.Context, id, actorUserID uint64, updates map[string]any) error {
	i, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if i.IsSystem {
		// system initiative:仅允许 status 改成 active(被使用) / 不允许其他;v0 简化 =
		// 完全不允许 update,后续按需放开
		return pm.ErrInitiativeSystem
	}
	if i.ArchivedAt != nil {
		return pm.ErrInitiativeArchived
	}
	if _, err := s.requireProjectActive(ctx, i.ProjectID, actorUserID); err != nil {
		return err
	}

	if status, ok := updates["status"].(string); ok {
		if !pm.IsValidInitiativeStatus(status) {
			return pm.ErrInitiativeStatusInvalid
		}
	}
	if name, ok := updates["name"].(string); ok {
		name = strings.TrimSpace(name)
		if name == "" || len(name) > pm.InitiativeNameMaxLen {
			return pm.ErrInitiativeNameInvalid
		}
		updates["name"] = name
	}

	if err := s.repo.UpdateInitiativeFields(ctx, id, updates); err != nil {
		if isUniqueViolation(err) {
			return pm.ErrInitiativeNameDup
		}
		return fmt.Errorf("update initiative: %w: %w", err, pm.ErrPMInternal)
	}
	return nil
}

// Archive 归档 initiative。前置守卫:无未归档 / 未 cancelled / 未 done 的
// workstream(否则用户该先把工作收尾)。is_system 不允许 archive。
func (s *initiativeService) Archive(ctx context.Context, id, actorUserID uint64) error {
	i, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if i.IsSystem {
		return pm.ErrInitiativeSystem
	}
	if i.ArchivedAt != nil {
		return nil
	}
	if _, err := s.requireProjectActive(ctx, i.ProjectID, actorUserID); err != nil {
		return err
	}

	n, err := s.repo.CountActiveWorkstreamsByInitiative(ctx, id)
	if err != nil {
		return fmt.Errorf("count active workstreams: %w: %w", err, pm.ErrPMInternal)
	}
	if n > 0 {
		return pm.ErrInitiativeNotEmpty
	}

	now := time.Now()
	if err := s.repo.UpdateInitiativeFields(ctx, id, map[string]any{
		"archived_at": now,
		"status":      pm.InitiativeStatusCompleted,
	}); err != nil {
		return fmt.Errorf("archive initiative: %w: %w", err, pm.ErrPMInternal)
	}
	s.logger.InfoCtx(ctx, "pm: initiative archived", map[string]any{
		"initiative_id": id, "actor": actorUserID,
	})
	// 查 project.OrgID 用于 event;失败不阻塞 archive(已落库)
	pid := uint64(0)
	if p, err := s.repo.FindProjectByID(ctx, i.ProjectID); err == nil {
		pid = p.OrgID
	}
	publishPMEvent(ctx, s.publisher, s.streamKey, s.logger, map[string]any{
		"event_type":    EventInitiativeArchived,
		"org_id":        strconv.FormatUint(pid, 10),
		"project_id":    strconv.FormatUint(i.ProjectID, 10),
		"initiative_id": strconv.FormatUint(id, 10),
		"actor_user_id": strconv.FormatUint(actorUserID, 10),
	})
	return nil
}

// requireProjectActive 校验 project 存在 / 未归档 / actor 是 org 成员。
// 返回 project 元信息以备调用方使用。
func (s *initiativeService) requireProjectActive(ctx context.Context, projectID, actorUserID uint64) (*model.Project, error) {
	p, err := s.repo.FindProjectByID(ctx, projectID)
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
	return p, nil
}
