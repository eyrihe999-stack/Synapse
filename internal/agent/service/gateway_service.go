// gateway_service.go 调用网关 service:JSON-RPC 请求转发 + HMAC 签名 + 响应转回。
//
// 核心流程(对应 plan 第 5.4 节 13 步):
//  1. 调用方已完成 JWT + OrgContext 校验(由 handler 层完成)
//  2. URL → agent:校验 status、health、publish binding
//  3. Body → method:解析 JSON-RPC body 抽 method 名,查 method,校验 visibility / transport
//  4. 四层限流检查
//  5. 生成 invocation_id,audit.Begin
//  6. 注入 _sayso 扩展字段到 body 顶层
//  7. 读取 + 解密 secret,计算 HMAC 签名
//  8. 构造 HTTP 请求,加 X-Sayso-* headers
//  9. 按 transport 分支:http 同步 / sse 流式
//  10. 响应回传给 writer
//  11. 异步 audit.Finish + 异步 audit.RecordPayload
//
// 取消:
//   - 进入 invoke 时 CancelRegistry.Register,context 挂钩
//   - Redis pub/sub 命中触发 cancel;兜底轮询 flag 每 2 秒一次
package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/agent"
	"github.com/eyrihe999-stack/Synapse/internal/agent/model"
	"github.com/eyrihe999-stack/Synapse/internal/agent/repository"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
)

// InvokeRequest 是 GatewayService.Invoke 的输入。
//
// Body 是原始 JSON-RPC body(字节流);Writer 是响应回写通道。
// 对于 http transport 调用方应该传一个 bytes.Buffer;对于 sse transport 调用方应该
// 传 gin.ResponseWriter 并设置 SSE header。
type InvokeRequest struct {
	OrgID        uint64
	OrgSlug      string
	CallerUserID uint64
	CallerRole   string
	OwnerUID     uint64
	AgentSlug    string
	Body         []byte
	ClientIP     string
	UserAgent    string
	TraceID      string
}

// InvokeResult 是 http 同步响应的结果(SSE 路径则直接写入 Writer 不返回此对象)。
type InvokeResult struct {
	InvocationID string
	StatusCode   int
	RespBody     []byte
	Transport    string
}

// GatewayService 定义调用转发的对外接口。
//sayso-lint:ignore interface-pollution
type GatewayService interface {
	// InvokeHTTP 执行 http transport 的同步调用。
	// 返回完整响应 body 和状态码,由 handler 写给客户端。
	InvokeHTTP(ctx context.Context, req InvokeRequest) (*InvokeResult, error)
	// InvokeSSE 执行 sse transport 的流式调用。
	// writer 应由 handler 传入(已设置 SSE headers 的 http.ResponseWriter +
	// http.Flusher)。invocationID 通过 out 参数返回给 handler 用于取消链接等。
	InvokeSSE(ctx context.Context, req InvokeRequest, writer SSEWriter) (string, error)
}

// SSEWriter 抽象出 SSE 事件写入通道。
type SSEWriter interface {
	// WriteEvent 写入一条 SSE 事件。eventType 为 chunk/done/error。
	WriteEvent(eventType string, data []byte) error
	// Flush 触发底层 http.Flusher。
	Flush()
}

// gatewayService 是 GatewayService 的实现。
type gatewayService struct {
	cfg          Config
	registry     RegistryService
	publishes    PublishService
	orgPort      OrgPort
	ratelimit    RateLimitService
	audit        AuditService
	cancels      *CancelRegistry
	httpClient   *http.Client
	logger       logger.LoggerInterface
}

