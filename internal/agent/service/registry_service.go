// registry_service.go Agent CRUD + token 管理。
package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/eyrihe999-stack/Synapse/internal/agent"
	"github.com/eyrihe999-stack/Synapse/internal/agent/dto"
	"github.com/eyrihe999-stack/Synapse/internal/agent/model"
	"github.com/eyrihe999-stack/Synapse/internal/agent/repository"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"gorm.io/gorm"
)

// RegistryService 管理 agent 注册、更新、删除。
type RegistryService interface {
	CreateAgent(ctx context.Context, userID uint64, req dto.CreateAgentRequest) (*dto.AgentResponse, error)
	GetAgentByID(ctx context.Context, agentID, requesterUserID uint64) (*dto.AgentResponse, error)
	ListMyAgents(ctx context.Context, userID uint64) ([]dto.AgentResponse, error)
	UpdateAgent(ctx context.Context, agentID, requesterUserID uint64, req dto.UpdateAgentRequest) (*dto.AgentResponse, error)
	DeleteAgent(ctx context.Context, agentID, requesterUserID uint64) error
	// Internal: 供 ChatService 使用
	LoadAgentByOwnerSlug(ctx context.Context, ownerUID uint64, slug string) (*model.Agent, error)
	DecryptAuthToken(ctx context.Context, a *model.Agent) (string, error)
}

type registryService struct {
	cfg       Config
	repo      repository.Repository
	masterKey *agent.MasterKey
	logger    logger.LoggerInterface
}

// NewRegistryService 创建 RegistryService 实例，注入配置、仓库、主密钥和日志依赖。
// cfg.AllowPrivateEndpoints 控制 endpoint URL 校验是否放行 RFC1918 私网地址。
func NewRegistryService(cfg Config, repo repository.Repository, masterKey *agent.MasterKey, log logger.LoggerInterface) RegistryService {
	return &registryService{cfg: cfg, repo: repo, masterKey: masterKey, logger: log}
}

var slugRe = regexp.MustCompile(agent.AgentSlugPattern)

// validateDataSources 校验 data_sources。knowledge 类型已撤,此字段当前在任何 agent 上都必须为空。
func validateDataSources(agentType string, sources []string) error {
	if len(sources) > 0 {
		return fmt.Errorf("data_sources not supported, got type=%s: %w", agentType, agent.ErrAgentInvalidRequest)
	}
	return nil
}

