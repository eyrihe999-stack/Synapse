// chat_service.go 对话服务:session 管理、消息组装、上游转发、SSE 流式。
package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/agent"
	"github.com/eyrihe999-stack/Synapse/internal/agent/dto"
	"github.com/eyrihe999-stack/Synapse/internal/agent/model"
	"github.com/eyrihe999-stack/Synapse/internal/agent/repository"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
)

// SSEWriter 是 chat handler 层传入的 SSE 写入器接口。
type SSEWriter interface {
	// WriteEvent 写一个 SSE event 到客户端。
	WriteEvent(event, data string) error
	// Flush 刷新缓冲区。
	Flush()
}

// ChatService 管理对话 session、消息组装和上游转发。
type ChatService interface {
	// Chat 非流式对话。
	Chat(ctx context.Context, req ChatServiceRequest) (*dto.ChatResponse, error)
	// ChatStream 流式对话,通过 SSEWriter 实时写入 SSE 事件。返回 session_id。
	ChatStream(ctx context.Context, req ChatServiceRequest, writer SSEWriter) (string, error)
	// Session 管理
	ListSessions(ctx context.Context, orgID, userID, agentID uint64, page, size int) ([]dto.SessionResponse, int64, error)
	GetSession(ctx context.Context, sessionID string, userID uint64) (*dto.SessionResponse, error)
	GetSessionMessages(ctx context.Context, sessionID string, userID uint64, page, size int) ([]dto.MessageResponse, int64, error)
	DeleteSession(ctx context.Context, sessionID string, userID uint64) error
}

// ChatServiceRequest 对话请求上下文。
type ChatServiceRequest struct {
	OrgID        uint64
	OrgSlug      string
	CallerUserID uint64
	CallerRole   string
	OwnerUID     uint64
	AgentSlug    string
	Message      string
	SessionID    string
	Stream       bool
}

type chatService struct {
	cfg          Config
	registrySvc  RegistryService
	publishSvc   PublishService
	repo         repository.Repository
	rateLimitSvc RateLimitService
	orgPort      OrgPort
	logger       logger.LoggerInterface
	httpClient   *http.Client
}

// NewChatService 构造 ChatService。
func NewChatService(
	cfg Config,
	registrySvc RegistryService,
	publishSvc PublishService,
	repo repository.Repository,
	rateLimitSvc RateLimitService,
	orgPort OrgPort,
	log logger.LoggerInterface,
) ChatService {
	return &chatService{
		cfg:          cfg,
		registrySvc:  registrySvc,
		publishSvc:   publishSvc,
		repo:         repo,
		rateLimitSvc: rateLimitSvc,
		orgPort:      orgPort,
		logger:       log,
		httpClient: &http.Client{
			// 不设全局 Timeout,超时由每次请求的 context deadline 控制(ag.TimeoutSeconds)。
			// Transport 由 agent.NewGuardedTransport 构造,内置 SSRF 防护:
			//   - Dialer.ControlContext 在 DNS 解析后、connect 前拦禁用 IP(防 DNS rebinding)。
			//   - 连接池参数(MaxIdleConns/PerHost/MaxConnsPerHost)沿用原默认值。
			// CheckRedirect 禁所有 redirect,避免 upstream 返回 302 指向元数据地址绕过校验。
			Transport:     agent.NewGuardedTransport(cfg.AllowPrivateEndpoints),
			CheckRedirect: agent.NoRedirectPolicy,
		},
	}
}

// ─── Chat (非流式) ──────────────────────────────────────────────────────────

