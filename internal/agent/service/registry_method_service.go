// registry_method_service.go Agent Method CRUD。
package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/agent"
	"github.com/eyrihe999-stack/Synapse/internal/agent/dto"
	"github.com/eyrihe999-stack/Synapse/internal/agent/model"
	"gorm.io/gorm"
)

// ListMethods 列出某 agent 的 methods(作者视角)。
//
// 错误:agent 不存在返回 ErrAgentNotFound,非作者返回 ErrAgentNotAuthor,其他存储错误返回 ErrAgentInternal。
func (s *registryService) ListMethods(ctx context.Context, agentID, requesterUserID uint64) ([]dto.MethodResponse, error) {
	//sayso-lint:ignore err-swallow
	if _, err := s.loadAgentOwned(ctx, agentID, requesterUserID); err != nil { // agent 返回值仅用于权限校验
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, err
	}
	list, err := s.repo.ListMethodsByAgent(ctx, agentID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "list methods failed", err, map[string]any{"agent_id": agentID})
		return nil, fmt.Errorf("list methods: %w: %w", err, agent.ErrAgentInternal)
	}
	out := make([]dto.MethodResponse, 0, len(list))
	for _, m := range list {
		out = append(out, methodToDTO(m))
	}
	return out, nil
}

// CreateMethod 追加 method。重名返回 ErrMethodNameTaken。
func (s *registryService) CreateMethod(ctx context.Context, agentID, requesterUserID uint64, req dto.CreateMethodRequest) (*dto.MethodResponse, error) {
	//sayso-lint:ignore err-swallow
	if _, err := s.loadAgentOwned(ctx, agentID, requesterUserID); err != nil { // agent 返回值仅用于权限校验
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, err
	}
	m, err := buildMethodModel(req)
	if err != nil {
		s.logger.WarnCtx(ctx, "build method model failed", map[string]any{"agent_id": agentID})
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	m.AgentID = agentID
	// 预检重名
	if existing, fErr := s.repo.FindMethodByAgentName(ctx, agentID, m.MethodName); fErr == nil && existing != nil {
		s.logger.WarnCtx(ctx, "method name taken", map[string]any{"agent_id": agentID, "method_name": m.MethodName})
		return nil, fmt.Errorf("method taken: %w", agent.ErrMethodNameTaken)
	} else if fErr != nil && !errors.Is(fErr, gorm.ErrRecordNotFound) {
		s.logger.ErrorCtx(ctx, "find method failed", fErr, map[string]any{"agent_id": agentID})
		return nil, fmt.Errorf("find method: %w: %w", fErr, agent.ErrAgentInternal)
	}
	if err := s.repo.CreateMethod(ctx, m); err != nil {
		s.logger.ErrorCtx(ctx, "create method failed", err, map[string]any{"agent_id": agentID})
		return nil, fmt.Errorf("create method: %w: %w", err, agent.ErrAgentInternal)
	}
	resp := methodToDTO(m)
	return &resp, nil
}

// UpdateMethod 更新 method(限 display_name / description / transport / visibility)。
//
// 错误:agent/method 不存在或不归属返回 ErrAgentNotFound/ErrMethodNotFound,非作者返回 ErrAgentNotAuthor;
// 字段校验失败返回 ErrAgentInvalidRequest/ErrMethodTransportUnsupported/ErrMethodVisibilityInvalid;存储错误返回 ErrAgentInternal。
func (s *registryService) UpdateMethod(ctx context.Context, agentID, methodID, requesterUserID uint64, req dto.UpdateMethodRequest) (*dto.MethodResponse, error) {
	//sayso-lint:ignore err-swallow
	if _, err := s.loadAgentOwned(ctx, agentID, requesterUserID); err != nil { // agent 返回值仅用于权限校验
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, err
	}
	m, err := s.repo.FindMethodByID(ctx, methodID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			//sayso-lint:ignore log-coverage
			return nil, fmt.Errorf("method not found: %w", agent.ErrMethodNotFound)
		}
		s.logger.ErrorCtx(ctx, "find method failed", err, map[string]any{"method_id": methodID})
		return nil, fmt.Errorf("find method: %w: %w", err, agent.ErrAgentInternal)
	}
	if m.AgentID != agentID {
		return nil, fmt.Errorf("method not in agent: %w", agent.ErrMethodNotFound)
	}
	updates := map[string]any{}
	if req.DisplayName != nil {
		if *req.DisplayName == "" || len(*req.DisplayName) > agent.MaxMethodDisplayNameLength {
			return nil, fmt.Errorf("method display name invalid: %w", agent.ErrAgentInvalidRequest)
		}
		updates["display_name"] = *req.DisplayName
	}
	if req.Description != nil {
		if len(*req.Description) > agent.MaxMethodDescriptionLength {
			return nil, fmt.Errorf("description too long: %w", agent.ErrAgentInvalidRequest)
		}
		updates["description"] = *req.Description
	}
	if req.Transport != nil {
		if err := validateTransport(*req.Transport); err != nil {
			s.logger.WarnCtx(ctx, "transport invalid", map[string]any{"method_id": methodID, "transport": *req.Transport})
			//sayso-lint:ignore sentinel-wrap
			return nil, err
		}
		updates["transport"] = *req.Transport
	}
	if req.Visibility != nil {
		if err := validateVisibility(*req.Visibility); err != nil {
			s.logger.WarnCtx(ctx, "visibility invalid", map[string]any{"method_id": methodID, "visibility": *req.Visibility})
			//sayso-lint:ignore sentinel-wrap
			return nil, err
		}
		updates["visibility"] = *req.Visibility
	}
	if len(updates) == 0 {
		resp := methodToDTO(m)
		return &resp, nil
	}
	if err := s.repo.UpdateMethodFields(ctx, methodID, updates); err != nil {
		s.logger.ErrorCtx(ctx, "update method failed", err, map[string]any{"method_id": methodID})
		return nil, fmt.Errorf("update method: %w: %w", err, agent.ErrAgentInternal)
	}
	// 重载返回
	reloaded, err := s.repo.FindMethodByID(ctx, methodID)
	if err != nil {
		return nil, fmt.Errorf("reload method: %w: %w", err, agent.ErrAgentInternal)
	}
	resp := methodToDTO(reloaded)
	return &resp, nil
}

