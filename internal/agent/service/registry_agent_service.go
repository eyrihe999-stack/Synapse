// registry_agent_service.go Agent CRUD。
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/agent"
	"github.com/eyrihe999-stack/Synapse/internal/agent/dto"
	"github.com/eyrihe999-stack/Synapse/internal/agent/model"
	"github.com/eyrihe999-stack/Synapse/internal/agent/repository"
	"gorm.io/gorm"
)

// CreateAgent 创建 agent。
//
// 错误:参数校验失败返回对应 sentinel(ErrAgentSlugInvalid、ErrAgentDisplayNameInvalid、ErrAgentEndpointInvalid、
// ErrMethodEmpty、ErrMethodNameTaken 等);slug 已占用返回 ErrAgentSlugTaken;加密或事务存储错误返回 ErrAgentInternal/ErrAgentCryptoFailed。
func (s *registryService) CreateAgent(ctx context.Context, userID uint64, req dto.CreateAgentRequest) (*dto.CreateAgentResponse, error) {
	// 参数校验
	if err := validateAgentSlug(req.Slug); err != nil {
		s.logger.WarnCtx(ctx, "agent slug 非法", map[string]any{"user_id": userID, "slug": req.Slug})
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	if req.DisplayName == "" || len(req.DisplayName) > agent.MaxAgentDisplayNameLength {
		s.logger.WarnCtx(ctx, "display_name 非法", map[string]any{"user_id": userID})
		return nil, fmt.Errorf("display name invalid: %w", agent.ErrAgentDisplayNameInvalid)
	}
	if len(req.Description) > agent.MaxAgentDescriptionLength {
		s.logger.WarnCtx(ctx, "description 过长", map[string]any{"user_id": userID})
		return nil, fmt.Errorf("description too long: %w", agent.ErrAgentInvalidRequest)
	}
	if err := validateEndpoint(req.EndpointURL); err != nil {
		s.logger.WarnCtx(ctx, "endpoint 非法", map[string]any{"user_id": userID, "endpoint": req.EndpointURL})
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	timeout, rate, concur, err := normalizeLimits(req.TimeoutSec, req.RatePerMin, req.MaxConcur)
	if err != nil {
		s.logger.WarnCtx(ctx, "limits 非法", map[string]any{"user_id": userID})
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	if len(req.Methods) == 0 {
		s.logger.WarnCtx(ctx, "method 列表为空", map[string]any{"user_id": userID})
		return nil, fmt.Errorf("methods empty: %w", agent.ErrMethodEmpty)
	}
	methodsToInsert := make([]*model.AgentMethod, 0, len(req.Methods))
	seen := make(map[string]struct{}, len(req.Methods))
	for _, mreq := range req.Methods {
		m, mErr := buildMethodModel(mreq)
		if mErr != nil {
			//sayso-lint:ignore sentinel-wrap
			return nil, mErr
		}
		if _, dup := seen[m.MethodName]; dup {
			s.logger.WarnCtx(ctx, "method 名重复", map[string]any{"user_id": userID, "name": m.MethodName})
			return nil, fmt.Errorf("method duplicate: %w", agent.ErrMethodNameTaken)
		}
		seen[m.MethodName] = struct{}{}
		methodsToInsert = append(methodsToInsert, m)
	}

	// slug 唯一性预检(作者内)
	if existing, findErr := s.repo.FindAgentByOwnerSlug(ctx, userID, req.Slug); findErr == nil && existing != nil {
		s.logger.WarnCtx(ctx, "agent slug 已占用", map[string]any{"user_id": userID, "slug": req.Slug})
		return nil, fmt.Errorf("slug taken: %w", agent.ErrAgentSlugTaken)
	} else if findErr != nil && !errors.Is(findErr, gorm.ErrRecordNotFound) {
		s.logger.ErrorCtx(ctx, "查 agent slug 失败", findErr, map[string]any{"user_id": userID})
		return nil, fmt.Errorf("check slug: %w: %w", findErr, agent.ErrAgentInternal)
	}

	// 生成 secret
	plaintext, err := agent.GenerateSecret()
	if err != nil {
		s.logger.ErrorCtx(ctx, "生成 secret 失败", err, map[string]any{"user_id": userID})
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	encrypted, err := agent.EncryptSecret(s.masterKey, plaintext)
	if err != nil {
		s.logger.ErrorCtx(ctx, "加密 secret 失败", err, map[string]any{"user_id": userID})
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}

	var createdAgent *model.Agent

	err = s.repo.WithTx(ctx, func(tx repository.Repository) error {
		a := &model.Agent{
			OwnerUserID:        userID,
			Slug:               req.Slug,
			DisplayName:        req.DisplayName,
			Description:        req.Description,
			Protocol:           agent.ProtocolJSONRPC,
			EndpointURL:        req.EndpointURL,
			DiscoveryMode:      agent.DiscoveryModeManual,
			IconURL:            req.IconURL,
			Tags:               marshalTags(req.Tags),
			HomepageURL:        req.HomepageURL,
			PriceTag:           req.PriceTag,
			DeveloperContact:   req.Developer,
			Version:            req.Version,
			TimeoutSeconds:     timeout,
			RateLimitPerMinute: rate,
			MaxConcurrent:      concur,
			Status:             model.AgentStatusActive,
			HealthStatus:       model.HealthStatusUnknown,
		}
		if createErr := tx.CreateAgent(ctx, a); createErr != nil {
			s.logger.ErrorCtx(ctx, "事务内创建 agent 失败", createErr, map[string]any{"user_id": userID})
			return fmt.Errorf("tx create agent: %w: %w", createErr, agent.ErrAgentInternal)
		}
		for _, m := range methodsToInsert {
			m.AgentID = a.ID
		}
		if createErr := tx.CreateMethodsBatch(ctx, methodsToInsert); createErr != nil {
			s.logger.ErrorCtx(ctx, "事务内创建 methods 失败", createErr, map[string]any{"agent_id": a.ID})
			return fmt.Errorf("tx create methods: %w: %w", createErr, agent.ErrAgentInternal)
		}
		sec := &model.AgentSecret{
			AgentID:         a.ID,
			EncryptedSecret: encrypted,
		}
		if createErr := tx.CreateSecret(ctx, sec); createErr != nil {
			s.logger.ErrorCtx(ctx, "事务内创建 secret 失败", createErr, map[string]any{"agent_id": a.ID})
			return fmt.Errorf("tx create secret: %w: %w", createErr, agent.ErrAgentInternal)
		}
		createdAgent = a
		return nil
	})
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}

	s.logger.InfoCtx(ctx, "agent 注册成功", map[string]any{"user_id": userID, "agent_id": createdAgent.ID, "slug": createdAgent.Slug})
	return &dto.CreateAgentResponse{
		Agent:  agentToDTO(createdAgent),
		Secret: plaintext,
		Notice: "Secret will only be shown once. Store it securely.",
	}, nil
}

// GetAgentByID 作者查询 agent 详情。非作者返回 ErrAgentNotAuthor。
func (s *registryService) GetAgentByID(ctx context.Context, agentID, requesterUserID uint64) (*dto.AgentResponse, error) {
	a, err := s.loadAgentOwned(ctx, agentID, requesterUserID)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, err
	}
	resp := agentToDTO(a)
	return &resp, nil
}

// ListMyAgents 列出调用者拥有的所有 agent。
//
// 错误:底层存储查询失败时返回 ErrAgentInternal。
func (s *registryService) ListMyAgents(ctx context.Context, userID uint64) ([]dto.AgentResponse, error) {
	list, err := s.repo.ListAgentsByOwner(ctx, userID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "list my agents failed", err, map[string]any{"user_id": userID})
		return nil, fmt.Errorf("list my agents: %w: %w", err, agent.ErrAgentInternal)
	}
	out := make([]dto.AgentResponse, 0, len(list))
	for _, a := range list {
		out = append(out, agentToDTO(a))
	}
	return out, nil
}

// UpdateAgent 作者更新 agent 元信息。
//
// 错误:agent 不存在返回 ErrAgentNotFound,非作者返回 ErrAgentNotAuthor;字段校验失败返回对应 sentinel
// (ErrAgentDisplayNameInvalid、ErrAgentEndpointInvalid、ErrAgentInvalidRequest 等);存储错误返回 ErrAgentInternal。
func (s *registryService) UpdateAgent(ctx context.Context, agentID, requesterUserID uint64, req dto.UpdateAgentRequest) (*dto.AgentResponse, error) {
	//sayso-lint:ignore err-swallow
	if _, err := s.loadAgentOwned(ctx, agentID, requesterUserID); err != nil { // agent 返回值仅用于权限校验
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, err
	}
	updates := map[string]any{}
	if req.DisplayName != nil {
		if *req.DisplayName == "" || len(*req.DisplayName) > agent.MaxAgentDisplayNameLength {
			s.logger.WarnCtx(ctx, "display_name invalid", map[string]any{"agent_id": agentID})
			return nil, fmt.Errorf("display name invalid: %w", agent.ErrAgentDisplayNameInvalid)
		}
		updates["display_name"] = *req.DisplayName
	}
	if req.Description != nil {
		if len(*req.Description) > agent.MaxAgentDescriptionLength {
			s.logger.WarnCtx(ctx, "description too long", map[string]any{"agent_id": agentID})
			return nil, fmt.Errorf("description too long: %w", agent.ErrAgentInvalidRequest)
		}
		updates["description"] = *req.Description
	}
	if req.EndpointURL != nil {
		if err := validateEndpoint(*req.EndpointURL); err != nil {
			s.logger.WarnCtx(ctx, "endpoint invalid", map[string]any{"agent_id": agentID, "endpoint": *req.EndpointURL})
			//sayso-lint:ignore sentinel-wrap
			return nil, err
		}
		updates["endpoint_url"] = *req.EndpointURL
	}
	if req.IconURL != nil {
		updates["icon_url"] = *req.IconURL
	}
	if req.Tags != nil {
		updates["tags"] = marshalTags(req.Tags)
	}
	if req.HomepageURL != nil {
		updates["homepage_url"] = *req.HomepageURL
	}
	if req.Developer != nil {
		updates["developer_contact"] = *req.Developer
	}
	if req.Version != nil {
		updates["version"] = *req.Version
	}
	if req.TimeoutSec != nil || req.RatePerMin != nil || req.MaxConcur != nil {
		// 复用 normalizeLimits:未传字段用 0,后续走默认
		t := 0
		if req.TimeoutSec != nil {
			t = *req.TimeoutSec
		}
		r := 0
		if req.RatePerMin != nil {
			r = *req.RatePerMin
		}
		c := 0
		if req.MaxConcur != nil {
			c = *req.MaxConcur
		}
		nt, nr, nc, err := normalizeLimits(t, r, c)
		if err != nil {
			s.logger.WarnCtx(ctx, "limits invalid", map[string]any{"agent_id": agentID})
			//sayso-lint:ignore sentinel-wrap
			return nil, err
		}
		if req.TimeoutSec != nil {
			updates["timeout_seconds"] = nt
		}
		if req.RatePerMin != nil {
			updates["rate_limit_per_minute"] = nr
		}
		if req.MaxConcur != nil {
			updates["max_concurrent"] = nc
		}
	}
	if len(updates) == 0 {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return s.GetAgentByID(ctx, agentID, requesterUserID)
	}
	if err := s.repo.UpdateAgentFields(ctx, agentID, updates); err != nil {
		s.logger.ErrorCtx(ctx, "update agent failed", err, map[string]any{"agent_id": agentID})
		return nil, fmt.Errorf("update agent: %w: %w", err, agent.ErrAgentInternal)
	}
	//sayso-lint:ignore sentinel-wrap,log-coverage
	return s.GetAgentByID(ctx, agentID, requesterUserID)
}

// DeleteAgent 作者删除 agent,级联 methods / secret / publish(publish 标记 revoked)。
//
// 错误:agent 不存在返回 ErrAgentNotFound,非作者返回 ErrAgentNotAuthor;事务内任一存储错误返回 ErrAgentInternal。
func (s *registryService) DeleteAgent(ctx context.Context, agentID, requesterUserID uint64) error {
	//sayso-lint:ignore err-swallow
	if _, err := s.loadAgentOwned(ctx, agentID, requesterUserID); err != nil { // agent 返回值仅用于权限校验
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return err
	}
	now := time.Now().UTC()
	err := s.repo.WithTx(ctx, func(tx repository.Repository) error {
		// 标记所有 active publish 为 revoked(reason=author_unpublished)
		publishes, pErr := tx.ListActivePublishesByAgent(ctx, agentID)
		if pErr != nil {
			//sayso-lint:ignore log-coverage
			return fmt.Errorf("list publishes: %w: %w", pErr, agent.ErrAgentInternal)
		}
		for _, p := range publishes {
			if uErr := tx.UpdatePublishFields(ctx, p.ID, map[string]any{
				"status":         model.PublishStatusRevoked,
				"revoked_at":     &now,
				"revoked_reason": agent.RevokedReasonAuthorUnpublished,
			}); uErr != nil {
				//sayso-lint:ignore log-coverage
				return fmt.Errorf("revoke publish: %w: %w", uErr, agent.ErrAgentInternal)
			}
		}
		if dErr := tx.DeleteMethodsByAgent(ctx, agentID); dErr != nil {
			//sayso-lint:ignore log-coverage
			return fmt.Errorf("delete methods: %w: %w", dErr, agent.ErrAgentInternal)
		}
		if dErr := tx.DeleteSecretByAgent(ctx, agentID); dErr != nil {
			//sayso-lint:ignore log-coverage
			return fmt.Errorf("delete secret: %w: %w", dErr, agent.ErrAgentInternal)
		}
		if dErr := tx.DeleteAgent(ctx, agentID); dErr != nil {
			//sayso-lint:ignore log-coverage
			return fmt.Errorf("delete agent: %w: %w", dErr, agent.ErrAgentInternal)
		}
		return nil
	})
	if err != nil {
		s.logger.ErrorCtx(ctx, "delete agent failed", err, map[string]any{"agent_id": agentID})
		//sayso-lint:ignore sentinel-wrap
		return err
	}
	s.logger.InfoCtx(ctx, "agent 已删除", map[string]any{"agent_id": agentID, "user_id": requesterUserID})
	return nil
}

// LoadAgentModel 按 ID 加载 agent(未过滤作者,gateway 使用)。
//
// 错误:未找到返回 ErrAgentNotFound,其他存储错误返回 ErrAgentInternal。
func (s *registryService) LoadAgentModel(ctx context.Context, agentID uint64) (*model.Agent, error) {
	a, err := s.repo.FindAgentByID(ctx, agentID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			//sayso-lint:ignore log-coverage
			return nil, fmt.Errorf("agent not found: %w", agent.ErrAgentNotFound)
		}
		s.logger.ErrorCtx(ctx, "find agent failed", err, map[string]any{"agent_id": agentID})
		return nil, fmt.Errorf("find agent: %w: %w", err, agent.ErrAgentInternal)
	}
	return a, nil
}

// LoadAgentByOwnerSlug 按 (owner_uid, slug) 加载 agent。
//
// 错误:未找到返回 ErrAgentNotFound,其他存储错误返回 ErrAgentInternal。
func (s *registryService) LoadAgentByOwnerSlug(ctx context.Context, ownerUID uint64, slug string) (*model.Agent, error) {
	a, err := s.repo.FindAgentByOwnerSlug(ctx, ownerUID, slug)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			//sayso-lint:ignore log-coverage
			return nil, fmt.Errorf("agent not found: %w", agent.ErrAgentNotFound)
		}
		s.logger.ErrorCtx(ctx, "find agent by owner slug failed", err, map[string]any{"owner_uid": ownerUID, "slug": slug})
		return nil, fmt.Errorf("find agent by owner slug: %w: %w", err, agent.ErrAgentInternal)
	}
	return a, nil
}
