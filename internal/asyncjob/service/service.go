// service.go asyncjob 调度器。
//
// 职责:
//   - 按 kind 路由到已注册的 Runner
//   - 管状态机推进(queued → running → terminal)
//   - 用 common.AsyncRunner 跑 goroutine,获得并发上限 + 优雅 shutdown
//   - 提供 Schedule / GetJob 给 HTTP handler
//   - 启动时 ReapStale 回收前次进程崩掉留下的 running 记录
//
// 反例(刻意不做):
//   - 不做"同 kind 串行化":每 user 同 kind 最多一条活跃 job 的保证走 FindActive
//     防重,真打并发压力让 Runner 自己决定是否加锁。
//   - 不做优先级队列:MVP 无差别先来先跑。将来有需求再加。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/datatypes"

	"github.com/eyrihe999-stack/Synapse/internal/asyncjob/model"
	"github.com/eyrihe999-stack/Synapse/internal/asyncjob/repository"
	"github.com/eyrihe999-stack/Synapse/internal/common"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
)

// ErrDuplicateJob Schedule 时发现该 (user_id, kind) 已有 queued/running 任务。
// HTTP handler 捕这个错返 409 Conflict 并把活跃 job_id 一起返,前端直接轮询已有任务。
var ErrDuplicateJob = errors.New("async job: duplicate active job")

// ErrUnknownKind 调 Schedule 传了没注册 Runner 的 kind —— 配置/代码 bug,返 500。
var ErrUnknownKind = errors.New("async job: unknown kind")

// Config 调度器可调参数。
type Config struct {
	// MaxConcurrency 全局并发上限。超过会阻塞在 AsyncRunner.Go 最多 5s 再拒。
	// 默认 8:考虑每个 Runner 可能跑好几分钟并发多了压后端(OSS / embed)。
	MaxConcurrency int64

	// StaleThreshold 心跳多久没更新算崩。默认 60s —— Runner 每 10s 心跳,留 6 倍余量。
	StaleThreshold time.Duration

	// HeartbeatInterval Runner 内部心跳间隔。默认 10s。
	HeartbeatInterval time.Duration
}

// Service asyncjob 对外入口。
type Service struct {
	cfg     Config
	repo    repository.Repository
	runners map[string]Runner
	async   *common.AsyncRunner
	log     logger.LoggerInterface
}

// NewService 构造并注册 runners。传入的 runners 里若两个 Kind() 相同,后者覆盖前者。
// 调用方(cmd/synapse)应在启动后调一次 ReapStale 收拾前次崩进程遗留。
func NewService(cfg Config, repo repository.Repository, runners []Runner, log logger.LoggerInterface) *Service {
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 8
	}
	if cfg.StaleThreshold <= 0 {
		cfg.StaleThreshold = 60 * time.Second
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = 10 * time.Second
	}
	reg := make(map[string]Runner, len(runners))
	for _, r := range runners {
		reg[r.Kind()] = r
	}
	return &Service{
		cfg:     cfg,
		repo:    repo,
		runners: reg,
		async:   common.NewAsyncRunner("asyncjob", cfg.MaxConcurrency, log),
		log:     log,
	}
}

// ScheduleInput 一次调度的参数。Payload 可为 nil。
type ScheduleInput struct {
	OrgID   uint64
	UserID  uint64
	Kind    string
	Payload any // marshal 失败会返 error
}