// NewGatewayService 构造 gateway service。
//
// httpClient 若为 nil,会构造一个全局默认客户端(MaxIdleConns=100, PerHost=10)。
func NewGatewayService(
	cfg Config,
	registry RegistryService,
	publishes PublishService,
	orgPort OrgPort,
	ratelimit RateLimitService,
	audit AuditService,
	cancels *CancelRegistry,
	httpClient *http.Client,
	log logger.LoggerInterface,
) GatewayService {
	if httpClient == nil {
		//sayso-lint:ignore http-timeout
		httpClient = &http.Client{ // 不设 Client.Timeout,改用每请求 context.WithTimeout,避免 SSE 流被截断
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		}
	}
	return &gatewayService{
		cfg:        cfg,
		registry:   registry,
		publishes:  publishes,
		orgPort:    orgPort,
		ratelimit:  ratelimit,
		audit:      audit,
		cancels:    cancels,
		httpClient: httpClient,
		logger:     log,
	}
}

// InvokeHTTP 执行 http transport 的同步调用。
//
// 错误:解析/校验失败返回对应 sentinel(ErrAgentNotFound、ErrInvokeMethodNotDeclared、ErrMethodTransportUnsupported 等);
// 上游不可达返回 ErrGatewayAgentUnreachable,超时返回 ErrGatewayAgentTimeout,被取消返回 ErrInvocationCanceled。
func (s *gatewayService) InvokeHTTP(ctx context.Context, req InvokeRequest) (*InvokeResult, error) {
	//sayso-lint:ignore err-swallow
	a, m, _, err := s.resolveAgentAndMethod(ctx, req) // publish 返回值此路径不需要
	if err != nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, err
	}
	if m.Transport != agent.TransportHTTP {
		s.logger.WarnCtx(ctx, "method transport mismatch", map[string]any{"agent_id": a.ID, "method_name": m.MethodName, "transport": m.Transport})
		return nil, fmt.Errorf("method transport mismatch: %w", agent.ErrMethodTransportUnsupported)
	}
	invocationID, startedAt, cleanup, err := s.beginInvocation(ctx, req, a, m)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, err
	}
	defer cleanup()

	// 构造上下游 context 并挂钩取消
	timeout := time.Duration(a.TimeoutSeconds) * time.Second
	upstreamCtx, upstreamCancel := context.WithTimeout(ctx, timeout)
	defer upstreamCancel()
	release := s.cancels.Register(invocationID, upstreamCancel)
	defer release()

	// 签名 + 构造请求 body
	bodyWithSayso, signedReq, err := s.buildUpstreamRequest(upstreamCtx, a, m, req, invocationID)
	if err != nil {
		s.finishInvocation(ctx, invocationID, startedAt, agent.InvocationStatusFailed, err, 0, 0)
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, err
	}

	// 启动兜底轮询 goroutine,监测 Redis cancel flag
	pollCtx, pollCancel := context.WithCancel(upstreamCtx)
	defer pollCancel()
	//sayso-lint:ignore bare-goroutine
	go s.cancelPollLoop(pollCtx, invocationID, upstreamCancel) // 绑定请求 context,defer pollCancel 保证退出

	// 执行
	resp, err := s.httpClient.Do(signedReq)
	if err != nil {
		status := agent.InvocationStatusFailed
		var sentinel error = agent.ErrGatewayAgentUnreachable
		if errors.Is(err, context.DeadlineExceeded) {
			status = agent.InvocationStatusTimeout
			sentinel = agent.ErrGatewayAgentTimeout
		} else if errors.Is(err, context.Canceled) {
			status = agent.InvocationStatusCanceled
			sentinel = agent.ErrInvocationCanceled
		}
		s.finishInvocation(ctx, invocationID, startedAt, status, sentinel, len(bodyWithSayso), 0)
		s.logger.ErrorCtx(ctx, "upstream http call failed", err, map[string]any{"invocation_id": invocationID, "agent_id": a.ID})
		return nil, fmt.Errorf("upstream: %w: %w", err, sentinel)
	}
	//sayso-lint:ignore err-swallow
	defer func() { _ = resp.Body.Close() }() // close 错误无处理

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		s.finishInvocation(ctx, invocationID, startedAt, agent.InvocationStatusFailed, agent.ErrGatewayAgentUnreachable, len(bodyWithSayso), 0)
		s.logger.ErrorCtx(ctx, "read upstream response failed", err, map[string]any{"invocation_id": invocationID, "agent_id": a.ID})
		return nil, fmt.Errorf("read resp: %w: %w", err, agent.ErrGatewayAgentUnreachable)
	}

	finalStatus := agent.InvocationStatusSucceeded
	if resp.StatusCode == http.StatusUnauthorized {
		finalStatus = agent.InvocationStatusFailed
		s.logger.WarnCtx(ctx, "upstream agent returned 401", map[string]any{"invocation_id": invocationID, "agent_id": a.ID})
	} else if resp.StatusCode >= 500 {
		finalStatus = agent.InvocationStatusFailed
	}
	s.finishInvocation(ctx, invocationID, startedAt, finalStatus, nil, len(bodyWithSayso), len(respBody))
	s.maybeRecordPayload(ctx, req.OrgID, invocationID, startedAt, bodyWithSayso, respBody)

	return &InvokeResult{
		InvocationID: invocationID,
		StatusCode:   resp.StatusCode,
		RespBody:     respBody,
		Transport:    agent.TransportHTTP,
	}, nil
}

