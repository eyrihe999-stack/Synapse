// audit_service.go 审计查询 service(M6)。
//
// 视图分级:
//   - 拥有 audit.read_all perm 的(默认 owner+admin)→ 看全 org,可按 actor / target / action 过滤
//   - 普通成员 → 强制 actor_user_id = self,过滤 caller 提供的 actor 参数被忽略
//
// 这样不需要在路由上加 RequirePerm,所有 org 成员都能访问端点,看到的范围由 service 决定。
package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/permission"
	"github.com/eyrihe999-stack/Synapse/internal/permission/dto"
	"github.com/eyrihe999-stack/Synapse/internal/permission/model"
	"github.com/eyrihe999-stack/Synapse/internal/permission/repository"
)

// AuditQueryFilter handler 透传给 service 的过滤参数(handler 已经把 query string 解析掉)。
type AuditQueryFilter struct {
	ActorUserID  uint64
	TargetType   string
	TargetID     uint64
	Action       string
	ActionPrefix string
	BeforeID     uint64
	Limit        int
}

// AuditScope 视图作用域,塞 ListAuditLogResponse.Scope 给前端展示。
const (
	AuditScopeAll  = "all"  // 看全 org
	AuditScopeSelf = "self" // 只看自己作为 actor 的事件
)

// AuditQueryService 审计查询服务。
//
//sayso-lint:ignore interface-pollution
type AuditQueryService interface {
	// ListAuditLog 列某 org 的审计日志,根据 caller 是否有 audit.read_all 自动选择 scope。
	ListAuditLog(ctx context.Context, orgID, callerUserID uint64, callerPerms []string, filter AuditQueryFilter) (*dto.ListAuditLogResponse, error)
}

// auditQueryService 默认实现。
type auditQueryService struct {
	repo   repository.Repository
	logger logger.LoggerInterface
}

// NewAuditQueryService 构造一个 AuditQueryService 实例。
func NewAuditQueryService(repo repository.Repository, log logger.LoggerInterface) AuditQueryService {
	return &auditQueryService{repo: repo, logger: log}
}

// ListAuditLog 见接口注释。
func (s *auditQueryService) ListAuditLog(ctx context.Context, orgID, callerUserID uint64, callerPerms []string, filter AuditQueryFilter) (*dto.ListAuditLogResponse, error) {
	// 决定 scope
	hasReadAll := false
	for _, p := range callerPerms {
		if p == permission.PermAuditReadAll {
			hasReadAll = true
			break
		}
	}

	repoFilter := repository.AuditFilter{
		TargetType:   filter.TargetType,
		TargetID:     filter.TargetID,
		Action:       filter.Action,
		ActionPrefix: filter.ActionPrefix,
		BeforeID:     filter.BeforeID,
		Limit:        filter.Limit,
	}
	scope := AuditScopeAll
	if !hasReadAll {
		// 普通成员:强制 actor=self,忽略外部 actor 参数
		repoFilter.ActorUserID = callerUserID
		scope = AuditScopeSelf
	} else {
		repoFilter.ActorUserID = filter.ActorUserID
	}

	rows, hasMore, err := s.repo.ListAuditLogByOrg(ctx, orgID, repoFilter)
	if err != nil {
		s.logger.ErrorCtx(ctx, "查审计日志失败", err, map[string]any{"org_id": orgID, "scope": scope})
		return nil, fmt.Errorf("list audit log: %w: %w", err, permission.ErrPermInternal)
	}

	out := make([]dto.AuditLogRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, auditRowToDTO(r))
	}

	resp := &dto.ListAuditLogResponse{
		Items: out,
		Scope: scope,
	}
	if hasMore && len(out) > 0 {
		resp.NextBeforeID = out[len(out)-1].ID
	}
	return resp, nil
}

// auditRowToDTO 转 PermissionAuditLog → AuditLogRow。before/after/metadata 直接透传 jsonb。
func auditRowToDTO(r *model.PermissionAuditLog) dto.AuditLogRow {
	return dto.AuditLogRow{
		ID:          r.ID,
		OrgID:       r.OrgID,
		ActorUserID: r.ActorUserID,
		Action:      r.Action,
		TargetType:  r.TargetType,
		TargetID:    r.TargetID,
		Before:      json.RawMessage(r.Before),
		After:       json.RawMessage(r.After),
		Metadata:    json.RawMessage(r.Metadata),
		CreatedAt:   r.CreatedAt.Unix(),
	}
}
