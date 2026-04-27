package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	chanerr "github.com/eyrihe999-stack/Synapse/internal/channel"
	"github.com/eyrihe999-stack/Synapse/internal/channel/model"
	"github.com/eyrihe999-stack/Synapse/internal/channel/repository"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
)

// VersionService version 子领域业务接口。
type VersionService interface {
	Create(ctx context.Context, projectID, actorUserID uint64, name, status string) (*model.Version, error)
	List(ctx context.Context, projectID uint64) ([]model.Version, error)
}

type versionService struct {
	repo       repository.Repository
	orgChecker OrgMembershipChecker
	logger     logger.LoggerInterface
}

func newVersionService(repo repository.Repository, orgChecker OrgMembershipChecker, log logger.LoggerInterface) VersionService {
	return &versionService{repo: repo, orgChecker: orgChecker, logger: log}
}

// Create 在 project 下新建 version。调用者必须是 project 所属 org 成员。
func (s *versionService) Create(ctx context.Context, projectID, actorUserID uint64, name, status string) (*model.Version, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > chanerr.VersionNameMaxLen {
		return nil, chanerr.ErrVersionNameInvalid
	}
	if !isValidVersionStatus(status) {
		return nil, chanerr.ErrVersionStatusInvalid
	}

	p, err := s.repo.FindProjectByID(ctx, projectID)
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

	v := &model.Version{
		ProjectID: projectID,
		Name:      name,
		Status:    status,
	}
	if err := s.repo.CreateVersion(ctx, v); err != nil {
		// DB 撞 uk_versions_project_name → 名字重复
		if isUniqueViolation(err) {
			return nil, chanerr.ErrVersionNameDup
		}
		return nil, fmt.Errorf("create version: %w: %w", err, chanerr.ErrChannelInternal)
	}
	s.logger.InfoCtx(ctx, "channel: version created", map[string]any{
		"version_id": v.ID, "project_id": projectID, "actor": actorUserID,
	})
	return v, nil
}

func (s *versionService) List(ctx context.Context, projectID uint64) ([]model.Version, error) {
	return s.repo.ListVersionsByProject(ctx, projectID)
}

func isValidVersionStatus(s string) bool {
	switch s {
	case chanerr.VersionStatusPlanned, chanerr.VersionStatusInProgress,
		chanerr.VersionStatusReleased, chanerr.VersionStatusCancelled:
		return true
	}
	return false
}