// CreateAgent 创建新 agent，校验 slug / endpoint / display_name 等字段合法性后写入数据库。
// 可能返回 ErrAgentSlugInvalid、ErrAgentEndpointInvalid、ErrAgentSlugTaken、ErrAgentInternal 等错误。
//
//sayso-lint:ignore sentinel-wrap
func (s *registryService) CreateAgent(ctx context.Context, userID uint64, req dto.CreateAgentRequest) (*dto.AgentResponse, error) {
	// 校验 agent_type（默认 chat）
	agentType := req.AgentType
	if agentType == "" {
		agentType = agent.AgentTypeChat
	}
	if _, ok := agent.ValidAgentTypes[agentType]; !ok {
		s.logger.WarnCtx(ctx, "unsupported agent type", map[string]any{"agent_type": agentType})
		return nil, fmt.Errorf("unsupported type: %w", agent.ErrAgentTypeUnsupported)
	}
	// 版本号（默认 0.1.0）
	version := req.Version
	if version == "" {
		version = "0.1.0"
	}
	// 校验 slug
	if !slugRe.MatchString(req.Slug) {
		s.logger.WarnCtx(ctx, "invalid agent slug", map[string]any{"slug": req.Slug})
		return nil, fmt.Errorf("invalid slug: %w", agent.ErrAgentSlugInvalid)
	}
	// 校验 endpoint(含 SSRF 防护:拦 loopback / link-local / 配置决定是否拦私网)
	if err := agent.ValidateEndpointURL(req.EndpointURL, s.cfg.AllowPrivateEndpoints); err != nil {
		s.logger.WarnCtx(ctx, "invalid agent endpoint", map[string]any{"endpoint": req.EndpointURL, "error": err.Error()})
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	// 校验 display_name
	if req.DisplayName == "" || len(req.DisplayName) > agent.MaxAgentDisplayNameLength {
		s.logger.WarnCtx(ctx, "invalid agent display name", map[string]any{"display_name": req.DisplayName})
		return nil, fmt.Errorf("invalid display name: %w", agent.ErrAgentDisplayNameInvalid)
	}
	// context_mode 默认 stateless
	contextMode := req.ContextMode
	if contextMode == "" {
		contextMode = agent.ContextModeStateless
	}
	if contextMode != agent.ContextModeStateless && contextMode != agent.ContextModeStateful {
		s.logger.WarnCtx(ctx, "invalid agent context mode", map[string]any{"context_mode": contextMode})
		return nil, fmt.Errorf("invalid context mode: %w", agent.ErrAgentContextModeInvalid)
	}
	// max_context_rounds
	maxRounds := req.MaxContextRounds
	if maxRounds == 0 {
		maxRounds = agent.DefaultMaxContextRounds
	}
	if maxRounds < agent.MinMaxContextRounds || maxRounds > agent.MaxMaxContextRounds {
		s.logger.WarnCtx(ctx, "agent max context rounds out of range", map[string]any{"max_context_rounds": maxRounds})
		return nil, fmt.Errorf("max context rounds out of range: %w", agent.ErrAgentMaxRoundsOutOfRange)
	}
	// timeout
	timeout := req.TimeoutSeconds
	if timeout == 0 {
		timeout = agent.DefaultTimeoutSeconds
	}
	if timeout < agent.MinTimeoutSeconds || timeout > agent.MaxTimeoutSeconds {
		s.logger.WarnCtx(ctx, "agent timeout out of range", map[string]any{"timeout_seconds": timeout})
		return nil, fmt.Errorf("timeout out of range: %w", agent.ErrAgentTimeoutOutOfRange)
	}
	// tags
	if err := validateTags(req.Tags); err != nil {
		s.logger.WarnCtx(ctx, "invalid agent tags", map[string]any{"tags": req.Tags})
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	// data_sources:knowledge 必填且非空,其它类型必须为空
	if err := validateDataSources(agentType, req.DataSources); err != nil {
		s.logger.WarnCtx(ctx, "invalid agent data_sources", map[string]any{"agent_type": agentType, "data_sources": req.DataSources})
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	// slug 唯一性
	//sayso-lint:ignore err-swallow
	if _, err := s.repo.FindAgentByOwnerSlug(ctx, userID, req.Slug); err == nil {
		s.logger.WarnCtx(ctx, "agent slug already taken", map[string]any{"user_id": userID, "slug": req.Slug})
		return nil, fmt.Errorf("slug taken: %w", agent.ErrAgentSlugTaken)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		s.logger.ErrorCtx(ctx, "find agent by slug failed", err, nil)
		return nil, fmt.Errorf("find agent: %w: %w", err, agent.ErrAgentInternal)
	}
	// 加密 auth token
	var encryptedToken []byte
	if req.AuthToken != "" {
		var encErr error
		encryptedToken, encErr = agent.EncryptSecret(s.masterKey, req.AuthToken)
		if encErr != nil {
			s.logger.ErrorCtx(ctx, "encrypt auth token failed", encErr, nil)
			return nil, fmt.Errorf("encrypt token: %w", encErr)
		}
	}
	a := &model.Agent{
		OwnerUserID:        userID,
		Slug:               req.Slug,
		DisplayName:        req.DisplayName,
		Description:        req.Description,
		AgentType:          agentType,
		EndpointURL:        req.EndpointURL,
		ContextMode:        contextMode,
		MaxContextRounds:   maxRounds,
		AuthTokenEncrypted: encryptedToken,
		TimeoutSeconds:     timeout,
		IconURL:            req.IconURL,
		Tags:               marshalTags(req.Tags),
		DataSources:        marshalDataSources(req.DataSources),
		Version:            version,
		Status:             model.AgentStatusActive,
	}
	if err := s.repo.CreateAgent(ctx, a); err != nil {
		s.logger.ErrorCtx(ctx, "create agent failed", err, nil)
		return nil, fmt.Errorf("create agent: %w: %w", err, agent.ErrAgentInternal)
	}
	s.logger.InfoCtx(ctx, "agent created", map[string]any{"agent_id": a.ID, "slug": a.Slug})
	resp := agentToDTO(a)
	return &resp, nil
}

// GetAgentByID 根据 ID 获取 agent，若非 owner 则拒绝。
// 可能返回 ErrAgentNotFound、ErrAgentNotAuthor、ErrAgentInternal。
func (s *registryService) GetAgentByID(ctx context.Context, agentID, requesterUserID uint64) (*dto.AgentResponse, error) {
	a, err := s.repo.FindAgentByID(ctx, agentID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "agent not found", map[string]any{"agent_id": agentID})
			return nil, fmt.Errorf("agent not found: %w", agent.ErrAgentNotFound)
		}
		s.logger.ErrorCtx(ctx, "find agent by id failed", err, map[string]any{"agent_id": agentID})
		return nil, fmt.Errorf("find agent: %w: %w", err, agent.ErrAgentInternal)
	}
	if a.OwnerUserID != requesterUserID {
		s.logger.WarnCtx(ctx, "agent access denied, not author", map[string]any{"agent_id": agentID, "requester": requesterUserID})
		return nil, fmt.Errorf("not author: %w", agent.ErrAgentNotAuthor)
	}
	resp := agentToDTO(a)
	return &resp, nil
}

// ListMyAgents 列出指定用户创建的全部 agent。
// 可能返回 ErrAgentInternal。
func (s *registryService) ListMyAgents(ctx context.Context, userID uint64) ([]dto.AgentResponse, error) {
	list, err := s.repo.ListAgentsByOwner(ctx, userID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "list agents failed", err, nil)
		return nil, fmt.Errorf("list agents: %w: %w", err, agent.ErrAgentInternal)
	}
	out := make([]dto.AgentResponse, 0, len(list))
	for _, a := range list {
		out = append(out, agentToDTO(a))
	}
	return out, nil
}

