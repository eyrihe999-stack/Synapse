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

// ProjectService project 子领域业务接口。
//
// Archive 不再级联归档 channel —— 那个职责由 channel 模块自己监听 pm 事件做。
// 当前 v0 直接 UPDATE projects 行;channel 模块如果不监听就保持自己的状态,
// 等 PR-B / Architect agent 接入时再做联动。
type ProjectService interface {
	Create(ctx context.Context, orgID, actorUserID uint64, name, description string) (*model.Project, error)
	Get(ctx context.Context, id uint64) (*model.Project, error)
	List(ctx context.Context, orgID uint64, limit, offset int) ([]model.Project, error)
	Archive(ctx context.Context, id, actorUserID uint64) error
}

type projectService struct {
	repo       repository.Repository
	orgChecker OrgMembershipChecker
	publisher  eventbus.Publisher
	streamKey  string
	logger     logger.LoggerInterface
}

func newProjectService(repo repository.Repository, orgChecker OrgMembershipChecker, publisher eventbus.Publisher, streamKey string, log logger.LoggerInterface) ProjectService {
	return &projectService{repo: repo, orgChecker: orgChecker, publisher: publisher, streamKey: streamKey, logger: log}
}

// publishPMEvent XADD 到 pm 事件流。失败仅 warn(对齐 channel.publishChannelEvent)。
func (s *projectService) publishPMEvent(ctx context.Context, fields map[string]any) {
	if s.publisher == nil || s.streamKey == "" {
		return
	}
	id, err := s.publisher.Publish(ctx, s.streamKey, fields)
	if err != nil {
		s.logger.WarnCtx(ctx, "pm: publish event failed", map[string]any{
			"event_type": fields["event_type"], "err": err.Error(),
		})
		return
	}
	s.logger.DebugCtx(ctx, "pm: published event", map[string]any{
		"event_type": fields["event_type"], "stream_id": id,
	})
}

// Create 在 org 下新建 project。调用者必须是 org 成员。
func (s *projectService) Create(ctx context.Context, orgID, actorUserID uint64, name, description string) (*model.Project, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > pm.ProjectNameMaxLen {
		return nil, pm.ErrProjectNameInvalid
	}
	if len(description) > pm.ProjectDescriptionMaxLen {
		return nil, pm.ErrProjectNameInvalid
	}

	if err := s.requireOrgMember(ctx, orgID, actorUserID); err != nil {
		return nil, err
	}

	// 应用层预检名字冲突 —— 返回友好错误码;DB 层 uk_projects_org_name_active 兜底
	n, err := s.repo.CountActiveProjectByName(ctx, orgID, name)
	if err != nil {
		return nil, fmt.Errorf("count project by name: %w: %w", err, pm.ErrPMInternal)
	}
	if n > 0 {
		return nil, pm.ErrProjectNameDup
	}

	p := &model.Project{
		OrgID:       orgID,
		Name:        name,
		Description: description,
		CreatedBy:   actorUserID,
	}
	if err := s.repo.CreateProject(ctx, p); err != nil {
		// DB 撞 uk_projects_org_name_active(竞态情况)
		if isUniqueViolation(err) {
			return nil, pm.ErrProjectNameDup
		}
		return nil, fmt.Errorf("create project: %w: %w", err, pm.ErrPMInternal)
	}
	// 立即触发 default initiative / Backlog version / Console channel + owner 的 seed,
	// 保证 user 从 HTTP 创建 project 后马上能看到这些 default 资源。失败只 warn 不
	// abort —— 数据落表了,下次重启 RunPostMigrations 也会兜底补上。
	if err := s.repo.SeedProjectDefaults(ctx, p.ID); err != nil {
		s.logger.WarnCtx(ctx, "pm: seed project defaults failed; will be retried at next startup", map[string]any{
			"project_id": p.ID, "err": err.Error(),
		})
	}
	s.logger.InfoCtx(ctx, "pm: project created", map[string]any{
		"project_id": p.ID, "org_id": orgID, "actor": actorUserID,
	})
	// 发 project.created 事件,后续 channel 模块可监听,自动开 Project Console channel
	// (PR-B 范围;v0 阶段事件可能没人消费,但占位先发)
	s.publishPMEvent(ctx, map[string]any{
		"event_type": "project.created",
		"org_id":     strconv.FormatUint(orgID, 10),
		"project_id": strconv.FormatUint(p.ID, 10),
		"actor_user_id": strconv.FormatUint(actorUserID, 10),
	})
	return p, nil
}

// Get 返回 project。调用方负责上游鉴权(handler 层做 org 成员校验)。
func (s *projectService) Get(ctx context.Context, id uint64) (*model.Project, error) {
	p, err := s.repo.FindProjectByID(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, pm.ErrProjectNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find project: %w: %w", err, pm.ErrPMInternal)
	}
	return p, nil
}

// List 列出 org 下所有 project(含归档)。
func (s *projectService) List(ctx context.Context, orgID uint64, limit, offset int) ([]model.Project, error) {
	if limit <= 0 {
		limit = pm.ListDefaultLimit
	}
	if limit > pm.ListMaxLimit {
		limit = pm.ListMaxLimit
	}
	return s.repo.ListProjectsByOrg(ctx, orgID, limit, offset)
}

// Archive 归档 project。调用者必须是 org 成员。已归档幂等返 nil。
//
// 注意:不再像旧 channel.projectService.Archive 那样级联归档 channel —— pm 模块
// 不直接操作 channel 表,channel 模块如有联动需求自己监听 project.archived 事件做。
func (s *projectService) Archive(ctx context.Context, id, actorUserID uint64) error {
	p, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if p.ArchivedAt != nil {
		return nil
	}
	if err := s.requireOrgMember(ctx, p.OrgID, actorUserID); err != nil {
		return err
	}
	now := time.Now()
	if err := s.repo.UpdateProjectFields(ctx, id, map[string]any{
		"archived_at": now,
	}); err != nil {
		return fmt.Errorf("archive project: %w: %w", err, pm.ErrPMInternal)
	}
	s.logger.InfoCtx(ctx, "pm: project archived", map[string]any{
		"project_id": id, "actor": actorUserID,
	})
	// 发 project.archived,channel 模块可监听级联归档下属 channel(PR-B 接入)
	actorPID, _ := s.repo.LookupUserPrincipalID(ctx, actorUserID) //sayso-lint:ignore err-swallow
	s.publishPMEvent(ctx, map[string]any{
		"event_type":         "project.archived",
		"org_id":             strconv.FormatUint(p.OrgID, 10),
		"project_id":         strconv.FormatUint(id, 10),
		"actor_principal_id": strconv.FormatUint(actorPID, 10),
	})
	return nil
}

// requireOrgMember 校验 actor 是 org 成员;否则返 ErrForbidden。
func (s *projectService) requireOrgMember(ctx context.Context, orgID, actorUserID uint64) error {
	ok, err := s.orgChecker.IsMember(ctx, orgID, actorUserID)
	if err != nil {
		return fmt.Errorf("check org membership: %w: %w", err, pm.ErrPMInternal)
	}
	if !ok {
		return pm.ErrForbidden
	}
	return nil
}
