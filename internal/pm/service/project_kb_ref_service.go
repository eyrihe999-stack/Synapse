package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/pm"
	"github.com/eyrihe999-stack/Synapse/internal/pm/model"
	"github.com/eyrihe999-stack/Synapse/internal/pm/repository"
)

// ProjectKBRefService project KB 挂载子领域业务接口。
//
// v0 简单实现:验证 source_id / doc_id 二选一(其中一个非零) + project 存在
// + actor 是 org 成员 → 写入 / 删除。**不验证 source/doc 真实存在和归属** ——
// 这两件事需要跨模块查 source / document repo,引入循环依赖,留给 PR-B 扩展。
//
// 当前用户的责任是传合法的 ID;非法 ID 写进表也只是死数据,不会影响其他逻辑。
type ProjectKBRefService interface {
	Attach(ctx context.Context, projectID, actorUserID, sourceID, docID uint64) (*model.ProjectKBRef, error)
	Detach(ctx context.Context, refID, actorUserID uint64) error
	List(ctx context.Context, projectID uint64) ([]model.ProjectKBRef, error)
}

type projectKBRefService struct {
	repo       repository.Repository
	orgChecker OrgMembershipChecker
	logger     logger.LoggerInterface
}

func newProjectKBRefService(repo repository.Repository, orgChecker OrgMembershipChecker, log logger.LoggerInterface) ProjectKBRefService {
	return &projectKBRefService{repo: repo, orgChecker: orgChecker, logger: log}
}

// Attach 给 project 挂 KB(source 或 doc 二选一)。
func (s *projectKBRefService) Attach(ctx context.Context, projectID, actorUserID, sourceID, docID uint64) (*model.ProjectKBRef, error) {
	// 二选一约束(恰好一个非零)
	if (sourceID == 0 && docID == 0) || (sourceID != 0 && docID != 0) {
		return nil, pm.ErrProjectKBRefInvalid
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

	// 应用层去重(uk_project_kb_refs_uniq 兜底)
	if existing, err := s.repo.FindProjectKBRefByTarget(ctx, projectID, sourceID, docID); err == nil {
		return existing, pm.ErrProjectKBRefDuplicated
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("find project kb ref: %w: %w", err, pm.ErrPMInternal)
	}

	actorPID, err := s.repo.LookupUserPrincipalID(ctx, actorUserID)
	if err != nil {
		return nil, fmt.Errorf("lookup user principal: %w: %w", err, pm.ErrPMInternal)
	}

	ref := &model.ProjectKBRef{
		ProjectID:    projectID,
		KBSourceID:   sourceID,
		KBDocumentID: docID,
		AttachedBy:   actorPID,
		AttachedAt:   time.Now(),
	}
	if err := s.repo.CreateProjectKBRef(ctx, ref); err != nil {
		if isUniqueViolation(err) {
			return nil, pm.ErrProjectKBRefDuplicated
		}
		return nil, fmt.Errorf("create project kb ref: %w: %w", err, pm.ErrPMInternal)
	}
	s.logger.InfoCtx(ctx, "pm: project kb ref attached", map[string]any{
		"ref_id": ref.ID, "project_id": projectID, "source_id": sourceID, "doc_id": docID, "actor": actorUserID,
	})
	return ref, nil
}

// Detach 卸载 KB 挂载。actor 必须是 project 所属 org 成员。
func (s *projectKBRefService) Detach(ctx context.Context, refID, actorUserID uint64) error {
	ref, err := s.repo.FindProjectKBRefByID(ctx, refID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return pm.ErrProjectKBRefNotFound
	}
	if err != nil {
		return fmt.Errorf("find project kb ref: %w: %w", err, pm.ErrPMInternal)
	}
	p, err := s.repo.FindProjectByID(ctx, ref.ProjectID)
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
	if err := s.repo.DeleteProjectKBRef(ctx, refID); err != nil {
		return fmt.Errorf("delete project kb ref: %w: %w", err, pm.ErrPMInternal)
	}
	return nil
}

// List 列 project 下所有 KB 挂载;不做权限校验(handler 层做 org 成员校验)。
func (s *projectKBRefService) List(ctx context.Context, projectID uint64) ([]model.ProjectKBRef, error) {
	return s.repo.ListProjectKBRefsByProject(ctx, projectID)
}