// UpdateAgent 更新 agent 字段，仅允许 owner 操作；支持部分更新。
// 可能返回 ErrAgentNotFound、ErrAgentNotAuthor、ErrAgentDisplayNameInvalid、ErrAgentEndpointInvalid、ErrAgentInternal 等错误。
//
//sayso-lint:ignore sentinel-wrap
func (s *registryService) UpdateAgent(ctx context.Context, agentID, requesterUserID uint64, req dto.UpdateAgentRequest) (*dto.AgentResponse, error) {
	a, err := s.repo.FindAgentByID(ctx, agentID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "agent not found for update", map[string]any{"agent_id": agentID})
			return nil, fmt.Errorf("agent not found: %w", agent.ErrAgentNotFound)
		}
		s.logger.ErrorCtx(ctx, "find agent for update failed", err, map[string]any{"agent_id": agentID})
		return nil, fmt.Errorf("find agent: %w: %w", err, agent.ErrAgentInternal)
	}
	if a.OwnerUserID != requesterUserID {
		s.logger.WarnCtx(ctx, "update agent denied, not author", map[string]any{"agent_id": agentID, "requester": requesterUserID})
		return nil, fmt.Errorf("not author: %w", agent.ErrAgentNotAuthor)
	}
	updates := map[string]any{}
	if req.DisplayName != nil {
		if *req.DisplayName == "" || len(*req.DisplayName) > agent.MaxAgentDisplayNameLength {
			s.logger.WarnCtx(ctx, "invalid display name on update", map[string]any{"display_name": *req.DisplayName})
			return nil, fmt.Errorf("invalid display name: %w", agent.ErrAgentDisplayNameInvalid)
		}
		updates["display_name"] = *req.DisplayName
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.EndpointURL != nil {
		if err := agent.ValidateEndpointURL(*req.EndpointURL, s.cfg.AllowPrivateEndpoints); err != nil {
			s.logger.WarnCtx(ctx, "invalid endpoint on update", map[string]any{"endpoint": *req.EndpointURL, "error": err.Error()})
			//sayso-lint:ignore sentinel-wrap
			return nil, err
		}
		updates["endpoint_url"] = *req.EndpointURL
	}
	if req.ContextMode != nil {
		if *req.ContextMode != agent.ContextModeStateless && *req.ContextMode != agent.ContextModeStateful {
			s.logger.WarnCtx(ctx, "invalid context mode on update", map[string]any{"context_mode": *req.ContextMode})
			return nil, fmt.Errorf("invalid context mode: %w", agent.ErrAgentContextModeInvalid)
		}
		updates["context_mode"] = *req.ContextMode
	}
	if req.MaxContextRounds != nil {
		if *req.MaxContextRounds < agent.MinMaxContextRounds || *req.MaxContextRounds > agent.MaxMaxContextRounds {
			s.logger.WarnCtx(ctx, "max context rounds out of range on update", map[string]any{"max_context_rounds": *req.MaxContextRounds})
			return nil, fmt.Errorf("max context rounds out of range: %w", agent.ErrAgentMaxRoundsOutOfRange)
		}
		updates["max_context_rounds"] = *req.MaxContextRounds
	}
	if req.TimeoutSeconds != nil {
		if *req.TimeoutSeconds < agent.MinTimeoutSeconds || *req.TimeoutSeconds > agent.MaxTimeoutSeconds {
			s.logger.WarnCtx(ctx, "timeout out of range on update", map[string]any{"timeout_seconds": *req.TimeoutSeconds})
			return nil, fmt.Errorf("timeout out of range: %w", agent.ErrAgentTimeoutOutOfRange)
		}
		updates["timeout_seconds"] = *req.TimeoutSeconds
	}
	if req.AuthToken != nil {
		if *req.AuthToken == "" {
			updates["auth_token_encrypted"] = nil
		} else {
			encrypted, encErr := agent.EncryptSecret(s.masterKey, *req.AuthToken)
			if encErr != nil {
				s.logger.ErrorCtx(ctx, "encrypt auth token failed on update", encErr, map[string]any{"agent_id": agentID})
				return nil, fmt.Errorf("encrypt token: %w", encErr)
			}
			updates["auth_token_encrypted"] = encrypted
		}
	}
	if req.IconURL != nil {
		updates["icon_url"] = *req.IconURL
	}
	if req.Tags != nil {
		if err := validateTags(req.Tags); err != nil {
			s.logger.WarnCtx(ctx, "invalid agent tags on update", map[string]any{"tags": req.Tags})
			//sayso-lint:ignore sentinel-wrap
			return nil, err
		}
		updates["tags"] = marshalTags(req.Tags)
	}
	if req.Version != nil {
		updates["version"] = *req.Version
	}
	// AgentType + DataSources 要一起校验:因为 validateDataSources 取决于最终的 type。
	// 组合出最终 type 和最终 sources,一并过一遍校验。
	finalType := a.AgentType
	if req.AgentType != nil {
		if _, ok := agent.ValidAgentTypes[*req.AgentType]; !ok {
			s.logger.WarnCtx(ctx, "unsupported agent type on update", map[string]any{"agent_type": *req.AgentType})
			return nil, fmt.Errorf("unsupported type: %w", agent.ErrAgentTypeUnsupported)
		}
		finalType = *req.AgentType
		updates["agent_type"] = *req.AgentType
	}
	if req.DataSources != nil || req.AgentType != nil {
		// 任一字段变更都需要校验,以免出现 "新 type + 旧 sources" 这种不一致态。
		finalSources := unmarshalDataSources(a.DataSources)
		if req.DataSources != nil {
			finalSources = *req.DataSources
		}
		if err := validateDataSources(finalType, finalSources); err != nil {
			s.logger.WarnCtx(ctx, "invalid data_sources on update", map[string]any{
				"agent_id": agentID, "agent_type": finalType, "data_sources": finalSources,
			})
			//sayso-lint:ignore sentinel-wrap
			return nil, err
		}
		if req.DataSources != nil {
			updates["data_sources"] = marshalDataSources(*req.DataSources)
		}
	}
	if len(updates) > 0 {
		if err := s.repo.UpdateAgentFields(ctx, agentID, updates); err != nil {
			s.logger.ErrorCtx(ctx, "update agent failed", err, nil)
			return nil, fmt.Errorf("update agent: %w: %w", err, agent.ErrAgentInternal)
		}
	}
	updated, err := s.repo.FindAgentByID(ctx, agentID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "reload agent after update failed", err, map[string]any{"agent_id": agentID})
		return nil, fmt.Errorf("reload agent: %w: %w", err, agent.ErrAgentInternal)
	}
	resp := agentToDTO(updated)
	return &resp, nil
}

