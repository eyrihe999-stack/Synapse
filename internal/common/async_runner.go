package common

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"golang.org/x/sync/semaphore"
)

// asyncRunnerAcquireTimeout 信号量等待超时。
const asyncRunnerAcquireTimeout = 5 * time.Second

// AsyncRunner 封装 fire-and-forget goroutine 的并发控制与优雅关闭。
//
// 生命周期:
//   - 构造时派生一个 runner 级 ctx(独立于调用方请求 ctx),传给每个任务。
//     这样请求 handler 返回不会让 fire-and-forget 任务被 cancel。
//   - Shutdown(ctx) 触发 runner ctx 取消,并等待 in-flight 任务结束(或 ctx 超时)。
//     任务应观察 ctx.Done() 主动收尾,避免 srv.Shutdown 超时后被连带强杀。
type AsyncRunner struct {
	sem    *semaphore.Weighted
	wg     sync.WaitGroup
	logger logger.LoggerInterface
	name   string
	ctx    context.Context
	cancel context.CancelFunc
}

func NewAsyncRunner(name string, maxConcurrency int64, log logger.LoggerInterface) *AsyncRunner {
	ctx, cancel := context.WithCancel(context.Background())
	return &AsyncRunner{
		sem:    semaphore.NewWeighted(maxConcurrency),
		logger: log,
		name:   name,
		ctx:    ctx,
		cancel: cancel,
	}
}

// Go 提交一个异步任务。信号量满时等待最多 5 秒,超时返回错误。
// 已 Shutdown 则直接拒收。任务体接收 runner 级 ctx,该 ctx 在 Shutdown 时被 cancel。
func (r *AsyncRunner) Go(ctx context.Context, taskName string, fn func(ctx context.Context)) error {
	if err := r.ctx.Err(); err != nil {
		return fmt.Errorf("async runner %s: shutting down: %w", r.name, err)
	}

	acquireCtx, cancel := context.WithTimeout(ctx, asyncRunnerAcquireTimeout)
	defer cancel()

	if err := r.sem.Acquire(acquireCtx, 1); err != nil {
		r.logger.WarnCtx(ctx, "async task rejected (semaphore acquire timeout)", map[string]interface{}{
			"runner": r.name,
			"task":   taskName,
		})
		return fmt.Errorf("async runner %s: task %s rejected: %w", r.name, taskName, err)
	}

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer r.sem.Release(1)
		defer func() {
			if rec := recover(); rec != nil {
				r.logger.Error("async task panic recovered", nil, map[string]interface{}{
					"runner": r.name,
					"task":   taskName,
					"panic":  rec,
				})
			}
		}()
		fn(r.ctx)
	}()
	return nil
}

// Wait 无超时等待所有 in-flight 任务结束。不发送取消信号。
// 主要用于测试;生产路径建议使用 Shutdown(ctx) 走带超时的优雅关闭。
func (r *AsyncRunner) Wait() {
	r.wg.Wait()
}

// Shutdown 触发 runner 级 ctx 取消,再等待 in-flight 任务完成或 ctx 超时。
// 返回 ctx.Err() 表示"还有任务未结束但超时了";返回 nil 表示全部完成。
// 超时后任务 goroutine 仍可能在跑,但 runner 不再接受新任务,调用方应继续走自己的 shutdown 流程。
func (r *AsyncRunner) Shutdown(ctx context.Context) error {
	r.cancel()
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("async runner %s: shutdown timed out: %w", r.name, ctx.Err())
	}
}
