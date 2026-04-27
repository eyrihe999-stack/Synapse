// Package service agents 模块业务逻辑层。
//
// 两块职责,分两个 struct:
//   - AgentService:HTTP CRUD + rotate(agent_service.go)
//   - DBAuthenticator:transport 握手鉴权(dbauth.go)
//
// 两者共享 Repository,但职责不同;分开测试、分开心智负担。
package service

import (
	"context"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/agents/repository"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
)

// OrgRoleLookup 跨模块读接口:service 需要知道某 user 在某 org 是什么角色,
// 用来判断"是否 owner/admin"。
//
// main.go 用 organization/repository 适配器注入,避免 agents 模块反向 import org 包。
type OrgRoleLookup interface {
	// GetMemberRoleSlug 返回 user 在 org 的系统角色 slug("owner"/"admin"/"member")。
	// 不是成员 → 返 ("", nil),service 按 forbidden 处理。
	// 错误 → 按内部错误处理。
	GetMemberRoleSlug(ctx context.Context, orgID, userID uint64) (string, error)
}

// AgentDisconnector 窄接口:service 在 disable/rotate/delete 时需要踢掉当前在线连接。
// 由 transport/service.LocalHub 实现(Disconnect 方法签名对齐)。
//
// 定义在本地避免 agents 模块直接 import transport/service 包。
type AgentDisconnector interface {
	Disconnect(agentID string, reason string) bool
}

// Config 结构保留位,未来放配置项(如 rotate cooldown、密钥前缀覆盖等)。
// V1 空;加字段时走零值 = 走默认逻辑的约定。
type Config struct{}

// AgentService HTTP CRUD 业务入口。goroutine-safe。
type AgentService struct {
	cfg          Config
	repo         repository.Repository
	roleLookup   OrgRoleLookup
	disconnector AgentDisconnector
	log          logger.LoggerInterface
}

// NewAgentService 构造 AgentService。所有依赖必填。
func NewAgentService(
	cfg Config,
	repo repository.Repository,
	roleLookup OrgRoleLookup,
	disconnector AgentDisconnector,
	log logger.LoggerInterface,
) *AgentService {
	return &AgentService{
		cfg:          cfg,
		repo:         repo,
		roleLookup:   roleLookup,
		disconnector: disconnector,
		log:          log,
	}
}

// nowFunc 时间源。测试可替换;业务代码用 nowFunc() 而不是 time.Now() 以便 mock。
var nowFunc = func() time.Time { return time.Now() }

// 编译期断言:AgentService / DBAuthenticator 被使用的地方有类型。
// 这个变量引用防止 unused 警告(两个 struct 通过 New* 导出,lint 误报)。
var _ = nowFunc