// Chat 执行非流式对话:准备 session、组装消息、转发上游并返回完整响应。
// 可能返回 ErrAgentInternal、ErrChatUpstreamTimeout、ErrChatUpstreamUnreachable 等错误。
func (s *chatService) Chat(ctx context.Context, req ChatServiceRequest) (*dto.ChatResponse, error) {
	ag, session, err := s.prepareChat(ctx, req)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, err
	}

	// 存储用户消息
	userMsg := &model.AgentMessage{
		SessionID: session.SessionID,
		Role:      agent.RoleUser,
		Content:   req.Message,
	}
	if err := s.repo.CreateMessage(ctx, userMsg); err != nil {
		s.logger.ErrorCtx(ctx, "save user message failed", err, map[string]any{"session_id": session.SessionID})
		return nil, fmt.Errorf("save user message: %w: %w", err, agent.ErrAgentInternal)
	}

	// 组装上游请求
	messages, err := s.buildUpstreamMessages(ctx, ag, session, req.Message)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}

	// 构建 HTTP 请求
	upstreamReq, err := s.buildHTTPRequest(ctx, ag, session, messages, false, req)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}

	// 执行请求
	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(ag.TimeoutSeconds)*time.Second)
	defer cancel()
	upstreamReq = upstreamReq.WithContext(timeoutCtx)

	resp, err := s.httpClient.Do(upstreamReq)
	if err != nil {
		if timeoutCtx.Err() == context.DeadlineExceeded {
			s.logger.WarnCtx(ctx, "upstream timeout", map[string]any{"agent_id": ag.ID})
			return nil, fmt.Errorf("upstream timeout: %w", agent.ErrChatUpstreamTimeout)
		}
		s.logger.ErrorCtx(ctx, "upstream request failed", err, map[string]any{"agent_id": ag.ID})
		return nil, fmt.Errorf("upstream unreachable: %w", agent.ErrChatUpstreamUnreachable)
	}
	//sayso-lint:ignore defer-err
	defer resp.Body.Close()

	// 解析响应
	content, err := s.parseUpstreamResponse(ctx, resp)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}

	// 存储 assistant 消息。client 可能在收到上游响应后立刻断开,
	// 此时 ctx 已 cancel,落库要走 detached ctx,否则 GORM 会立刻 fail 丢消息。
	saveCtx, saveCancel := detachedDBCtx(ctx)
	defer saveCancel()
	assistantMsg := &model.AgentMessage{
		SessionID: session.SessionID,
		Role:      agent.RoleAssistant,
		Content:   content,
	}
	if err := s.repo.CreateMessage(saveCtx, assistantMsg); err != nil {
		s.logger.ErrorCtx(ctx, "save assistant message failed", err, nil)
	}

	// 自动设置 session title
	s.autoSetTitle(saveCtx, session, req.Message)

	return &dto.ChatResponse{
		SessionID: session.SessionID,
		Message: dto.ChatMessage{
			Role:    agent.RoleAssistant,
			Content: content,
		},
	}, nil
}

// ─── ChatStream (流式) ──────────────────────────────────────────────────────

