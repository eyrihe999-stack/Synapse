// health_service.go agent 健康检查定时任务。
//
// 机制:
//   - 每 DefaultHealthCheckIntervalSeconds 秒扫一次所有 status=active 的 agent
//   - 对每个 agent 发起一个 HEAD 请求(超时 3s),连接成功即 healthy
//   - 连续失败达 DefaultHealthFailThreshold 次则标记 unhealthy
//   - 恢复后 fail_count 归零,状态 healthy
//   - errgroup 限制并发 DefaultHealthCheckConcurrency
//
// 注意:
//   - 同状态连续多次不重复写 DB(避免热路径写放大)
//   - Shutdown 时通过 context cancel 优雅退出
package service

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/agent"
	"github.com/eyrihe999-stack/Synapse/internal/agent/model"
	"github.com/eyrihe999-stack/Synapse/internal/agent/repository"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"golang.org/x/sync/semaphore"
)

// HealthService 提供 agent 健康检查的启动/停止接口。
//sayso-lint:ignore interface-pollution
type HealthService interface {
	// Start 启动后台 goroutine 循环执行健康检查。阻塞调用需要用 go 启动。
	Start(ctx context.Context)
	// Stop 终止 Start 启动的循环并等待退出。
	Stop()
}

type healthService struct {
	cfg    Config
	repo   repository.Repository
	logger logger.LoggerInterface
	client *http.Client

	cancelFn context.CancelFunc
	done     chan struct{}
}

// NewHealthService 构造健康检查服务。
func NewHealthService(cfg Config, repo repository.Repository, log logger.LoggerInterface) HealthService {
	return &healthService{
		cfg:    cfg,
		repo:   repo,
		logger: log,
		client: &http.Client{
			Timeout: time.Duration(agent.HealthCheckRequestTimeoutSeconds) * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        50,
				MaxIdleConnsPerHost: 5,
				IdleConnTimeout:     30 * time.Second,
			},
		},
	}
}

// Start 启动健康检查循环。
func (s *healthService) Start(parent context.Context) {
	//sayso-lint:ignore ctx-cancel-leak
	ctx, cancel := context.WithCancel(parent) // cancel 保存到 s.cancelFn,由 Stop() 调用
	s.cancelFn = cancel
	s.done = make(chan struct{})
	interval := time.Duration(s.cfg.HealthCheckIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = time.Duration(agent.DefaultHealthCheckIntervalSeconds) * time.Second
	}
	//sayso-lint:ignore bare-goroutine
	go func() { // 长生命周期健康检查主循环,由 Stop() 控制退出,不适合 AsyncRunner 的 fire-and-forget 模型
		defer close(s.done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.runOnce(ctx)
			}
		}
	}()
	s.logger.Info("agent health checker started", map[string]any{"interval_seconds": int(interval.Seconds())})
}

// Stop 取消 Start 创建的内部 context 并阻塞等待主循环 goroutine 退出。
// 幂等:未启动或已停止时为 no-op。
func (s *healthService) Stop() {
	if s.cancelFn != nil {
		s.cancelFn()
	}
	if s.done != nil {
		<-s.done
	}
}

// runOnce 扫描 active agent 并行 probe。
func (s *healthService) runOnce(ctx context.Context) {
	list, err := s.repo.ListActiveAgentsForHealthCheck(ctx, 0)
	if err != nil {
		s.logger.ErrorCtx(ctx, "health check list failed", err, nil)
		return
	}
	if len(list) == 0 {
		return
	}
	// 大于 1000 时发 warn
	if len(list) > 1000 {
		s.logger.WarnCtx(ctx, "health check fan-out large", map[string]any{"count": len(list)})
	}
	concurrency := int64(s.cfg.HealthCheckConcurrency)
	if concurrency <= 0 {
		concurrency = int64(agent.DefaultHealthCheckConcurrency)
	}
	sem := semaphore.NewWeighted(concurrency)
	var wg sync.WaitGroup
	for _, a := range list {
		if err := sem.Acquire(ctx, 1); err != nil {
			return
		}
		wg.Add(1)
		//sayso-lint:ignore bare-goroutine
		go func(a *model.Agent) { // fan-out probe,由 wg + semaphore 控制生命周期
			defer sem.Release(1)
			defer wg.Done()
			s.probeOne(ctx, a)
		}(a)
	}
	wg.Wait()
}

// probeOne 对单个 agent 发送探测请求。
func (s *healthService) probeOne(ctx context.Context, a *model.Agent) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, a.EndpointURL, nil)
	if err != nil {
		s.logger.WarnCtx(ctx, "health check new request failed", map[string]any{"agent_id": a.ID, "error": err.Error()})
		s.applyResult(ctx, a, false)
		return
	}
	resp, err := s.client.Do(req)
	if err != nil {
		s.applyResult(ctx, a, false)
		return
	}
	//sayso-lint:ignore err-swallow
	_ = resp.Body.Close() // close 错误无处理
	// 2xx / 3xx / 405 (Method Not Allowed,但连接已通) 视作 healthy
	ok := resp.StatusCode < 500
	s.applyResult(ctx, a, ok)
}

// applyResult 根据探测结果更新 agent 的 health_status / fail_count。
// 同状态连续相同结果不重写 DB。
func (s *healthService) applyResult(ctx context.Context, a *model.Agent, healthy bool) {
	now := time.Now().UTC()
	updates := map[string]any{"health_checked_at": &now}
	if healthy {
		if a.HealthStatus == model.HealthStatusHealthy && a.HealthFailCount == 0 {
			// 无状态变化,只更新时间戳(可选:跳过以减少写)
			//sayso-lint:ignore err-swallow
			_ = s.repo.UpdateAgentFields(ctx, a.ID, updates) // best-effort 时间戳更新
			return
		}
		updates["health_status"] = model.HealthStatusHealthy
		updates["health_fail_count"] = 0
	} else {
		newFail := a.HealthFailCount + 1
		updates["health_fail_count"] = newFail
		threshold := s.cfg.HealthFailThreshold
		if threshold <= 0 {
			threshold = agent.DefaultHealthFailThreshold
		}
		if newFail >= threshold {
			if a.HealthStatus == model.HealthStatusUnhealthy {
				// 已经是 unhealthy,只刷计数
			} else {
				updates["health_status"] = model.HealthStatusUnhealthy
			}
		}
	}
	if err := s.repo.UpdateAgentFields(ctx, a.ID, updates); err != nil {
		s.logger.ErrorCtx(ctx, "health check update failed", err, map[string]any{"agent_id": a.ID})
	}
}