// InvokeSSE 执行 sse transport 的流式调用。
// writer 由 handler 传入(已设置 SSE headers)。
//
// 错误:解析/校验失败返回对应 sentinel(ErrAgentNotFound、ErrMethodTransportUnsupported 等);
// 上游不可达返回 ErrGatewayAgentUnreachable,超时返回 ErrGatewayAgentTimeout,被取消返回 ErrInvocationCanceled。
func (s *gatewayService) InvokeSSE(ctx context.Context, req InvokeRequest, writer SSEWriter) (string, error) {
	//sayso-lint:ignore err-swallow
	a, m, _, err := s.resolveAgentAndMethod(ctx, req) // publish 返回值此路径不需要
	if err != nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return "", err
	}
	if m.Transport != agent.TransportSSE {
		s.logger.WarnCtx(ctx, "method transport mismatch", map[string]any{"agent_id": a.ID, "method_name": m.MethodName, "transport": m.Transport})
		return "", fmt.Errorf("method transport mismatch: %w", agent.ErrMethodTransportUnsupported)
	}
	invocationID, startedAt, cleanup, err := s.beginInvocation(ctx, req, a, m)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return "", err
	}
	defer cleanup()

	timeout := time.Duration(a.TimeoutSeconds) * time.Second
	upstreamCtx, upstreamCancel := context.WithTimeout(ctx, timeout)
	defer upstreamCancel()
	release := s.cancels.Register(invocationID, upstreamCancel)
	defer release()

	bodyWithSayso, signedReq, err := s.buildUpstreamRequest(upstreamCtx, a, m, req, invocationID)
	if err != nil {
		s.finishInvocation(ctx, invocationID, startedAt, agent.InvocationStatusFailed, err, 0, 0)
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return invocationID, err
	}

	pollCtx, pollCancel := context.WithCancel(upstreamCtx)
	defer pollCancel()
	//sayso-lint:ignore bare-goroutine
	go s.cancelPollLoop(pollCtx, invocationID, upstreamCancel) // 绑定请求 context,defer pollCancel 保证退出

	resp, err := s.httpClient.Do(signedReq)
	if err != nil {
		status := agent.InvocationStatusFailed
		var sentinel error = agent.ErrGatewayAgentUnreachable
		if errors.Is(err, context.DeadlineExceeded) {
			status = agent.InvocationStatusTimeout
			sentinel = agent.ErrGatewayAgentTimeout
		} else if errors.Is(err, context.Canceled) {
			status = agent.InvocationStatusCanceled
			sentinel = agent.ErrInvocationCanceled
		}
		s.finishInvocation(ctx, invocationID, startedAt, status, sentinel, len(bodyWithSayso), 0)
		//sayso-lint:ignore err-swallow
		_ = writer.WriteEvent("error", []byte(sentinel.Error())) // 写错误事件失败无处理
		writer.Flush()
		s.logger.ErrorCtx(ctx, "upstream sse call failed", err, map[string]any{"invocation_id": invocationID, "agent_id": a.ID})
		return invocationID, fmt.Errorf("upstream: %w: %w", err, sentinel)
	}
	//sayso-lint:ignore err-swallow
	defer func() { _ = resp.Body.Close() }() // close 错误无处理

	// 流式转发:按行读 upstream,每行作为一个 chunk 事件
	var totalResp int
	reader := bufio.NewReader(resp.Body)
	for {
		if upstreamCtx.Err() != nil {
			break
		}
		//sayso-lint:ignore err-shadow
		line, err := reader.ReadBytes('\n') // for-loop 内新声明 err,外层 err 已不再使用
		if len(line) > 0 {
			totalResp += len(line)
			if werr := writer.WriteEvent("chunk", line); werr != nil {
				s.logger.WarnCtx(ctx, "sse write chunk failed", map[string]any{"invocation_id": invocationID, "error": werr.Error()})
				break
			}
			writer.Flush()
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			// 其它错误:把消息透出为 error 事件
			//sayso-lint:ignore err-swallow
			_ = writer.WriteEvent("error", []byte(err.Error())) // 写错误事件失败无处理
			writer.Flush()
			s.finishInvocation(ctx, invocationID, startedAt, agent.InvocationStatusFailed, agent.ErrGatewayAgentUnreachable, len(bodyWithSayso), totalResp)
			s.logger.ErrorCtx(ctx, "sse read upstream failed", err, map[string]any{"invocation_id": invocationID, "agent_id": a.ID})
			return invocationID, fmt.Errorf("sse read: %w: %w", err, agent.ErrGatewayAgentUnreachable)
		}
	}
	//sayso-lint:ignore err-swallow
	_ = writer.WriteEvent("done", []byte("{}")) // done 事件失败无处理
	writer.Flush()

	finalStatus := agent.InvocationStatusSucceeded
	if upstreamCtx.Err() != nil {
		finalStatus = agent.InvocationStatusCanceled
	}
	s.finishInvocation(ctx, invocationID, startedAt, finalStatus, nil, len(bodyWithSayso), totalResp)
	// SSE 不记 payload(流式不适合)
	return invocationID, nil
}

