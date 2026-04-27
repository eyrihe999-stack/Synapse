// dbauth.go 实现 transport.Authenticator,查 agent_registry 表做握手鉴权。
//
// 替换 transport/service.StaticAPIKeyAuthenticator —— main.go 那一行换注入即可,
// transport 包零改动。这就是当初 transport 层留 Authenticator 接口的价值兑现。
//
// 校验流程:
//   1. 读 X-Agent-ID / X-Agent-Key header(缺任何一个 → ErrInvalidHandshake)
//   2. DB 查 agent_registry 按 agent_id
//   3. enabled = false → ErrAgentDisabled(映射到 transport.ErrAuthFailed 给上层)
//   4. ConstantTimeCompare(apikey) 不等 → ErrAuthFailed
//   5. 同步 UPDATE last_seen_at(失败仅 log warn,不影响鉴权结果)
//   6. 返 AgentMeta
package service

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/agents"
	"github.com/eyrihe999-stack/Synapse/internal/agents/repository"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/transport"
)

// DBAuthenticator 实现 transport.Authenticator。
type DBAuthenticator struct {
	repo repository.Repository
	log  logger.LoggerInterface
}

// NewDBAuthenticator 构造。
func NewDBAuthenticator(repo repository.Repository, log logger.LoggerInterface) *DBAuthenticator {
	return &DBAuthenticator{repo: repo, log: log}
}

// Authenticate 实现 transport.Authenticator。
//
// 错误映射(返值必须是 transport 层 sentinel 或其包装,上层 handler 按此分类响应):
//   - transport.ErrInvalidHandshake:header 缺失
//   - transport.ErrAuthFailed      :agent 不存在 / 被禁用 / key 不匹配(不泄漏存在性)
//   - transport.ErrTransportInternal:DB 异常
func (a *DBAuthenticator) Authenticate(ctx context.Context, r *http.Request) (*transport.AgentMeta, error) {
	agentID := r.Header.Get(transport.HeaderAgentID)
	submitted := r.Header.Get(transport.HeaderAgentKey)
	if agentID == "" || submitted == "" {
		return nil, fmt.Errorf("missing %s / %s header: %w",
			transport.HeaderAgentID, transport.HeaderAgentKey, transport.ErrInvalidHandshake)
	}

	row, err := a.repo.FindByAgentID(ctx, agentID)
	if err != nil {
		if errors.Is(err, agents.ErrAgentNotFound) {
			// 不泄漏存在性:未知 agent_id 和 key 不对返同类错
			return nil, fmt.Errorf("agent %s not registered: %w", agentID, transport.ErrAuthFailed)
		}
		a.log.ErrorCtx(ctx, "agents: db lookup failed during handshake", err, map[string]any{
			"agent_id": agentID,
		})
		return nil, fmt.Errorf("db lookup: %w: %w", err, transport.ErrTransportInternal)
	}

	if !row.Enabled {
		a.log.WarnCtx(ctx, "agents: handshake rejected - disabled", map[string]any{
			"agent_id": agentID, "ip": r.RemoteAddr,
		})
		return nil, fmt.Errorf("agent %s disabled: %w", agentID, transport.ErrAuthFailed)
	}

	// ConstantTimeCompare:防 timing attack(虽 V1 明文存,该防还是防,零成本)
	if subtle.ConstantTimeCompare([]byte(submitted), []byte(row.APIKey)) != 1 {
		a.log.WarnCtx(ctx, "agents: handshake rejected - apikey mismatch", map[string]any{
			"agent_id": agentID, "ip": r.RemoteAddr,
		})
		return nil, fmt.Errorf("apikey mismatch: %w", transport.ErrAuthFailed)
	}

	// 同步更新 last_seen_at。失败不影响鉴权 —— 只 log warn。
	// TODO: 规模上去后加 debounce —— 距上次更新 > 60s 才写。
	updateCtx, cancel := context.WithTimeout(ctx, agents.LastSeenUpdateTimeout)
	defer cancel()
	if uerr := a.repo.UpdateLastSeen(updateCtx, row.ID, time.Now()); uerr != nil {
		a.log.WarnCtx(ctx, "agents: last_seen update failed (non-fatal)", map[string]any{
			"agent_id": agentID, "err": uerr.Error(),
		})
	}

	return &transport.AgentMeta{
		AgentID:     transport.AgentID(row.AgentID),
		AuthMode:    transport.AuthModeAPIKey,
		OrgID:       row.OrgID,
		ConnectedAt: time.Now(),
	}, nil
}