// Schedule 创建 queued job 并提交 goroutine。幂等性:同 (user_id, kind) 已有 active → 返
// ErrDuplicateJob + 已存在的 Job。调用方按场景自己决定是视为成功(返 existing id)还是报错。
func (s *Service) Schedule(ctx context.Context, in ScheduleInput) (*model.Job, error) {
	if in.UserID == 0 || in.Kind == "" {
		return nil, fmt.Errorf("schedule: user_id + kind required")
	}
	runner, ok := s.runners[in.Kind]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownKind, in.Kind)
	}
	// 防重:查 active。有则抛 ErrDuplicateJob + 把现有 job 带出去,由 handler 决定展示行为。
	if existing, err := s.repo.FindActive(ctx, in.UserID, in.Kind); err != nil {
		return nil, fmt.Errorf("schedule: check active: %w", err)
	} else if existing != nil {
		return existing, ErrDuplicateJob
	}

	var payloadJSON datatypes.JSON
	if in.Payload != nil {
		raw, err := json.Marshal(in.Payload)
		if err != nil {
			return nil, fmt.Errorf("schedule: marshal payload: %w", err)
		}
		payloadJSON = raw
	}
	job := &model.Job{
		OrgID:   in.OrgID,
		UserID:  in.UserID,
		Kind:    in.Kind,
		Status:  model.StatusQueued,
		Payload: payloadJSON,
	}
	if err := s.repo.Create(ctx, job); err != nil {
		return nil, err
	}

	// 提交 goroutine。runner ctx 独立于 request ctx —— handler return 后任务继续跑。
	if err := s.async.Go(ctx, fmt.Sprintf("%s#%d", in.Kind, job.ID), func(runCtx context.Context) {
		s.runJob(runCtx, job, runner)
	}); err != nil {
		// 调度失败:标 failed 方便前端看到,不留 queued 悬而未决。
		_ = s.repo.MarkRunning(context.Background(), job.ID) // 占位推进,便于 MarkFinished 通过状态校验
		_ = s.repo.MarkFinished(context.Background(), job.ID, model.StatusFailed, nil,
			fmt.Sprintf("schedule: submit goroutine failed: %v", err))
		return nil, err
	}
	return job, nil
}

// GetJob HTTP 轮询接口。权限校验(userID 是否 match)由 handler 层决定,本层只做查找。
func (s *Service) GetJob(ctx context.Context, id uint64) (*model.Job, error) {
	return s.repo.Get(ctx, id)
}

// FindActive 查当前用户是否有指定 kind 的活跃任务(queued 或 running)。
// 用途:前端切页面 / F5 回来时,拿这个接口续上已在跑的任务 id,不需要重启新任务。
// 返 (nil, nil) 表示没有活跃任务。
func (s *Service) FindActive(ctx context.Context, userID uint64, kind string) (*model.Job, error) {
	return s.repo.FindActive(ctx, userID, kind)
}

// FindLatest 查当前用户该 kind 最近一次任务(按创建顺序,不论状态)。
// 用途:前端 mount 时判断"上次有没有失败过",失败则展示常驻横幅 + 重试按钮。
// 返 (nil, nil) 表示该用户从未跑过此类任务。
func (s *Service) FindLatest(ctx context.Context, userID uint64, kind string) (*model.Job, error) {
	return s.repo.FindLatest(ctx, userID, kind)
}