// DeleteAgent 删除指定 agent，仅允许 owner 操作。
// 可能返回 ErrAgentNotFound、ErrAgentNotAuthor、ErrAgentInternal。
func (s *registryService) DeleteAgent(ctx context.Context, agentID, requesterUserID uint64) error {
	a, err := s.repo.FindAgentByID(ctx, agentID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "agent not found for delete", map[string]any{"agent_id": agentID})
			return fmt.Errorf("agent not found: %w", agent.ErrAgentNotFound)
		}
		s.logger.ErrorCtx(ctx, "find agent for delete failed", err, map[string]any{"agent_id": agentID})
		return fmt.Errorf("find agent: %w: %w", err, agent.ErrAgentInternal)
	}
	if a.OwnerUserID != requesterUserID {
		s.logger.WarnCtx(ctx, "delete agent denied, not author", map[string]any{"agent_id": agentID, "requester": requesterUserID})
		return fmt.Errorf("not author: %w", agent.ErrAgentNotAuthor)
	}
	// 级联删除拆成 3 步,避免一个长事务持锁卡住其他 chat 写入:
	//   1. 先 ban,阻断 prepareChat 接受新对话(已 in-flight 的几秒内会自然结束)。
	//   2. 大表(payload/invocation/message/session)分批删,每批独立 implicit tx。
	//   3. 小表 + agent 行用一个事务,保证元数据原子清理。
	// 这种拆法不再保证全过程原子,但每个删除幂等,失败重试 DeleteAgent 可继续清理残留。
	if err := s.repo.UpdateAgentFields(ctx, agentID, map[string]any{"status": model.AgentStatusBanned}); err != nil {
		s.logger.ErrorCtx(ctx, "ban agent before cascade delete failed", err, map[string]any{"agent_id": agentID})
		return fmt.Errorf("ban agent: %w: %w", err, agent.ErrAgentInternal)
	}
	for _, step := range []struct {
		name string
		fn   func(context.Context, uint64) error
	}{
		{"invocation_payloads", s.repo.DeleteInvocationPayloadsByAgent},
		{"invocations", s.repo.DeleteInvocationsByAgent},
		{"messages", s.repo.DeleteMessagesByAgent},
		{"sessions", s.repo.DeleteSessionsByAgent},
	} {
		if err := step.fn(ctx, agentID); err != nil {
			s.logger.ErrorCtx(ctx, "cascade delete failed", err, map[string]any{"agent_id": agentID, "step": step.name})
			return fmt.Errorf("cascade delete %s: %w: %w", step.name, err, agent.ErrAgentInternal)
		}
	}
	if err := s.repo.WithTx(ctx, func(tx repository.Repository) error {
		if err := tx.DeleteMethodsByAgent(ctx, agentID); err != nil {
			return err
		}
		if err := tx.DeleteSecretsByAgent(ctx, agentID); err != nil {
			return err
		}
		if err := tx.DeletePublishesByAgent(ctx, agentID); err != nil {
			return err
		}
		return tx.DeleteAgent(ctx, agentID)
	}); err != nil {
		s.logger.ErrorCtx(ctx, "delete agent metadata failed", err, nil)
		return fmt.Errorf("delete agent: %w: %w", err, agent.ErrAgentInternal)
	}
	s.logger.InfoCtx(ctx, "agent deleted", map[string]any{"agent_id": agentID})
	return nil
}

