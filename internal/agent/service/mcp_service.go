// mcp_service.go MCP 协议代理服务:把调用方发来的 JSON-RPC 包转发给 agent 的 MCP endpoint,
// 响应回传。Synapse 不解析 JSON-RPC 内容,纯透传;只负责:
//   - 权限(OAuth token 里的 orgID 要对该 agent 有 approved publish)
//   - 认证转换(token 换成 agent 自己的 auth_token)
//   - SSRF 防护(复用 GuardedTransport 拦 loopback/私网)
//   - 超时控制(用 agent.timeout_seconds 作 ctx deadline)
//
// 透传的好处:agent 实现方想怎么加新 MCP method(如 resources / prompts)都不用改 Synapse。
package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/agent"
	"github.com/eyrihe999-stack/Synapse/internal/agent/hub"
	"github.com/eyrihe999-stack/Synapse/internal/agent/model"
	"github.com/eyrihe999-stack/Synapse/internal/agent/repository"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
)

// MCPProxyService 把 MCP JSON-RPC 请求代理到指定 agent 的 endpoint。
type MCPProxyService interface {
	// Invoke 按 (ownerUID, slug) 找 agent 然后转发。HTTP 入口路径用此签名。
	Invoke(ctx context.Context, orgID, ownerUID uint64, agentSlug string, rpcBody []byte) ([]byte, error)

	// InvokeByID 按 agent_id 直接转发。invoke_agent meta-tool 用此签名 —— 调用方
	// 从 search_agent 结果里只拿到 id 字符串,不需要再解成 (owner, slug)。
	// 其它校验 / 转发逻辑与 Invoke 一致。
	InvokeByID(ctx context.Context, orgID, agentID uint64, rpcBody []byte) ([]byte, error)
}

type mcpProxyService struct {
	cfg        Config
	repo       repository.Repository
	registry   RegistryService
	publishSvc PublishService
	hub        *hub.Hub // 可为 nil(hub 未启用时)
	logger     logger.LoggerInterface
	httpClient *http.Client
}

// NewMCPProxyService 构造 MCPProxyService。httpClient 复用和 chat_service 同款 GuardedTransport 策略。
// h 为 nil = 禁用 WS 反向隧道,所有调用一律走 HTTP endpoint_url。
func NewMCPProxyService(
	cfg Config,
	repo repository.Repository,
	registry RegistryService,
	publishSvc PublishService,
	h *hub.Hub,
	log logger.LoggerInterface,
) MCPProxyService {
	return &mcpProxyService{
		cfg:        cfg,
		repo:       repo,
		registry:   registry,
		publishSvc: publishSvc,
		hub:        h,
		logger:     log,
		httpClient: &http.Client{
			// 全局 Timeout 不设,由 ctx deadline(agent.timeout_seconds)控制
			Transport:     agent.NewGuardedTransport(cfg.AllowPrivateEndpoints),
			CheckRedirect: agent.NoRedirectPolicy,
		},
	}
}

func (s *mcpProxyService) Invoke(ctx context.Context, orgID, ownerUID uint64, agentSlug string, rpcBody []byte) ([]byte, error) {
	if orgID == 0 {
		return nil, fmt.Errorf("orgID required: %w", agent.ErrAgentInvalidRequest)
	}
	ag, err := s.registry.LoadAgentByOwnerSlug(ctx, ownerUID, agentSlug)
	if err != nil {
		return nil, err
	}
	return s.invokeLoaded(ctx, orgID, ag, rpcBody)
}