// ChatStream 执行流式对话:准备 session、组装消息、转发上游 SSE 事件并通过 writer 实时写入。
// 返回 session_id 和可能的错误,包括 ErrAgentInternal、ErrChatUpstreamTimeout 等。
func (s *chatService) ChatStream(ctx context.Context, req ChatServiceRequest, writer SSEWriter) (string, error) {
	ag, session, err := s.prepareChat(ctx, req)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return "", err
	}

	// 存储用户消息
	userMsg := &model.AgentMessage{
		SessionID: session.SessionID,
		Role:      agent.RoleUser,
		Content:   req.Message,
	}
	if err := s.repo.CreateMessage(ctx, userMsg); err != nil {
		s.logger.ErrorCtx(ctx, "save user message failed in stream", err, map[string]any{"session_id": session.SessionID})
		return "", fmt.Errorf("save user message: %w: %w", err, agent.ErrAgentInternal)
	}

	// 组装上游请求
	messages, err := s.buildUpstreamMessages(ctx, ag, session, req.Message)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return "", err
	}


	upstreamReq, err := s.buildHTTPRequest(ctx, ag, session, messages, true, req)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return "", err
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(ag.TimeoutSeconds)*time.Second)
	defer cancel()
	upstreamReq = upstreamReq.WithContext(timeoutCtx)

	resp, err := s.httpClient.Do(upstreamReq)
	if err != nil {
		if timeoutCtx.Err() == context.DeadlineExceeded {
			s.logger.WarnCtx(ctx, "upstream stream timeout", map[string]any{"agent_id": ag.ID})
			return "", fmt.Errorf("upstream timeout: %w", agent.ErrChatUpstreamTimeout)
		}
		s.logger.ErrorCtx(ctx, "upstream stream request failed", err, nil)
		return "", fmt.Errorf("upstream unreachable: %w", agent.ErrChatUpstreamUnreachable)
	}
	//sayso-lint:ignore defer-err
	defer resp.Body.Close()

	// 先发 session_id 事件,让客户端知道 session
	if err := writer.WriteEvent("session", session.SessionID); err != nil {
		s.logger.WarnCtx(ctx, "write session event failed, client may have disconnected", map[string]any{"session_id": session.SessionID, "error": err.Error()})
		return session.SessionID, nil
	}

	// 流式转发 SSE
	var contentBuf bytes.Buffer
	clientGone := false
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), agent.MaxSSELineBytes)
	for scanner.Scan() {
		// 客户端断开(请求 ctx 被 cancel)时立即退出,避免继续消耗上游 token。
		if err := ctx.Err(); err != nil {
			s.logger.WarnCtx(ctx, "client disconnected during stream, aborting upstream", map[string]any{
				"session_id": session.SessionID, "error": err.Error(),
			})
			clientGone = true
			break
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			//sayso-lint:ignore err-swallow
			writer.WriteEvent("done", "{}")
			writer.Flush()
			break
		}
		// 解析 delta content
		content := extractDeltaContent(data)
		if content != "" {
			if contentBuf.Len()+len(content) > agent.MaxStreamContentBytes {
				s.logger.WarnCtx(ctx, "stream content exceeds limit, truncating", map[string]any{
					"session_id": session.SessionID, "limit": agent.MaxStreamContentBytes,
				})
			} else {
				contentBuf.WriteString(content)
			}
		}
		// 原样转发 SSE 给客户端;写失败意味着客户端已断开,停止继续读取上游。
		if err := writer.WriteEvent("chunk", data); err != nil {
			s.logger.WarnCtx(ctx, "write chunk failed, aborting stream", map[string]any{
				"session_id": session.SessionID, "error": err.Error(),
			})
			
			clientGone = true
			break
		}
		writer.Flush()
	}
	if err := scanner.Err(); err != nil && !clientGone {
		s.logger.WarnCtx(ctx, "SSE scanner error", map[string]any{"session_id": session.SessionID, "error": err.Error()})
	}

	// 存储完整 assistant 消息。client 中途断开时 ctx 已 cancel,
	// 落库切到 detached ctx,避免已生成的回答因 ctx 取消被丢掉。
	saveCtx, saveCancel := detachedDBCtx(ctx)
	defer saveCancel()
	if contentBuf.Len() > 0 {
		assistantMsg := &model.AgentMessage{
			SessionID: session.SessionID,
			Role:      agent.RoleAssistant,
			Content:   contentBuf.String(),
		}
		if err := s.repo.CreateMessage(saveCtx, assistantMsg); err != nil {
			s.logger.ErrorCtx(ctx, "save streamed assistant message failed", err, nil)
		}
	}

	s.autoSetTitle(saveCtx, session, req.Message)
	return session.SessionID, nil
}

// ─── Session 管理 ────────────────────────────────────────────────────────────

