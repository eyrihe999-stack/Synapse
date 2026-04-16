// registry_service.go Agent + Method + Secret 服务的接口定义、构造器与共享校验工具。
//
// 业务实现按职责拆分到:
//   - registry_agent_service.go  : Agent CRUD
//   - registry_method_service.go : Method CRUD
//   - registry_secret_service.go : Secret / Health
//
// 不涉及调用转发、发布关系、健康检查调度。
package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/eyrihe999-stack/Synapse/internal/agent"
	"github.com/eyrihe999-stack/Synapse/internal/agent/dto"
	"github.com/eyrihe999-stack/Synapse/internal/agent/model"
	"github.com/eyrihe999-stack/Synapse/internal/agent/repository"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"gorm.io/gorm"
)

// RegistryService 提供 agent CRUD + method 管理 + secret 操作的组合接口。
//sayso-lint:ignore interface-pollution
type RegistryService interface {
	// ─── Agent ────────────────────────────────────────────────────────────

	// CreateAgent 注册 agent,事务内写 agent + methods + secret。
	// 返回一次性明文 secret。
	CreateAgent(ctx context.Context, userID uint64, req dto.CreateAgentRequest) (*dto.CreateAgentResponse, error)
	// GetAgentByID 按 ID 查 agent(作者视角完整信息)。
	GetAgentByID(ctx context.Context, agentID, requesterUserID uint64) (*dto.AgentResponse, error)
	// ListMyAgents 列出调用者拥有的所有 agent。
	ListMyAgents(ctx context.Context, userID uint64) ([]dto.AgentResponse, error)
	// UpdateAgent 部分更新 agent 元信息。
	UpdateAgent(ctx context.Context, agentID, requesterUserID uint64, req dto.UpdateAgentRequest) (*dto.AgentResponse, error)
	// DeleteAgent 删除 agent(连带 methods/secret/publish)。
	DeleteAgent(ctx context.Context, agentID, requesterUserID uint64) error

	// ─── Method ───────────────────────────────────────────────────────────

	// ListMethods 列出某 agent 的所有 method。
	ListMethods(ctx context.Context, agentID, requesterUserID uint64) ([]dto.MethodResponse, error)
	// CreateMethod 追加一条 method。
	CreateMethod(ctx context.Context, agentID, requesterUserID uint64, req dto.CreateMethodRequest) (*dto.MethodResponse, error)
	// UpdateMethod 部分更新 method。
	UpdateMethod(ctx context.Context, agentID, methodID, requesterUserID uint64, req dto.UpdateMethodRequest) (*dto.MethodResponse, error)
	// DeleteMethod 删除 method(保留至少 1 条)。
	DeleteMethod(ctx context.Context, agentID, methodID, requesterUserID uint64) error

	// ─── Secret ───────────────────────────────────────────────────────────

	// RotateSecret 生成新 secret,保留旧 secret 24 小时。
	RotateSecret(ctx context.Context, agentID, requesterUserID uint64) (*dto.RotateSecretResponse, error)

	// ─── Health(作者查看) ───────────────────────────────────────────────

	// GetHealth 返回 agent 当前健康状态快照。
	GetHealth(ctx context.Context, agentID, requesterUserID uint64) (*dto.HealthResponse, error)

	// ─── 内部辅助(供 gateway 使用) ───────────────────────────────────────

	// LoadAgentModel 按 ID 加载原始 model.Agent(网关调用时用)。
	LoadAgentModel(ctx context.Context, agentID uint64) (*model.Agent, error)
	// LoadAgentByOwnerSlug 按 (owner_uid, agent_slug) 加载原始 model.Agent(网关调用时用)。
	LoadAgentByOwnerSlug(ctx context.Context, ownerUID uint64, slug string) (*model.Agent, error)
	// LoadMethod 按 (agent_id, method_name) 加载 method model。
	LoadMethod(ctx context.Context, agentID uint64, methodName string) (*model.AgentMethod, error)
	// LoadActiveSecret 读取并解密当前 secret(网关转发签名时用)。
	// 若存在 previous 且未过期,也可由调用方自行判断取哪个。
	LoadActiveSecret(ctx context.Context, agentID uint64) (plaintext string, previousPlaintext string, err error)
}

// ─── 实现 ────────────────────────────────────────────────────────────────────

type registryService struct {
	repo      repository.Repository
	masterKey *agent.MasterKey
	logger    logger.LoggerInterface
}

// NewRegistryService 构造 RegistryService。masterKey 为 AES-GCM 加密用。
func NewRegistryService(repo repository.Repository, masterKey *agent.MasterKey, log logger.LoggerInterface) RegistryService {
	return &registryService{repo: repo, masterKey: masterKey, logger: log}
}

var (
	agentSlugRegexp  = regexp.MustCompile(agent.AgentSlugPattern)
	methodNameRegexp = regexp.MustCompile(agent.MethodNamePattern)
)