// DeleteMethod 删除 method,保留至少 1 条。
//
// 错误:agent/method 不存在返回 ErrAgentNotFound/ErrMethodNotFound,非作者返回 ErrAgentNotAuthor;
// 仅剩最后 1 条返回 ErrMethodLastCannotDelete;存储错误返回 ErrAgentInternal。
func (s *registryService) DeleteMethod(ctx context.Context, agentID, methodID, requesterUserID uint64) error {
	//sayso-lint:ignore err-swallow
	if _, err := s.loadAgentOwned(ctx, agentID, requesterUserID); err != nil { // agent 返回值仅用于权限校验
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return err
	}
	m, err := s.repo.FindMethodByID(ctx, methodID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			//sayso-lint:ignore log-coverage
			return fmt.Errorf("method not found: %w", agent.ErrMethodNotFound)
		}
		s.logger.ErrorCtx(ctx, "find method failed", err, map[string]any{"method_id": methodID})
		return fmt.Errorf("find method: %w: %w", err, agent.ErrAgentInternal)
	}
	if m.AgentID != agentID {
		//sayso-lint:ignore log-coverage
		return fmt.Errorf("method not in agent: %w", agent.ErrMethodNotFound)
	}
	count, err := s.repo.CountMethodsByAgent(ctx, agentID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "count methods failed", err, map[string]any{"agent_id": agentID})
		return fmt.Errorf("count methods: %w: %w", err, agent.ErrAgentInternal)
	}
	if count <= 1 {
		return fmt.Errorf("last method: %w", agent.ErrMethodLastCannotDelete)
	}
	if err := s.repo.DeleteMethod(ctx, methodID); err != nil {
		s.logger.ErrorCtx(ctx, "delete method failed", err, map[string]any{"method_id": methodID})
		return fmt.Errorf("delete method: %w: %w", err, agent.ErrAgentInternal)
	}
	return nil
}

// LoadMethod 按 (agent_id, method_name) 加载 method。
//
// 错误:未找到返回 ErrInvokeMethodNotDeclared,其他存储错误返回 ErrAgentInternal。
func (s *registryService) LoadMethod(ctx context.Context, agentID uint64, methodName string) (*model.AgentMethod, error) {
	m, err := s.repo.FindMethodByAgentName(ctx, agentID, methodName)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			//sayso-lint:ignore log-coverage
			return nil, fmt.Errorf("method not found: %w", agent.ErrInvokeMethodNotDeclared)
		}
		s.logger.ErrorCtx(ctx, "find method failed", err, map[string]any{"agent_id": agentID, "method_name": methodName})
		return nil, fmt.Errorf("find method: %w: %w", err, agent.ErrAgentInternal)
	}
	return m, nil
}