// ListSessions 分页列出指定用户在某 agent 下的所有对话 session。
// 返回 ErrAgentInternal 当数据库查询失败时。
func (s *chatService) ListSessions(ctx context.Context, orgID, userID, agentID uint64, page, size int) ([]dto.SessionResponse, int64, error) {
	if page <= 0 {
		page = 1
	}
	if size <= 0 || size > agent.MaxPageSize {
		size = agent.DefaultPageSize
	}
	list, total, err := s.repo.ListSessionsByUserAgent(ctx, orgID, userID, agentID, page, size)
	if err != nil {
		s.logger.ErrorCtx(ctx, "list sessions failed", err, map[string]any{"org_id": orgID, "user_id": userID, "agent_id": agentID})
		return nil, 0, fmt.Errorf("list sessions: %w: %w", err, agent.ErrAgentInternal)
	}
	out := make([]dto.SessionResponse, 0, len(list))
	for _, s := range list {
		out = append(out, sessionToDTO(s))
	}
	return out, total, nil
}

// GetSession 根据 sessionID 获取单个 session 详情。
// 返回 ErrSessionNotFound 或 ErrSessionNotBelongToUser。
func (s *chatService) GetSession(ctx context.Context, sessionID string, userID uint64) (*dto.SessionResponse, error) {
	session, err := s.repo.FindSessionByID(ctx, sessionID)
	if err != nil {
		s.logger.WarnCtx(ctx, "get session not found", map[string]any{"session_id": sessionID, "user_id": userID})
		return nil, fmt.Errorf("session not found: %w", agent.ErrSessionNotFound)
	}
	if session.UserID != userID {
		s.logger.WarnCtx(ctx, "get session permission denied", map[string]any{"session_id": sessionID, "user_id": userID, "owner_id": session.UserID})
		return nil, fmt.Errorf("session not belong to user: %w", agent.ErrSessionNotBelongToUser)
	}
	resp := sessionToDTO(session)
	return &resp, nil
}

// GetSessionMessages 分页获取某 session 下的消息列表。
// 返回 ErrSessionNotFound、ErrSessionNotBelongToUser 或 ErrAgentInternal。
func (s *chatService) GetSessionMessages(ctx context.Context, sessionID string, userID uint64, page, size int) ([]dto.MessageResponse, int64, error) {
	if page <= 0 {
		page = 1
	}
	if size <= 0 || size > agent.MaxPageSize {
		size = agent.DefaultPageSize
	}
	session, err := s.repo.FindSessionByID(ctx, sessionID)
	if err != nil {
		s.logger.WarnCtx(ctx, "get session messages: session not found", map[string]any{"session_id": sessionID})
		return nil, 0, fmt.Errorf("session not found: %w", agent.ErrSessionNotFound)
	}
	if session.UserID != userID {
		s.logger.WarnCtx(ctx, "get session messages: permission denied", map[string]any{"session_id": sessionID, "user_id": userID, "owner_id": session.UserID})
		return nil, 0, fmt.Errorf("session not belong to user: %w", agent.ErrSessionNotBelongToUser)
	}
	list, total, err := s.repo.ListMessagesBySession(ctx, sessionID, page, size)
	if err != nil {
		s.logger.ErrorCtx(ctx, "list messages by session failed", err, map[string]any{"session_id": sessionID})
		return nil, 0, fmt.Errorf("list messages: %w: %w", err, agent.ErrAgentInternal)
	}
	out := make([]dto.MessageResponse, 0, len(list))
	for _, m := range list {
		out = append(out, messageToDTO(m))
	}
	return out, total, nil
}

// DeleteSession 删除指定 session 及其所有消息。
// 返回 ErrSessionNotFound、ErrSessionNotBelongToUser 或 ErrAgentInternal。
func (s *chatService) DeleteSession(ctx context.Context, sessionID string, userID uint64) error {
	session, err := s.repo.FindSessionByID(ctx, sessionID)
	if err != nil {
		s.logger.WarnCtx(ctx, "delete session: session not found", map[string]any{"session_id": sessionID})
		return fmt.Errorf("session not found: %w", agent.ErrSessionNotFound)
	}
	if session.UserID != userID {
		s.logger.WarnCtx(ctx, "delete session: permission denied", map[string]any{"session_id": sessionID, "user_id": userID, "owner_id": session.UserID})
		return fmt.Errorf("session not belong to user: %w", agent.ErrSessionNotBelongToUser)
	}
	// 事务内先删消息再删 session,保证原子性
	if err := s.repo.WithTx(ctx, func(tx repository.Repository) error {
		if err := tx.DeleteMessagesBySession(ctx, sessionID); err != nil {
			return fmt.Errorf("delete messages: %w", err)
		}
		if err := tx.DeleteSession(ctx, sessionID); err != nil {
			return fmt.Errorf("delete session: %w", err)
		}
		return nil
	}); err != nil {
		s.logger.ErrorCtx(ctx, "delete session tx failed", err, map[string]any{"session_id": sessionID})
		return fmt.Errorf("delete session: %w: %w", err, agent.ErrAgentInternal)
	}
	return nil
}