// LoadAgentByOwnerSlug 根据 owner UID 和 slug 加载 agent，供内部 ChatService 使用。
// 可能返回 ErrAgentNotFound、ErrAgentInternal。
func (s *registryService) LoadAgentByOwnerSlug(ctx context.Context, ownerUID uint64, slug string) (*model.Agent, error) {
	a, err := s.repo.FindAgentByOwnerSlug(ctx, ownerUID, slug)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "agent not found by owner slug", map[string]any{"owner_uid": ownerUID, "slug": slug})
			return nil, fmt.Errorf("agent not found: %w", agent.ErrAgentNotFound)
		}
		s.logger.ErrorCtx(ctx, "find agent by owner slug failed", err, map[string]any{"owner_uid": ownerUID, "slug": slug})
		return nil, fmt.Errorf("find agent: %w: %w", err, agent.ErrAgentInternal)
	}
	return a, nil
}

// DecryptAuthToken 解密 agent 的 auth token；若无 token 返回空字符串。
// 可能返回 ErrAgentCryptoFailed（来自 agent.DecryptSecret）。
//
func (s *registryService) DecryptAuthToken(ctx context.Context, a *model.Agent) (string, error) {
	if len(a.AuthTokenEncrypted) == 0 {
		return "", nil
	}
	token, err := agent.DecryptSecret(s.masterKey, a.AuthTokenEncrypted)
	if err != nil {
		s.logger.ErrorCtx(ctx, "decrypt auth token failed", err, map[string]any{"agent_id": a.ID})
		//sayso-lint:ignore sentinel-wrap
		return "", err
	}
	return token, nil
}

// validateTags 校验标签数量和单项长度。
func validateTags(tags []string) error {
	if len(tags) > agent.MaxTagsCount {
		return fmt.Errorf("too many tags (max %d): %w", agent.MaxTagsCount, agent.ErrAgentTagsInvalid)
	}
	for _, t := range tags {
		if len([]rune(t)) > agent.MaxTagLength || t == "" {
			return fmt.Errorf("invalid tag (empty or exceeds %d chars): %w", agent.MaxTagLength, agent.ErrAgentTagsInvalid)
		}
	}
	return nil
}