// ListRecent 列出当前用户该 kind 的最近 limit 条任务(按 id DESC)。
// 用途:前端"同步历史"视图。limit 上限在此 clamp(保护 DB),0 / 负数走默认。
func (s *Service) ListRecent(ctx context.Context, userID uint64, kind string, limit int) ([]*model.Job, error) {
	const maxLimit = 50
	if limit <= 0 {
		limit = 10
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	return s.repo.ListByUser(ctx, userID, kind, limit)
}

// ReapStale 启动时扫一次。返回被回收数量。错误仅 log,不 fatal(DB 临时问题不应阻塞启动)。
func (s *Service) ReapStale(ctx context.Context) {
	n, err := s.repo.ReapStale(ctx, s.cfg.StaleThreshold)
	if err != nil {
		s.log.Warn("asyncjob: reap stale failed", map[string]any{"err": err.Error()})
		return
	}
	if n > 0 {
		s.log.Info("asyncjob: reaped stale jobs", map[string]any{"count": n})
	}
}

// Shutdown 触发 runner ctx 取消并等待 in-flight 任务结束或超时。
// 超时后 goroutine 仍可能在跑,service 本身不再接受新任务。返回 nil 表示全部结束。
func (s *Service) Shutdown(ctx context.Context) error {
	return s.async.Shutdown(ctx)
}

// ─── 内部 ────────────────────────────────────────────────────────────────────

// runJob 单次 job 执行的主流程。失败标 failed,成功标 succeeded。panic 被 AsyncRunner 接。
func (s *Service) runJob(ctx context.Context, job *model.Job, runner Runner) {
	// 状态推进到 running。失败(比如前面崩进程已被 reap 成 failed)就早退。
	if err := s.repo.MarkRunning(ctx, job.ID); err != nil {
		s.log.Warn("asyncjob: mark running failed", map[string]any{
			"job_id": job.ID, "kind": job.Kind, "err": err.Error(),
		})
		return
	}

	// 心跳 ticker。单独 goroutine 每 HeartbeatInterval 打一次,直到 ctx/结束信号 done。
	hbCtx, cancelHB := context.WithCancel(ctx)
	defer cancelHB()
	go s.heartbeatLoop(hbCtx, job.ID)

	reporter := &dbReporter{repo: s.repo, jobID: job.ID, log: s.log}

	result, runErr := s.safeRun(ctx, runner, job, reporter)
	cancelHB()

	// 终态写入。服务 shutdown 中 ctx 已 cancel,此时用 background ctx 做最后回写,
	// 确保状态落库(否则重启时被 reap 误判)。
	persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if runErr != nil {
		errMsg := runErr.Error()
		// ctx.Err 不算业务失败 —— 服务 shutdown 导致,重启后允许用户重跑。
		// 但状态还是标 failed,只是 error 字段区分一下原因。
		if errors.Is(runErr, context.Canceled) {
			errMsg = "canceled: service shutdown"
		}
		if err := s.repo.MarkFinished(persistCtx, job.ID, model.StatusFailed, nil, errMsg); err != nil {
			s.log.Error("asyncjob: mark failed persist error", err, map[string]any{"job_id": job.ID})
		}
		return
	}

	var resultJSON datatypes.JSON
	if result != nil {
		raw, err := json.Marshal(result)
		if err != nil {
			// result marshal 失败退化成 succeeded + 空 result,不影响任务本身结果。
			s.log.Warn("asyncjob: marshal result failed", map[string]any{
				"job_id": job.ID, "err": err.Error(),
			})
		} else {
			resultJSON = raw
		}
	}
	if err := s.repo.MarkFinished(persistCtx, job.ID, model.StatusSucceeded, resultJSON, ""); err != nil {
		s.log.Error("asyncjob: mark succeeded persist error", err, map[string]any{"job_id": job.ID})
	}
}

// safeRun recover panic 包装,把 panic 转 error。
func (s *Service) safeRun(ctx context.Context, runner Runner, job *model.Job, reporter ProgressReporter) (result any, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("panic: %v", rec)
			s.log.Error("asyncjob: runner panic", nil, map[string]any{
				"job_id": job.ID, "kind": job.Kind, "panic": rec,
			})
		}
	}()
	return runner.Run(ctx, job, reporter)
}

// heartbeatLoop 每 HeartbeatInterval 打一次心跳。失败仅 warn(DB 抖动不该让任务失败)。
func (s *Service) heartbeatLoop(ctx context.Context, jobID uint64) {
	ticker := time.NewTicker(s.cfg.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.repo.Heartbeat(ctx, jobID); err != nil {
				s.log.Warn("asyncjob: heartbeat failed", map[string]any{
					"job_id": jobID, "err": err.Error(),
				})
			}
		}
	}
}

// dbReporter 把 Runner 的 SetTotal/Inc 调用转发到 repo。
// 没做合批:Runner 层以"每条处理完调一次"的频率,飞书 Sync 一次上百条也能接受,
// 将来 hot path 真出问题再加 ring buffer + flush interval。
type dbReporter struct {
	repo  repository.Repository
	jobID uint64
	log   logger.LoggerInterface
}

func (r *dbReporter) SetTotal(total int) error {
	return r.repo.SetTotal(context.Background(), r.jobID, total)
}

func (r *dbReporter) Inc(deltaDone, deltaFailed int) error {
	return r.repo.IncProgress(context.Background(), r.jobID, deltaDone, deltaFailed)
}