// ─── 内部方法 ────────────────────────────────────────────────────────────────

// prepareChat 执行对话前的公共准备:解析 agent、校验发布、限流、获取/创建 session。
// 返回 agent 和 session,可能返回 ErrAgentInvalidRequest、ErrSessionNotFound、
// ErrSessionNotBelongToUser、ErrChatRateLimited、ErrAgentInternal 等错误。
func (s *chatService) prepareChat(ctx context.Context, req ChatServiceRequest) (*model.Agent, *model.AgentSession, error) {
	// 消息长度校验(按 rune 计数,对中文友好)
	if len([]rune(req.Message)) > agent.MaxChatMessageLength {
		return nil, nil, fmt.Errorf("message too long (max %d chars): %w", agent.MaxChatMessageLength, agent.ErrAgentInvalidRequest)
	}
	// 解析 agent
	ag, err := s.registrySvc.LoadAgentByOwnerSlug(ctx, req.OwnerUID, req.AgentSlug)
	if err != nil {
		s.logger.WarnCtx(ctx, "prepareChat: load agent failed", map[string]any{"owner_uid": req.OwnerUID, "agent_slug": req.AgentSlug, "error": err.Error()})
		//sayso-lint:ignore sentinel-wrap
		return nil, nil, err
	}
	// 检查状态
	if ag.Status != model.AgentStatusActive {
		s.logger.WarnCtx(ctx, "prepareChat: agent not active", map[string]any{"agent_id": ag.ID, "status": ag.Status})
		return nil, nil, fmt.Errorf("agent not active: %w", agent.ErrAgentInvalidRequest)
	}
	// 检查发布
	//sayso-lint:ignore err-swallow
	if _, err := s.publishSvc.GetActivePublish(ctx, ag.ID, req.OrgID); err != nil {
		s.logger.WarnCtx(ctx, "prepareChat: agent not published in org", map[string]any{"agent_id": ag.ID, "org_id": req.OrgID, "error": err.Error()})
		//sayso-lint:ignore sentinel-wrap
		return nil, nil, err
	}
	// 限流
	if err := s.rateLimitSvc.CheckChatLimit(ctx, req.CallerUserID, ag.ID); err != nil {
		s.logger.WarnCtx(ctx, "prepareChat: rate limited", map[string]any{"user_id": req.CallerUserID, "agent_id": ag.ID})
		//sayso-lint:ignore sentinel-wrap
		return nil, nil, err
	}
	// 获取或创建 session
	var session *model.AgentSession
	if req.SessionID != "" {
		session, err = s.repo.FindSessionByID(ctx, req.SessionID)
		if err != nil {
			s.logger.WarnCtx(ctx, "prepareChat: session not found", map[string]any{"session_id": req.SessionID})
			return nil, nil, fmt.Errorf("session not found: %w", agent.ErrSessionNotFound)
		}
		if session.UserID != req.CallerUserID {
			s.logger.WarnCtx(ctx, "prepareChat: session not belong to user", map[string]any{"session_id": req.SessionID, "user_id": req.CallerUserID, "owner_id": session.UserID})
			return nil, nil, fmt.Errorf("session not belong to user: %w", agent.ErrSessionNotBelongToUser)
		}
	} else {
		session = &model.AgentSession{
			SessionID:   agent.GenerateSessionID(),
			OrgID:       req.OrgID,
			UserID:      req.CallerUserID,
			AgentID:     ag.ID,
			ContextMode: ag.ContextMode,
		}
		if err := s.repo.CreateSession(ctx, session); err != nil {
			s.logger.ErrorCtx(ctx, "prepareChat: create session failed", err, map[string]any{"agent_id": ag.ID, "user_id": req.CallerUserID})
			return nil, nil, fmt.Errorf("create session: %w: %w", err, agent.ErrAgentInternal)
		}
	}
	return ag, session, nil
}