// ─── 内部辅助 ────────────────────────────────────────────────────────────────

// resolveAgentAndMethod 做前置校验(agent 状态 / publish binding / method / visibility),
// 返回用于后续使用的 agent / method / publish。
func (s *gatewayService) resolveAgentAndMethod(ctx context.Context, req InvokeRequest) (*model.Agent, *model.AgentMethod, *model.AgentPublish, error) {
	a, err := s.registry.LoadAgentByOwnerSlug(ctx, req.OwnerUID, req.AgentSlug)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, nil, nil, err
	}
	if a.Status != model.AgentStatusActive {
		s.logger.WarnCtx(ctx, "agent not active", map[string]any{"agent_id": a.ID, "status": a.Status})
		return nil, nil, nil, fmt.Errorf("agent not active: %w", agent.ErrAgentNotFound)
	}
	if a.HealthStatus == model.HealthStatusUnhealthy {
		s.logger.WarnCtx(ctx, "agent unhealthy", map[string]any{"agent_id": a.ID})
		return nil, nil, nil, fmt.Errorf("agent unhealthy: %w", agent.ErrAgentUnhealthy)
	}
	publish, err := s.publishes.GetActivePublish(ctx, a.ID, req.OrgID)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, nil, nil, err
	}
	// 解析 method
	methodName, err := extractJSONRPCMethod(req.Body)
	if err != nil {
		s.logger.WarnCtx(ctx, "jsonrpc method extract failed", map[string]any{"agent_id": a.ID})
		//sayso-lint:ignore sentinel-wrap
		return nil, nil, nil, err
	}
	m, err := s.registry.LoadMethod(ctx, a.ID, methodName)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, nil, nil, err
	}
	if m.Visibility == agent.VisibilityPrivate && a.OwnerUserID != req.CallerUserID {
		s.logger.WarnCtx(ctx, "private method invoke denied", map[string]any{"agent_id": a.ID, "method_name": methodName, "user_id": req.CallerUserID})
		return nil, nil, nil, fmt.Errorf("private method: %w", agent.ErrMethodPrivateInvoke)
	}
	if m.Transport == agent.TransportWS {
		s.logger.WarnCtx(ctx, "ws transport unsupported", map[string]any{"agent_id": a.ID, "method_name": methodName})
		return nil, nil, nil, fmt.Errorf("ws unsupported: %w", agent.ErrMethodTransportUnsupported)
	}
	return a, m, publish, nil
}

