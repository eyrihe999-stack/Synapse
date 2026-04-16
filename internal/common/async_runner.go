package common

import (
	"context"
	"sync"

	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"golang.org/x/sync/semaphore"
)

// AsyncRunner 封装 fire-and-forget goroutine 的并发控制与优雅关闭。
type AsyncRunner struct {
	sem    *semaphore.Weighted
	wg     sync.WaitGroup
	logger logger.LoggerInterface
	name   string
}

func NewAsyncRunner(name string, maxConcurrency int64, log logger.LoggerInterface) *AsyncRunner {
	return &AsyncRunner{
		sem:    semaphore.NewWeighted(maxConcurrency),
		logger: log,
		name:   name,
	}
}

func (r *AsyncRunner) Go(ctx context.Context, taskName string, fn func(ctx context.Context)) {
	if !r.sem.TryAcquire(1) {
		r.logger.WarnCtx(ctx, "async task dropped (semaphore full)", map[string]interface{}{
			"runner": r.name,
			"task":   taskName,
		})
		return
	}

	bgCtx := context.Background()

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
		fn(bgCtx)
	}()
}

func (r *AsyncRunner) Wait() {
	r.wg.Wait()
}