// buildUpstreamMessages 根据 context_mode 组装 OpenAI 格式的 messages 数组。
// stateful 模式只发送当前消息;stateless 模式加载历史消息并追加当前消息。
// 当历史消息加载失败时会降级为只发送当前消息,返回 nil error。
func (s *chatService) buildUpstreamMessages(ctx context.Context, ag *model.Agent, session *model.AgentSession, currentMessage string) ([]map[string]string, error) {
	if session.ContextMode == agent.ContextModeStateful {
		// stateful: 只发当前消息
		return []map[string]string{
			{"role": agent.RoleUser, "content": currentMessage},
		}, nil
	}
	// stateless: 加载历史消息 + 当前消息
	history, err := s.repo.GetRecentMessages(ctx, session.SessionID, ag.MaxContextRounds)
	if err != nil {
		s.logger.ErrorCtx(ctx, "load history messages failed", err, nil)
		// 降级:只发当前消息
		return []map[string]string{
			{"role": agent.RoleUser, "content": currentMessage},
		}, nil
	}
	messages := make([]map[string]string, 0, len(history)+1)
	for _, m := range history {
		messages = append(messages, map[string]string{
			"role":    m.Role,
			"content": m.Content,
		})
	}
	// 当前消息已经在 prepareChat 后存入 DB,但 GetRecentMessages 可能已经包含它
	// 检查最后一条是否是当前消息,避免重复
	if len(messages) > 0 {
		last := messages[len(messages)-1]
		if last["role"] == agent.RoleUser && last["content"] == currentMessage {
			return messages, nil
		}
	}
	messages = append(messages, map[string]string{
		"role":    agent.RoleUser,
		"content": currentMessage,
	})
	return messages, nil
}

// buildHTTPRequest 构建发送给上游 agent 的 HTTP 请求。
// 包含 JSON body 序列化、认证 token 解密和 header 设置。
// 返回 ErrAgentInternal(序列化/请求构建失败)或 ErrAgentCryptoFailed(token 解密失败)。
func (s *chatService) buildHTTPRequest(ctx context.Context, ag *model.Agent, session *model.AgentSession, messages []map[string]string, stream bool, csReq ChatServiceRequest) (*http.Request, error) {
	body := map[string]any{
		"messages": messages,
		"stream":   stream,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		s.logger.ErrorCtx(ctx, "marshal request body failed", err, map[string]any{"agent_id": ag.ID})
		return nil, fmt.Errorf("marshal request body: %w: %w", err, agent.ErrAgentInternal)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ag.EndpointURL, bytes.NewReader(bodyBytes))
	if err != nil {
		s.logger.ErrorCtx(ctx, "create http request failed", err, map[string]any{"endpoint": ag.EndpointURL})
		return nil, fmt.Errorf("create http request: %w: %w", err, agent.ErrAgentInternal)
	}

	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}

	// Bearer Token
	token, err := s.registrySvc.DecryptAuthToken(ctx, ag)
	if err != nil {
		s.logger.ErrorCtx(ctx, "decrypt auth token failed", err, map[string]any{"agent_id": ag.ID})
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	// stateful 模式传 session_id
	if session.ContextMode == agent.ContextModeStateful {
		req.Header.Set("X-Synapse-Session-ID", session.SessionID)
	}

	// org / user 上下文头。agent 不用来鉴权,只是让 agent 能在日志 / 限流 / 引用里
	// 识别"哪个 org 的哪个用户在问"。未设置的字段不写 header。
	if csReq.OrgID != 0 {
		req.Header.Set("X-Synapse-Org-ID", strconv.FormatUint(csReq.OrgID, 10))
	}
	if csReq.OrgSlug != "" {
		req.Header.Set("X-Synapse-Org-Slug", csReq.OrgSlug)
	}
	if csReq.CallerUserID != 0 {
		req.Header.Set("X-Synapse-User-ID", strconv.FormatUint(csReq.CallerUserID, 10))
	}

	return req, nil
}