// beginInvocation 执行限流 + audit.Begin,返回 invocation_id / startedAt / cleanup 回调。
func (s *gatewayService) beginInvocation(ctx context.Context, req InvokeRequest, a *model.Agent, m *model.AgentMethod) (string, time.Time, func(), error) {
	// 限流
	invocationID := agent.GenerateInvocationID()
	if err := s.ratelimit.CheckAndConsume(ctx, req.CallerUserID, req.OrgID, a.ID, a.RateLimitPerMinute, invocationID); err != nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return "", time.Time{}, func() {}, err
	}
	// 写 invocation 主表(best-effort,失败仅打日志不阻塞)
	now := time.Now().UTC()
	inv := &model.AgentInvocation{
		InvocationID:     invocationID,
		TraceID:          req.TraceID,
		OrgID:            req.OrgID,
		CallerUserID:     req.CallerUserID,
		CallerRoleName:   req.CallerRole,
		AgentID:          a.ID,
		AgentOwnerUserID: a.OwnerUserID,
		MethodName:       m.MethodName,
		Transport:        m.Transport,
		StartedAt:        now,
		Status:           agent.InvocationStatusRunning,
		ClientIP:         req.ClientIP,
		UserAgent:        req.UserAgent,
	}
	if err := s.audit.Begin(ctx, inv); err != nil {
		s.logger.ErrorCtx(ctx, "audit begin failed (continue anyway)", err, map[string]any{"invocation_id": invocationID})
	}
	return invocationID, now, func() {}, nil
}

// buildUpstreamRequest 注入 _sayso 字段、签名、构造 HTTP 请求。
func (s *gatewayService) buildUpstreamRequest(ctx context.Context, a *model.Agent, m *model.AgentMethod, req InvokeRequest, invocationID string) ([]byte, *http.Request, error) {
	// 1. 注入 _sayso 扩展字段到 body 顶层
	bodyWithSayso, err := injectSaysoField(req.Body, req, invocationID)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, nil, err
	}
	// 2. 拿 secret
	//sayso-lint:ignore err-swallow
	plaintext, _, err := s.registry.LoadActiveSecret(ctx, a.ID) // versionID 此路径不需要
	if err != nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return nil, nil, err
	}
	// 3. 签名
	ts := time.Now().UTC().Unix()
	nonce := agent.GenerateNonce()
	sig := agent.ComputeSignature(agent.SignatureInput{
		Secret:    plaintext,
		Timestamp: ts,
		Nonce:     nonce,
		Body:      bodyWithSayso,
	})
	// 4. 构造 HTTP 请求
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.EndpointURL, bytes.NewReader(bodyWithSayso))
	if err != nil {
		//sayso-lint:ignore log-coverage
		return nil, nil, fmt.Errorf("new request: %w: %w", err, agent.ErrAgentInternal)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Sayso-Timestamp", strconv.FormatInt(ts, 10))
	httpReq.Header.Set("X-Sayso-Nonce", nonce)
	httpReq.Header.Set("X-Sayso-Signature", sig)
	httpReq.Header.Set("X-Sayso-Invocation-ID", invocationID)
	if req.TraceID != "" {
		httpReq.Header.Set("X-Sayso-Trace-ID", req.TraceID)
	}
	if m.Transport == agent.TransportSSE {
		httpReq.Header.Set("Accept", "text/event-stream")
	}
	return bodyWithSayso, httpReq, nil
}

