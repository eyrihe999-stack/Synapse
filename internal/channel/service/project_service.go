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

// ProjectService project 子领域业务接口。
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

// publishChannelEvent 同其它 service,XADD 到 synapse:channel:events,失败只 warn。
func (s *projectService) publishChannelEvent(ctx context.Context, fields map[string]any) {
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

// Create 在 org 下新建 project。调用者必须是 org 成员。
func (s *projectService) Create(ctx context.Context, orgID, actorUserID uint64, name, description string) (*model.Project, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > chanerr.NameMaxLen {
		return nil, chanerr.ErrProjectNameInvalid
	}
	if len(description) > chanerr.DescriptionMaxLen {
		return nil, chanerr.ErrProjectNameInvalid
	}

	if err := s.requireOrgMember(ctx, orgID, actorUserID); err != nil {
		return nil, err
	}

	// 应用层预检名字冲突 —— 返回友好错误码;DB 层 uk_projects_org_name_active 兜底
	n, err := s.repo.CountActiveProjectByName(ctx, orgID, name)
	if err != nil {
		return nil, fmt.Errorf("count project by name: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if n > 0 {
		return nil, chanerr.ErrProjectNameDup
	}

	p := &model.Project{
		OrgID:       orgID,
		Name:        name,
		Description: description,
		CreatedBy:   actorUserID,
	}
	if err := s.repo.CreateProject(ctx, p); err != nil {
		return nil, fmt.Errorf("create project: %w: %w", err, chanerr.ErrChannelInternal)
	}
	s.logger.InfoCtx(ctx, "channel: project created", map[string]any{
		"project_id": p.ID, "org_id": orgID, "actor": actorUserID,
	})
	return p, nil
}

// Get 返回 project。调用方负责上游鉴权。
func (s *projectService) Get(ctx context.Context, id uint64) (*model.Project, error) {
	p, err := s.repo.FindProjectByID(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, chanerr.ErrProjectNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find project: %w: %w", err, chanerr.ErrChannelInternal)
	}
	return p, nil
}

// List 列出 org 下所有 project(含归档)。
func (s *projectService) List(ctx context.Context, orgID uint64, limit, offset int) ([]model.Project, error) {
	if limit <= 0 {
		limit = chanerr.ListDefaultLimit
	}
	if limit > chanerr.ListMaxLimit {
		limit = chanerr.ListMaxLimit
	}
	return s.repo.ListProjectsByOrg(ctx, orgID, limit, offset)
}

// Archive 归档 project。调用者必须是 org 成员。已归档幂等返 nil。
//
// 级联:把该 project 下所有 status='open' 的 channel 同步置为 archived。之前只改
// project 自身会导致语义裂开("项目已归档但里面 channel 还能收消息")。事务内原子
// UPDATE,失败整体回滚。已归档的 channel 不动。
//
// 级联成功后对每个被归档的 channel 发一条 `channel.archived` 事件(给 PR #11'
// 的 system_event consumer 生成卡片消息用)。
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
	// actor principal_id 用于 publish 的 actor 字段
	actorPID, err := s.repo.LookupUserPrincipalID(ctx, actorUserID)
	if err != nil {
		return fmt.Errorf("lookup actor principal: %w: %w", err, chanerr.ErrChannelInternal)
	}
	// 先查将被级联归档的 channel id 列表(status=open)—— 事务外 best-effort 查,
	// 给发布事件用;如果查失败退化成"事件空",业务本身继续走,不阻塞归档。
	openChannels, _ := s.repo.ListChannelsByProject(ctx, id, 1000, 0) //sayso-lint:ignore err-swallow
	var cascadedIDs []uint64
	for _, c := range openChannels {
		if c.Status == chanerr.ChannelStatusOpen {
			cascadedIDs = append(cascadedIDs, c.ID)
		}
	}

	now := time.Now()
	var cascadedCount int64
	err = s.repo.WithTx(ctx, func(tx repository.Repository) error {
		if err := tx.UpdateProjectFields(ctx, id, map[string]any{
			"archived_at": now,
		}); err != nil {
			return err
		}
		n, err := tx.ArchiveOpenChannelsByProject(ctx, id, now)
		if err != nil {
			return err
		}
		cascadedCount = n
		return nil
	})
	if err != nil {
		return fmt.Errorf("archive project: %w: %w", err, chanerr.ErrChannelInternal)
	}
	s.logger.InfoCtx(ctx, "channel: project archived", map[string]any{
		"project_id": id, "actor": actorUserID, "cascaded_channel_count": cascadedCount,
	})
	// 对每个被级联的 channel 发 channel.archived 事件,actor 填"操作归档 project 的 user 的 principal"
	for _, cid := range cascadedIDs {
		s.publishChannelEvent(ctx, map[string]any{
			"event_type":         "channel.archived",
			"org_id":             strconv.FormatUint(p.OrgID, 10),
			"channel_id":         strconv.FormatUint(cid, 10),
			"actor_principal_id": strconv.FormatUint(actorPID, 10),
			"cascaded_from_project_id": strconv.FormatUint(id, 10),
		})
	}
	return nil
}

// requireOrgMember 校验 actor 是 org 成员;否则返 ErrForbidden。
func (s *projectService) requireOrgMember(ctx context.Context, orgID, actorUserID uint64) error {
	ok, err := s.orgChecker.IsMember(ctx, orgID, actorUserID)
	if err != nil {
		return fmt.Errorf("check org membership: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if !ok {
		return chanerr.ErrForbidden
	}
	return nil
}
