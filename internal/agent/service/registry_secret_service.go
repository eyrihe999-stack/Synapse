// registry_secret_service.go Agent secret / health 相关操作。
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/agent"
	"github.com/eyrihe999-stack/Synapse/internal/agent/dto"
	"gorm.io/gorm"
)

// RotateSecret 生成新 secret,保留 previous 24 小时。
//
// 错误:agent 不存在返回 ErrAgentNotFound,非作者返回 ErrAgentNotAuthor;加解密失败返回 ErrAgentCryptoFailed;
// 存储错误返回 ErrAgentInternal。
func (s *registryService) RotateSecret(ctx context.Context, agentID, requesterUserID uint64) (*dto.RotateSecretResponse, error) {
	//sayso-lint:ignore err-swallow
	if _, err := s.loadAgentOwned(ctx, agentID, requesterUserID); err != nil { // agent 返回值仅用于权限校验
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, err
	}
	existing, err := s.repo.FindSecretByAgent(ctx, agentID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "find secret failed", err, map[string]any{"agent_id": agentID})
		return nil, fmt.Errorf("find secret: %w: %w", err, agent.ErrAgentInternal)
	}
	plaintext, err := agent.GenerateSecret()
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	newEncrypted, err := agent.EncryptSecret(s.masterKey, plaintext)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	now := time.Now().UTC()
	grace := now.Add(agent.SecretRotateGraceHours * time.Hour)
	updates := map[string]any{
		"encrypted_secret":          newEncrypted,
		"previous_encrypted_secret": existing.EncryptedSecret,
		"previous_expires_at":       &grace,
		"last_rotated_at":           &now,
	}
	if err := s.repo.UpdateSecret(ctx, agentID, updates); err != nil {
		s.logger.ErrorCtx(ctx, "rotate secret update failed", err, map[string]any{"agent_id": agentID})
		return nil, fmt.Errorf("rotate secret: %w: %w", err, agent.ErrAgentInternal)
	}
	s.logger.InfoCtx(ctx, "secret rotated", map[string]any{"agent_id": agentID})
	return &dto.RotateSecretResponse{
		Secret: plaintext,
		Notice: "Previous secret will be accepted for 24 hours.",
	}, nil
}

// GetHealth 作者查看 agent 健康快照。
//
// 错误:agent 不存在返回 ErrAgentNotFound,非作者返回 ErrAgentNotAuthor,其他存储错误返回 ErrAgentInternal。
func (s *registryService) GetHealth(ctx context.Context, agentID, requesterUserID uint64) (*dto.HealthResponse, error) {
	a, err := s.loadAgentOwned(ctx, agentID, requesterUserID)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, err
	}
	resp := &dto.HealthResponse{
		Status:    a.HealthStatus,
		FailCount: a.HealthFailCount,
	}
	if a.HealthCheckedAt != nil {
		resp.CheckedAt = a.HealthCheckedAt.Unix()
	}
	return resp, nil
}

// LoadActiveSecret 读取并解密当前 secret,如存在未过期的 previous 也一并返回(grace 期支持双 secret 验签)。
func (s *registryService) LoadActiveSecret(ctx context.Context, agentID uint64) (string, string, error) {
	sec, err := s.repo.FindSecretByAgent(ctx, agentID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			//sayso-lint:ignore log-coverage
			return "", "", fmt.Errorf("secret not found: %w", agent.ErrAgentNotFound)
		}
		s.logger.ErrorCtx(ctx, "find secret failed", err, map[string]any{"agent_id": agentID})
		return "", "", fmt.Errorf("find secret: %w: %w", err, agent.ErrAgentInternal)
	}
	plaintext, err := agent.DecryptSecret(s.masterKey, sec.EncryptedSecret)
	if err != nil {
		s.logger.ErrorCtx(ctx, "decrypt secret failed", err, map[string]any{"agent_id": agentID})
		//sayso-lint:ignore sentinel-wrap
		return "", "", err
	}
	var previous string
	if len(sec.PreviousEncryptedSecret) > 0 && sec.PreviousExpiresAt != nil && time.Now().UTC().Before(*sec.PreviousExpiresAt) {
		if pv, pErr := agent.DecryptSecret(s.masterKey, sec.PreviousEncryptedSecret); pErr == nil {
			previous = pv
		}
	}
	return plaintext, previous, nil
}