// finishInvocation 异步更新 audit 主表。
func (s *gatewayService) finishInvocation(ctx context.Context, invocationID string, startedAt time.Time, status string, errIn error, reqSize, respSize int) {
	now := time.Now().UTC()
	latency := int(now.Sub(startedAt).Milliseconds())
	updates := map[string]any{
		"finished_at":         &now,
		"latency_ms":          &latency,
		"status":              status,
		"request_size_bytes":  &reqSize,
		"response_size_bytes": &respSize,
	}
	if errIn != nil {
		msg := errIn.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
		updates["error_message"] = msg
	}
	s.audit.Finish(ctx, invocationID, startedAt, updates)
}

// maybeRecordPayload 仅当 org.record_full_payload=true 时写 payload。
func (s *gatewayService) maybeRecordPayload(ctx context.Context, orgID uint64, invocationID string, startedAt time.Time, req, resp []byte) {
	org, err := s.orgPort.GetOrgByID(ctx, orgID)
	if err != nil || org == nil || !org.RecordFullPayload {
		return
	}
	s.audit.RecordPayload(ctx, invocationID, startedAt, req, resp)
}

// cancelPollLoop 每 CancelPollIntervalSeconds 秒检查一次 Redis flag,若命中则调用 cancel。
func (s *gatewayService) cancelPollLoop(ctx context.Context, invocationID string, cancel context.CancelFunc) {
	t := time.NewTicker(time.Duration(agent.CancelPollIntervalSeconds) * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if s.cancels.IsCanceled(ctx, invocationID) {
				cancel()
				return
			}
		}
	}
}

// ─── JSON-RPC helpers ────────────────────────────────────────────────────────

// extractJSONRPCMethod 从 JSON-RPC 2.0 body 抽 method 字段。格式错误返回 ErrInvokeJSONRPCInvalid。
func extractJSONRPCMethod(body []byte) (string, error) {
	if len(body) == 0 {
		//sayso-lint:ignore log-coverage
		return "", fmt.Errorf("empty body: %w", agent.ErrInvokeJSONRPCInvalid)
	}
	var parsed struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		ID      json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		//sayso-lint:ignore log-coverage
		return "", fmt.Errorf("parse body: %w: %w", err, agent.ErrInvokeJSONRPCInvalid)
	}
	if parsed.JSONRPC != "2.0" {
		//sayso-lint:ignore log-coverage
		return "", fmt.Errorf("jsonrpc version: %w", agent.ErrInvokeJSONRPCInvalid)
	}
	if parsed.Method == "" {
		//sayso-lint:ignore log-coverage
		return "", fmt.Errorf("method missing: %w", agent.ErrInvokeMethodMissing)
	}
	return parsed.Method, nil
}

// injectSaysoField 在 body 顶层注入 _sayso 字段(不污染 params)。
func injectSaysoField(body []byte, req InvokeRequest, invocationID string) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("parse for inject: %w: %w", err, agent.ErrInvokeJSONRPCInvalid)
	}
	saysoPayload := map[string]any{
		"version": "1",
		"caller": map[string]any{
			"user_id":  req.CallerUserID,
			"nickname": "",
			"role":     req.CallerRole,
		},
		"org": map[string]any{
			"id":   req.OrgID,
			"slug": req.OrgSlug,
		},
		"trace_id":      req.TraceID,
		"invocation_id": invocationID,
		"timestamp":     time.Now().UTC().Unix(),
	}
	encoded, err := json.Marshal(saysoPayload)
	if err != nil {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("marshal sayso: %w: %w", err, agent.ErrAgentInternal)
	}
	envelope["_sayso"] = encoded
	//sayso-lint:ignore sentinel-wrap,log-coverage
	return json.Marshal(envelope)
}

// ─── 用于 audit 查询的辅助工具 ──────────────────────────────────────────────

// InvocationFilter re-exports repository 的 filter 类型,避免 handler 直接 import repo。
type InvocationFilter = repository.InvocationFilter
