// Package service pm 模块业务层。
//
// 拆分:每个 entity 一个 Service interface + struct 实现(project / initiative /
// version / workstream / project_kb_ref)。顶层 Service 聚合所有子 service,
// 便于 wiring 层一次性注入。
//
// 权限模型:操作分两层 ——
//  1. **Org 归属**:调用者必须是 project 所属 org 的成员。通过 OrgMembershipChecker
//     校验(由 wiring 注入,实际调 organization.OrgService.IsMember)
//  2. **角色**:某些治理动作(创建/改/归档 initiative、改 version)需要 owner / admin
//     —— v0 阶段简化为"只要是 org 成员就能做",后续 PR 引入 project role 字段后细化
package service

import (
	"context"

	"github.com/eyrihe999-stack/Synapse/internal/common/eventbus"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/pm/repository"
)

// OrgMembershipChecker 由 wiring 层注入,实际调 organization.OrgService.IsMember。
// 语义:userID 是否是 orgID 的活跃成员。
//
// 接口和 channel/service.OrgMembershipChecker 同 shape;wiring 层可以共用同一个实例,
// 通过 OrgMembershipCheckerFunc 适配传入两边。
type OrgMembershipChecker interface {
	IsMember(ctx context.Context, orgID, userID uint64) (bool, error)
}

// OrgMembershipCheckerFunc 函数适配器,便于 main.go 直接注入 orgService.IsMember。
type OrgMembershipCheckerFunc func(ctx context.Context, orgID, userID uint64) (bool, error)

// IsMember 实现 OrgMembershipChecker。
func (f OrgMembershipCheckerFunc) IsMember(ctx context.Context, orgID, userID uint64) (bool, error) {
	return f(ctx, orgID, userID)
}

// Service 聚合 pm 模块所有业务接口。
type Service struct {
	Project      ProjectService
	Initiative   InitiativeService
	Version      VersionService
	Workstream   WorkstreamService
	ProjectKBRef ProjectKBRefService
}

// Config 构造 Service 所需的配置项。
//
// PMEventStream 事件总线 stream key,空串 = 不发事件。当前 v0 不发事件,留作占位。
type Config struct {
	PMEventStream string
}

// New 构造 Service。依赖:
//   - repo:pm repository
//   - orgChecker:org 成员关系查询器(包装 orgSvc.IsMember)
//   - publisher:eventbus 发布器(可 nil,nil 时所有 publish 调用降级 warn 不报错)
//   - log:日志接口
func New(
	cfg Config,
	repo repository.Repository,
	orgChecker OrgMembershipChecker,
	publisher eventbus.Publisher,
	log logger.LoggerInterface,
) *Service {
	return &Service{
		Project:      newProjectService(repo, orgChecker, publisher, cfg.PMEventStream, log),
		Initiative:   newInitiativeService(repo, orgChecker, publisher, cfg.PMEventStream, log),
		Version:      newVersionService(repo, orgChecker, publisher, cfg.PMEventStream, log),
		Workstream:   newWorkstreamService(repo, orgChecker, publisher, cfg.PMEventStream, log),
		ProjectKBRef: newProjectKBRefService(repo, orgChecker, log),
	}
}
