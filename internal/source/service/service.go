// service.go source 模块 service 层共享类型与转换工具。
package service

import (
	"context"

	permmodel "github.com/eyrihe999-stack/Synapse/internal/permission/model"
	"github.com/eyrihe999-stack/Synapse/internal/source/dto"
	"github.com/eyrihe999-stack/Synapse/internal/source/model"
)

// ─── 跨模块依赖接口 ───────────────────────────────────────────────────────────

// ACLOps source 模块需要的 ACL 操作子集。main.go 用 permission/repository.Repository
// 做适配器注入(它的 GrantACL/RevokeACL 等签名直接对得上)。
//
// auditAction / auditTarget 由 source 模块传(它持有自己的 source.AuditActionSourceACL* 常量)。
type ACLOps interface {
	GrantACL(ctx context.Context, acl *permmodel.ResourceACL, auditAction, auditTarget string) error
	FindACLByID(ctx context.Context, id uint64) (*permmodel.ResourceACL, error)
	FindACL(ctx context.Context, resourceType string, resourceID uint64, subjectType string, subjectID uint64) (*permmodel.ResourceACL, error)
	ListACLByResource(ctx context.Context, resourceType string, resourceID uint64) ([]*permmodel.ResourceACL, error)
	UpdateACLPermission(ctx context.Context, aclID uint64, newPermission, auditAction, auditTarget string) error
	RevokeACL(ctx context.Context, aclID uint64, auditAction, auditTarget string) error
	// BulkRevokeACLsByResource 删 source 时清理其下所有 ACL 行;返回被删行数,无匹配返 0。
	BulkRevokeACLsByResource(ctx context.Context, resourceType string, resourceID uint64, auditAction, auditTarget string) (int64, error)
}

// DocumentCounter 给 DeleteSource 前置守卫用:统计某 source 下尚存多少 doc。
// main.go 用 document/repository.Repository(方法 CountBySource)做适配器注入。
// 当 pgDB 缺失(doc 子系统未装配)时可注入 nil —— service 层把 nil 视作"没有 doc"放行删除。
type DocumentCounter interface {
	CountBySource(ctx context.Context, orgID, sourceID uint64) (int64, error)
}

// SubjectValidator ACL 授权目标的合法性校验:group 必须存在于该 org;user 必须是 org 成员。
type SubjectValidator interface {
	GroupExistsInOrg(ctx context.Context, orgID, groupID uint64) (bool, error)
	UserIsOrgMember(ctx context.Context, orgID, userID uint64) (bool, error)
}

// VisibleSourceFilter 给 ListSources(scope=visible) 用:返回 user 在 org 内能"读"到的 source id 集合。
//
// 实现由 main.go 用 permission/service.PermissionService 适配注入(它已经有同名方法)。
// 注入 nil → ListSources(scope=visible) 不可用,会降级为 all(打 warn 日志)。
type VisibleSourceFilter interface {
	VisibleSourceIDsInOrg(ctx context.Context, orgID, userID uint64, minPerm string) ([]uint64, error)
}

// ListScope 列表请求的可见范围。
//   - visible(默认):只列 user 能读的 source(owner / org-vis / ACL hit)
//   - all:           列出 org 下所有 source(管理员 / 审计场景用)
type ListScope string

const (
	ListScopeVisible ListScope = "visible"
	ListScopeAll     ListScope = "all"
)

// ParseListScope 把 query string 解析为 ListScope,空 / 非法 → visible(默认)。
func ParseListScope(s string) ListScope {
	if s == string(ListScopeAll) {
		return ListScopeAll
	}
	return ListScopeVisible
}

// ─── model → dto 转换 ────────────────────────────────────────────────────────

// sourceToDTO 把 Source 模型转为 SourceResponse。
func sourceToDTO(s *model.Source) dto.SourceResponse {
	return dto.SourceResponse{
		ID:          s.ID,
		OrgID:       s.OrgID,
		Kind:        s.Kind,
		OwnerUserID: s.OwnerUserID,
		ExternalRef: s.ExternalRef,
		Name:        s.Name,
		Visibility:  s.Visibility,
		CreatedAt:   s.CreatedAt.Unix(),
		UpdatedAt:   s.UpdatedAt.Unix(),
	}
}

// aclToDTO 把 ResourceACL 转为 SourceACLResponse(source 视角的 ACL 行展示)。
func aclToDTO(a *permmodel.ResourceACL) dto.SourceACLResponse {
	return dto.SourceACLResponse{
		ID:          a.ID,
		SourceID:    a.ResourceID,
		SubjectType: a.SubjectType,
		SubjectID:   a.SubjectID,
		Permission:  a.Permission,
		GrantedBy:   a.GrantedBy,
		CreatedAt:   a.CreatedAt.Unix(),
	}
}