// parseUpstreamResponse 解析上游非流式响应。
// 尝试 OpenAI choices 格式 → 简单 JSON 格式 → 纯文本回退。
// 返回 ErrChatUpstreamUnreachable 当读取 body 失败或上游返回非 2xx 时。
func (s *chatService) parseUpstreamResponse(ctx context.Context, resp *http.Response) (string, error) {
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, agent.MaxUpstreamResponseBytes))
	if err != nil {
		s.logger.ErrorCtx(ctx, "read upstream response body failed", err, map[string]any{"status": resp.StatusCode})
		return "", fmt.Errorf("read upstream response: %w: %w", err, agent.ErrChatUpstreamUnreachable)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.logger.WarnCtx(ctx, "upstream returned non-2xx", map[string]any{
			"status": resp.StatusCode, "body": string(bodyBytes),
		})
		return "", fmt.Errorf("upstream error status %d: %w", resp.StatusCode, agent.ErrChatUpstreamUnreachable)
	}

	// 尝试解析 OpenAI 格式
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(bodyBytes, &result); err == nil && len(result.Choices) > 0 {
		return result.Choices[0].Message.Content, nil
	}

	// 回退:尝试解析纯文本或其他格式
	var simpleResult struct {
		Content string `json:"content"`
		Message string `json:"message"`
		Text    string `json:"text"`
	}
	if err := json.Unmarshal(bodyBytes, &simpleResult); err == nil {
		if simpleResult.Content != "" {
			return simpleResult.Content, nil
		}
		if simpleResult.Message != "" {
			return simpleResult.Message, nil
		}
		if simpleResult.Text != "" {
			return simpleResult.Text, nil
		}
	}

	// 最后回退:直接当字符串
	return string(bodyBytes), nil
}

// extractDeltaContent 从 SSE data JSON 中提取 OpenAI 格式的 delta content 字段。
// 解析失败或无 choices 时返回空字符串。
func extractDeltaContent(data string) string {
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return ""
	}
	if len(chunk.Choices) > 0 {
		return chunk.Choices[0].Delta.Content
	}
	return ""
}

// detachedDBCtx 派生一个不被 parent cancel 影响、带短 timeout 的 ctx,
// 用于流末/同步末的落库。client 在拿到上游响应后断开时,parent ctx 已 cancel,
// 直接用它落库会被 GORM 立刻 fail,导致已生成的 assistant 内容丢失。
// 5 秒足够覆盖一次正常的 INSERT/UPDATE,超过表示 DB 异常,该失败就失败。
func detachedDBCtx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
}

// autoSetTitle 在首次对话后自动设置 session title(取用户消息前 N 个字符)。
// 如果 session 已有 title 则跳过。更新失败只记录日志不返回错误。
func (s *chatService) autoSetTitle(ctx context.Context, session *model.AgentSession, userMessage string) {
	if session.Title != "" {
		return
	}
	title := userMessage
	runes := []rune(title)
	if len(runes) > agent.MaxSessionTitleLength {
		title = string(runes[:agent.MaxSessionTitleLength])
	}
	if err := s.repo.UpdateSessionTitle(ctx, session.SessionID, title); err != nil {
		s.logger.ErrorCtx(ctx, "auto set session title failed", err, nil)
	}
}
