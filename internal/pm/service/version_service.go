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

// VersionService version 子领域业务接口。
type VersionService interface {
	// targetDate 为 nil 表示未指定目标日期(model.TargetDate 留 NULL,后续 PATCH 可补)。
	Create(ctx context.Context, projectID, actorUserID uint64, name, status string, targetDate *time.Time) (*model.Version, error)
	Get(ctx context.Context, id uint64) (*model.Version, error)
	List(ctx context.Context, projectID uint64) ([]model.Version, error)
	Update(ctx context.Context, id, actorUserID uint64, updates map[string]any) error
}

type versionService struct {
	repo       repository.Repository
	orgChecker OrgMembershipChecker
	publisher  eventbus.Publisher
	streamKey  string
	logger     logger.LoggerInterface
}

func newVersionService(repo repository.Repository, orgChecker OrgMembershipChecker, publisher eventbus.Publisher, streamKey string, log logger.LoggerInterface) VersionService {
	return &versionService{repo: repo, orgChecker: orgChecker, publisher: publisher, streamKey: streamKey, logger: log}
}

// Create 在 project 下新建 version。
//
// 调用者必须是 project 所属 org 成员。is_system version(Backlog)由 migration
// 阶段建,不允许人工手动建同名;名字撞 UNIQUE 走 ErrVersionNameDup 友好返回。
func (s *versionService) Create(ctx context.Context, projectID, actorUserID uint64, name, status string, targetDate *time.Time) (*model.Version, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > pm.VersionNameMaxLen {
		return nil, pm.ErrVersionNameInvalid
	}
	if !pm.IsValidVersionStatus(status) {
		return nil, pm.ErrVersionStatusInvalid
	}

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

	// 应用层预检名字重复(uk_versions_project_name 兜底)
	n, err := s.repo.CountActiveVersionByName(ctx, projectID, name)
	if err != nil {
		return nil, fmt.Errorf("count version by name: %w: %w", err, pm.ErrPMInternal)
	}
	if n > 0 {
		return nil, pm.ErrVersionNameDup
	}

	v := &model.Version{
		ProjectID:  projectID,
		Name:       name,
		Status:     status,
		TargetDate: targetDate,
		CreatedBy:  actorUserID,
	}
	if err := s.repo.CreateVersion(ctx, v); err != nil {
		if isUniqueViolation(err) {
			return nil, pm.ErrVersionNameDup
		}
		return nil, fmt.Errorf("create version: %w: %w", err, pm.ErrPMInternal)
	}
	s.logger.InfoCtx(ctx, "pm: version created", map[string]any{
		"version_id": v.ID, "project_id": projectID, "actor": actorUserID,
	})
	publishPMEvent(ctx, s.publisher, s.streamKey, s.logger, map[string]any{
		"event_type":    EventVersionCreated,
		"org_id":        strconv.FormatUint(p.OrgID, 10),
		"project_id":    strconv.FormatUint(projectID, 10),
		"version_id":    strconv.FormatUint(v.ID, 10),
		"actor_user_id": strconv.FormatUint(actorUserID, 10),
	})
	return v, nil
}

// Get 返回 version。
func (s *versionService) Get(ctx context.Context, id uint64) (*model.Version, error) {
	v, err := s.repo.FindVersionByID(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, pm.ErrVersionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find version: %w: %w", err, pm.ErrPMInternal)
	}
	return v, nil
}

func (s *versionService) List(ctx context.Context, projectID uint64) ([]model.Version, error) {
	return s.repo.ListVersionsByProject(ctx, projectID)
}

// Update 改 version 字段。is_system version 不允许改名 / 删 / 任何 status 转换;
// 当前 v0 只支持 status / target_date / released_at 三个字段的更新。
func (s *versionService) Update(ctx context.Context, id, actorUserID uint64, updates map[string]any) error {
	v, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if v.IsSystem {
		return pm.ErrVersionSystem
	}
	p, err := s.repo.FindProjectByID(ctx, v.ProjectID)
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
	if status, ok := updates["status"].(string); ok {
		if !pm.IsValidVersionStatus(status) {
			return pm.ErrVersionStatusInvalid
		}
	}
	if err := s.repo.UpdateVersionFields(ctx, id, updates); err != nil {
		return fmt.Errorf("update version: %w: %w", err, pm.ErrPMInternal)
	}
	eventType := EventVersionUpdated
	if status, ok := updates["status"].(string); ok && status == pm.VersionStatusReleased {
		eventType = EventVersionReleased
	}
	publishPMEvent(ctx, s.publisher, s.streamKey, s.logger, map[string]any{
		"event_type":    eventType,
		"org_id":        strconv.FormatUint(p.OrgID, 10),
		"project_id":    strconv.FormatUint(v.ProjectID, 10),
		"version_id":    strconv.FormatUint(id, 10),
		"actor_user_id": strconv.FormatUint(actorUserID, 10),
	})
	return nil
}