// loadAgentOwned 加载 agent 并校验作者身份。
func (s *registryService) loadAgentOwned(ctx context.Context, agentID, requesterUserID uint64) (*model.Agent, error) {
	a, err := s.repo.FindAgentByID(ctx, agentID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			//sayso-lint:ignore log-coverage
			return nil, fmt.Errorf("agent not found: %w", agent.ErrAgentNotFound)
		}
		s.logger.ErrorCtx(ctx, "find agent failed", err, map[string]any{"agent_id": agentID})
		return nil, fmt.Errorf("find agent: %w: %w", err, agent.ErrAgentInternal)
	}
	if a.OwnerUserID != requesterUserID {
		s.logger.WarnCtx(ctx, "not agent author", map[string]any{"agent_id": agentID, "user_id": requesterUserID})
		return nil, fmt.Errorf("not author: %w", agent.ErrAgentNotAuthor)
	}
	return a, nil
}

// ─── 校验工具 ───────────────────────────────────────────────────────────────

func validateAgentSlug(slug string) error {
	if !agentSlugRegexp.MatchString(slug) {
		//sayso-lint:ignore log-coverage
		return fmt.Errorf("slug invalid: %w", agent.ErrAgentSlugInvalid)
	}
	return nil
}

func validateEndpoint(u string) error {
	if u == "" || !strings.HasPrefix(u, "https://") {
		//sayso-lint:ignore log-coverage
		return fmt.Errorf("endpoint must be https: %w", agent.ErrAgentEndpointInvalid)
	}
	return nil
}

func validateTransport(t string) error {
	switch t {
	case agent.TransportHTTP, agent.TransportSSE:
		return nil
	case agent.TransportWS:
		//sayso-lint:ignore log-coverage
		return fmt.Errorf("ws unsupported: %w", agent.ErrMethodTransportUnsupported)
	default:
		//sayso-lint:ignore log-coverage
		return fmt.Errorf("transport unsupported: %w", agent.ErrMethodTransportUnsupported)
	}
}

func validateVisibility(v string) error {
	switch v {
	case agent.VisibilityPublic, agent.VisibilityPrivate:
		return nil
	default:
		//sayso-lint:ignore log-coverage
		return fmt.Errorf("visibility invalid: %w", agent.ErrMethodVisibilityInvalid)
	}
}

// normalizeLimits 把 0/负值替换为默认值,并校验上限。
func normalizeLimits(timeoutSec, ratePerMin, maxConcur int) (int, int, int, error) {
	t := timeoutSec
	if t <= 0 {
		t = agent.DefaultTimeoutSeconds
	}
	if t > agent.MaxTimeoutSeconds {
		//sayso-lint:ignore log-coverage
		return 0, 0, 0, fmt.Errorf("timeout too large: %w", agent.ErrAgentTimeoutOutOfRange)
	}
	r := ratePerMin
	if r <= 0 {
		r = agent.DefaultRateLimitPerMinute
	}
	if r > agent.MaxRateLimitPerMinute {
		//sayso-lint:ignore log-coverage
		return 0, 0, 0, fmt.Errorf("rate too large: %w", agent.ErrAgentRateLimitOutOfRange)
	}
	c := maxConcur
	if c <= 0 {
		c = agent.DefaultMaxConcurrent
	}
	if c > agent.MaxMaxConcurrent {
		//sayso-lint:ignore log-coverage
		return 0, 0, 0, fmt.Errorf("concurrent too large: %w", agent.ErrAgentConcurrentOutOfRange)
	}
	return t, r, c, nil
}

// buildMethodModel 按请求构造 model.AgentMethod,并校验 transport/visibility/name。
func buildMethodModel(req dto.CreateMethodRequest) (*model.AgentMethod, error) {
	if !methodNameRegexp.MatchString(req.MethodName) {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("method name invalid: %w", agent.ErrMethodNameInvalid)
	}
	if req.DisplayName == "" || len(req.DisplayName) > agent.MaxMethodDisplayNameLength {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("method display name invalid: %w", agent.ErrAgentInvalidRequest)
	}
	if len(req.Description) > agent.MaxMethodDescriptionLength {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("method description too long: %w", agent.ErrAgentInvalidRequest)
	}
	transport := req.Transport
	if transport == "" {
		transport = agent.TransportHTTP
	}
	if err := validateTransport(transport); err != nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, err
	}
	visibility := req.Visibility
	if visibility == "" {
		visibility = agent.VisibilityPublic
	}
	if err := validateVisibility(visibility); err != nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, err
	}
	return &model.AgentMethod{
		MethodName:  req.MethodName,
		DisplayName: req.DisplayName,
		Description: req.Description,
		Transport:   transport,
		Visibility:  visibility,
	}, nil
}
