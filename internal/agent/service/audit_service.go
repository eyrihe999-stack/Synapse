// audit_service.go 调用审计 service:写入主表 + 异步写 payload。
//
// 调用时序(gateway 调用):
//  1. gateway 生成 invocation_id,调用 Begin(ctx, inv) 同步写 agent_invocations
//     主表(status=running)
//  2. 转发完成后调用 Finish(ctx, id, startedAt, updates) 更新 finished_at / status / ...
//  3. 若需要 payload,调用 RecordPayload(ctx, id, startedAt, req, resp),内部异步写
//     agent_invocation_payloads
//
// Finish 和 RecordPayload 都走 AsyncRunner,不阻塞请求返回。
// 主表写入失败时,gateway 仍然正常转发(只记录 ErrorCtx),避免审计组件故障阻塞主流程。
package service

import (
	"context"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/agent"
	"github.com/eyrihe999-stack/Synapse/internal/agent/model"
	"github.com/eyrihe999-stack/Synapse/internal/agent/repository"
	"github.com/eyrihe999-stack/Synapse/internal/common"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
)

// AuditService 提供 invocation 审计写入与查询能力。
//sayso-lint:ignore interface-pollution
type AuditService interface {
	// Begin 同步写入 invocation 主表。失败返回 error,但 gateway 会选择继续转发(best-effort)。
	Begin(ctx context.Context, inv *model.AgentInvocation) error
	// Finish 异步更新主表的 finished_at / latency / status / error 等字段。
	Finish(ctx context.Context, invocationID string, startedAt time.Time, updates map[string]any)
	// RecordPayload 异步写 payload 表(仅当 org 开启 record_full_payload)。
	// req/resp 会在此处被截断至 PayloadTruncateSize。
	RecordPayload(ctx context.Context, invocationID string, startedAt time.Time, req, resp []byte)
	// ListByOrg 分页查询 org 的审计记录。filter 支持按调用者、agent owner、agent_id 过滤。
	ListByOrg(ctx context.Context, orgID uint64, filter repository.InvocationFilter, page, size int) ([]*model.AgentInvocation, int64, error)
	// GetByInvocationID 按 invocation_id 查主表 + payload(payload 可选)。
	GetByInvocationID(ctx context.Context, invocationID string, withPayload bool) (*model.AgentInvocation, *model.AgentInvocationPayload, error)
}

type auditService struct {
	repo   repository.Repository
	runner *common.AsyncRunner
	logger logger.LoggerInterface
}

// NewAuditService 构造 AuditService。runner 用于异步写入 Finish/Payload,避免阻塞网关。
func NewAuditService(repo repository.Repository, runner *common.AsyncRunner, log logger.LoggerInterface) AuditService {
	return &auditService{repo: repo, runner: runner, logger: log}
}

// Begin 同步写入 invocation 主表,标记调用开始(status=running)。
// 被 gateway 在发起上游请求前调用,失败会被视为 best-effort 并由调用方决定是否继续转发。
//
// 错误:底层 InsertInvocation 失败时透传 repository 错误,由调用方上浮为 ErrAgentInternal。
func (s *auditService) Begin(ctx context.Context, inv *model.AgentInvocation) error {
	if err := s.repo.InsertInvocation(ctx, inv); err != nil {
		s.logger.ErrorCtx(ctx, "audit begin failed", err, map[string]any{"invocation_id": inv.InvocationID})
		//sayso-lint:ignore sentinel-wrap
		return err
	}
	return nil
}

// Finish 异步更新主表状态。
func (s *auditService) Finish(ctx context.Context, invocationID string, startedAt time.Time, updates map[string]any) {
	if len(updates) == 0 {
		return
	}
	idCopy := invocationID
	stCopy := startedAt
	upCopy := updates
	s.runner.Go(ctx, "audit.finish", func(bgCtx context.Context) {
		if err := s.repo.UpdateInvocationByID(bgCtx, idCopy, stCopy, upCopy); err != nil {
			s.logger.ErrorCtx(bgCtx, "audit finish failed", err, map[string]any{"invocation_id": idCopy})
		}
	})
}

// RecordPayload 异步写 payload。req/resp 会被截断。
func (s *auditService) RecordPayload(ctx context.Context, invocationID string, startedAt time.Time, req, resp []byte) {
	idCopy := invocationID
	stCopy := startedAt
	reqCopy := truncate(req, agent.PayloadTruncateSize)
	respCopy := truncate(resp, agent.PayloadTruncateSize)
	s.runner.Go(ctx, "audit.payload", func(bgCtx context.Context) {
		p := &model.AgentInvocationPayload{
			InvocationID: idCopy,
			RequestBody:  reqCopy,
			ResponseBody: respCopy,
			StartedAt:    stCopy,
		}
		if err := s.repo.InsertInvocationPayload(bgCtx, p); err != nil {
			s.logger.ErrorCtx(bgCtx, "audit payload insert failed", err, map[string]any{"invocation_id": idCopy})
		}
	})
}

// ListByOrg 分页查询 org 的审计记录。
//
// 错误:底层 repository 查询失败时透传错误,由调用方上浮为 ErrAgentInternal。
func (s *auditService) ListByOrg(ctx context.Context, orgID uint64, filter repository.InvocationFilter, page, size int) ([]*model.AgentInvocation, int64, error) {
	//sayso-lint:ignore sentinel-wrap,log-coverage
	return s.repo.ListInvocationsByOrg(ctx, orgID, filter, page, size)
}

// GetByInvocationID 查单条 invocation(可选 payload)。
//
// 错误:主表不存在返回 gorm.ErrRecordNotFound(由调用方翻译为 ErrInvocationNotFound),其他存储错误上浮为 ErrAgentInternal。
func (s *auditService) GetByInvocationID(ctx context.Context, invocationID string, withPayload bool) (*model.AgentInvocation, *model.AgentInvocationPayload, error) {
	inv, err := s.repo.FindInvocationByID(ctx, invocationID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "find invocation failed", err, map[string]any{"invocation_id": invocationID})
		//sayso-lint:ignore sentinel-wrap
		return nil, nil, err
	}
	if !withPayload {
		return inv, nil, nil
	}
	p, err := s.repo.FindInvocationPayload(ctx, invocationID)
	if err != nil {
		// payload 不存在不是错误(未开启或未异步写完)
		return inv, nil, nil
	}
	return inv, p, nil
}

// truncate 截断字节切片到 max,返回拷贝(避免被上层修改)。
func truncate(b []byte, max int) []byte {
	if len(b) == 0 {
		return nil
	}
	if len(b) > max {
		b = b[:max]
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