// InvokeByID 按 agent_id 查 agent 再转发。所有校验和 Invoke 等同。
func (s *mcpProxyService) InvokeByID(ctx context.Context, orgID, agentID uint64, rpcBody []byte) ([]byte, error) {
	if orgID == 0 {
		return nil, fmt.Errorf("orgID required: %w", agent.ErrAgentInvalidRequest)
	}
	ag, err := s.repo.FindAgentByID(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if ag == nil {
		return nil, fmt.Errorf("agent %d: %w", agentID, agent.ErrAgentNotFound)
	}
	return s.invokeLoaded(ctx, orgID, ag, rpcBody)
}

// invokeLoaded 已加载 ag,做剩下的校验 + 转发。Invoke / InvokeByID 共用主体。
func (s *mcpProxyService) invokeLoaded(ctx context.Context, orgID uint64, ag *model.Agent, rpcBody []byte) ([]byte, error) {
	if ag.Status != agent.AgentStatusActive {
		return nil, fmt.Errorf("agent status not active: %w", agent.ErrAgentNotFound)
	}
	if ag.AgentType != agent.AgentTypeMCP {
		return nil, fmt.Errorf("agent_type=%s,not mcp: %w", ag.AgentType, agent.ErrAgentInvalidRequest)
	}

	// 2) 权限:token 的 orgID 对这个 agent 有 approved publish
	pub, err := s.publishSvc.GetActivePublish(ctx, ag.ID, orgID)
	if err != nil {
		return nil, err
	}
	if pub == nil {
		return nil, fmt.Errorf("agent not published to org %d: %w", orgID, agent.ErrAgentNotFound)
	}

	// 3) 优先走 WS 反向隧道。hub 命中 = agent 本地在线且保持长连接,消息经此投递不走公网;
	// 未命中(离线 / hub 未启用)fallback 到 HTTP endpoint_url。
	if s.hub != nil && s.hub.IsOnline(ag.ID) {
		// 超时统一用 agent.TimeoutSeconds;hub.Invoke 会复用 ctx deadline。
		invokeCtx := ctx
		if ag.TimeoutSeconds > 0 {
			var cancel context.CancelFunc
			invokeCtx, cancel = context.WithTimeout(ctx, time.Duration(ag.TimeoutSeconds)*time.Second)
			defer cancel()
		}
		resp, hErr := s.hub.Invoke(invokeCtx, ag.ID, rpcBody)
		if hErr == nil {
			return resp, nil
		}
		// offline 是连接瞬断,fall through 到 HTTP;其他 hub 错误直接返。
		if !errors.Is(hErr, hub.ErrAgentOffline) {
			if errors.Is(hErr, hub.ErrInvokeTimeout) {
				return nil, fmt.Errorf("agent timeout via ws: %w", agent.ErrChatUpstreamTimeout)
			}
			s.logger.WarnCtx(ctx, "mcp proxy: ws invoke failed, falling back to http", map[string]any{
				"agent_id": ag.ID, "err": hErr.Error(),
			})
		}
		// fall through to HTTP
	}

	// 4) 认证转换:Synapse 自己的 OAuth token 不给 agent 看,换成 agent 预设的 auth token
	token, err := s.registry.DecryptAuthToken(ctx, ag)
	if err != nil {
		s.logger.ErrorCtx(ctx, "decrypt agent auth token failed", err, map[string]any{"agent_id": ag.ID})
		return nil, fmt.Errorf("decrypt auth token: %w", agent.ErrAgentInternal)
	}

	// 5) 拼转发请求。endpoint_url 已经在 CreateAgent 时过了 SSRF 校验。
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ag.EndpointURL, bytes.NewReader(rpcBody))
	if err != nil {
		return nil, fmt.Errorf("build upstream req: %w: %w", err, agent.ErrAgentInternal)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// 让 agent 知道谁在调,用于审计。不用于权限(权限 agent 无法验证,只能信任 Synapse)。
	req.Header.Set("X-Synapse-Org-ID", fmt.Sprintf("%d", orgID))

	// 5) 超时:context deadline 是真正的上限;httpClient 无全局 Timeout。
	if ag.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(ag.TimeoutSeconds)*time.Second)
		defer cancel()
		req = req.WithContext(ctx)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		// 分两类:超时 vs 其它。上层可能想按超时做不同处理(重试 / 告警)。
		if errors.Is(err, context.DeadlineExceeded) {
			s.logger.WarnCtx(ctx, "mcp proxy: agent timeout", map[string]any{"agent_id": ag.ID, "timeout_s": ag.TimeoutSeconds})
			return nil, fmt.Errorf("agent timeout: %w", agent.ErrChatUpstreamTimeout)
		}
		s.logger.WarnCtx(ctx, "mcp proxy: agent unreachable", map[string]any{"agent_id": ag.ID, "err": err.Error()})
		return nil, fmt.Errorf("agent unreachable: %w", agent.ErrChatUpstreamUnreachable)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read upstream body: %w: %w", err, agent.ErrAgentInternal)
	}

	// agent 返 4xx/5xx:按"上游不可用"处理。JSON-RPC 规范 2.0 也允许用 HTTP 200 + body error code,
	// 所以此处只处理 HTTP 异常状态;200 + body 里 error 属于业务层错误,透传给调用方让 LLM 读懂。
	if resp.StatusCode >= 400 {
		ct := resp.Header.Get("Content-Type")
		snippet := string(respBody)
		if len(snippet) > 500 {
			snippet = snippet[:500] + "..."
		}
		s.logger.WarnCtx(ctx, "mcp proxy: agent non-2xx", map[string]any{
			"agent_id": ag.ID, "status": resp.StatusCode, "content_type": ct, "body_snippet": snippet,
		})
		return nil, fmt.Errorf("agent returned %d: %w", resp.StatusCode, agent.ErrChatUpstreamUnreachable)
	}

	// 确保上游确实返 JSON —— 某些 agent 实现 500 掉后 HTTP/1.1 可能带 text/html;防止把错页返给 LLM 当结果。
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(strings.ToLower(ct), "json") {
		s.logger.WarnCtx(ctx, "mcp proxy: agent non-json response", map[string]any{"agent_id": ag.ID, "content_type": ct})
		return nil, fmt.Errorf("agent returned non-JSON content-type=%s: %w", ct, agent.ErrChatUpstreamUnreachable)
	}

	return respBody, nil
}
