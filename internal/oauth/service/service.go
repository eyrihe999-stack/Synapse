package service

import (
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/oauth/repository"
)

// Config 构造 Service 所需配置项(当前为空占位;未来加 token TTL / issuer 时扩)。
type Config struct{}

// Service 聚合 OAuth 模块所有业务接口。
type Service struct {
	Client        ClientService
	Authorization AuthorizationService
	PAT           PATService
}

// New 构造 Service。agentBootstrapper 可 nil,nil 时 PAT.Create 会拒绝(PR #5'-9
// 未接入时可降级;生产必传)。
func New(cfg Config, repo repository.Repository, agentBootstrapper AgentBootstrapper, log logger.LoggerInterface) *Service {
	clientSvc := newClientService(repo, log)
	return &Service{
		Client:        clientSvc,
		Authorization: newAuthorizationService(repo, clientSvc, log),
		PAT:           newPATService(repo, agentBootstrapper, log),
	}
}
