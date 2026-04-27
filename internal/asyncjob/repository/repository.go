// Package repository async_jobs 表的持久化层。
//
// 接口刻意薄:Runner 运行中只需要 heartbeat + progress update;查询路径主要给 HTTP
// handler 轮询用。future-proofing 放 kind/payload/result 这三个 jsonb 字段上,
// 接口不需要为每种 kind 加新方法。
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/asyncjob/model"
)

// Repository async_jobs 表 CRUD。
// 11 个方法对应 Job 生命周期的 Create / 状态机(MarkRunning/Heartbeat/MarkFinished/ReapStale)/ 进度(SetTotal/IncProgress)/ 查询 —— 各职责正交,拆分会让 Service 拼装多个子仓库、收益很低。
//
//sayso-lint:ignore interface-pollution
type Repository interface {
	// Create 插入 queued 任务。返回带 ID 的行。
	Create(ctx context.Context, in *model.Job) error

	// Get 按 ID 取。找不到返 (nil, nil) —— HTTP handler 可据此返 404。
	Get(ctx context.Context, id uint64) (*model.Job, error)

	// FindActive 查 (user_id, kind) 当前是否有 queued/running 任务;
	// 有则返,供"防重复提交"用。无则 (nil, nil)。
	FindActive(ctx context.Context, userID uint64, kind string) (*model.Job, error)

	// FindLatest 查 (user_id, kind) 最近一条任务(按 id DESC),不论状态。
	// 用途:前端 mount 时拿"上次任务是不是失败了"的信息,决定是否展示持久失败横幅。
	// 找不到返 (nil, nil)。
	FindLatest(ctx context.Context, userID uint64, kind string) (*model.Job, error)

	// FindByIdempotencyKey 按幂等键查已存在的 job(不论状态)。
	//
	// 用途:workflow 引擎 re-drive 同一 step_run 时用相同 key,命中则直接复用 —— 含终态。
	// key 空串返 (nil, nil)(语义:不启用幂等判断);无匹配也返 (nil, nil)。
	// 唯一索引保证"同 (org_id, kind, key) 至多一条",所以直接 Take 即可。
	FindByIdempotencyKey(ctx context.Context, orgID uint64, kind, key string) (*model.Job, error)

	// MarkRunning queued → running,填 StartedAt + 首次 HeartbeatAt。幂等:状态不是 queued 返 error。
	MarkRunning(ctx context.Context, id uint64) error

	// Heartbeat 仅更新 heartbeat_at。Runner 周期调。
	Heartbeat(ctx context.Context, id uint64) error

	// SetTotal 设置 progress_total(Runner 扫到总量后调一次)。
	SetTotal(ctx context.Context, id uint64, total int) error

	// IncProgress done / failed 增量 +=。用 GORM Expr 走原子更新,避免读后写竞争。
	IncProgress(ctx context.Context, id uint64, deltaDone, deltaFailed int) error

	// MarkFinished 转终态(succeeded/failed/canceled),填 FinishedAt + result/error。
	// 传 status=failed 时 result 可为 nil;传 succeeded 时 errMsg 应为空。
	MarkFinished(ctx context.Context, id uint64, status model.Status, result datatypes.JSON, errMsg string) error

	// ReapStale 启动时扫"status=running 且 heartbeat 陈旧"的行,标 failed。
	// 返被回收条数。err 仅 DB 异常。
	ReapStale(ctx context.Context, olderThan time.Duration) (int64, error)
}

// New 构造。
func New(db *gorm.DB) Repository { return &gormRepo{db: db} }

type gormRepo struct{ db *gorm.DB }

func (r *gormRepo) Create(ctx context.Context, in *model.Job) error {
	if in == nil || in.UserID == 0 || in.Kind == "" {
		return fmt.Errorf("async job create: user_id and kind required")
	}
	if in.Status == "" {
		in.Status = model.StatusQueued
	}
	if err := r.db.WithContext(ctx).Create(in).Error; err != nil {
		return fmt.Errorf("async job create: %w", err)
	}
	return nil
}

func (r *gormRepo) Get(ctx context.Context, id uint64) (*model.Job, error) {
	var row model.Job
	err := r.db.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("async job get: %w", err)
	}
	return &row, nil
}

func (r *gormRepo) FindActive(ctx context.Context, userID uint64, kind string) (*model.Job, error) {
	var row model.Job
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND kind = ? AND status IN ?", userID, kind,
			[]model.Status{model.StatusQueued, model.StatusRunning}).
		Order("id DESC").
		Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("async job find active: %w", err)
	}
	return &row, nil
}

func (r *gormRepo) FindLatest(ctx context.Context, userID uint64, kind string) (*model.Job, error) {
	var row model.Job
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND kind = ?", userID, kind).
		Order("id DESC").
		Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("async job find latest: %w", err)
	}
	return &row, nil
}

func (r *gormRepo) FindByIdempotencyKey(ctx context.Context, orgID uint64, kind, key string) (*model.Job, error) {
	if key == "" {
		return nil, nil
	}
	var row model.Job
	err := r.db.WithContext(ctx).
		Where("org_id = ? AND kind = ? AND idempotency_key = ?", orgID, kind, key).
		Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("async job find by idempotency key: %w", err)
	}
	return &row, nil
}

func (r *gormRepo) MarkRunning(ctx context.Context, id uint64) error {
	now := time.Now().UTC()
	res := r.db.WithContext(ctx).Model(&model.Job{}).
		Where("id = ? AND status = ?", id, model.StatusQueued).
		Updates(map[string]any{
			"status":       model.StatusRunning,
			"started_at":   now,
			"heartbeat_at": now,
		})
	if res.Error != nil {
		return fmt.Errorf("async job mark running: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("async job mark running: job %d not in queued state", id)
	}
	return nil
}

func (r *gormRepo) Heartbeat(ctx context.Context, id uint64) error {
	if err := r.db.WithContext(ctx).Model(&model.Job{}).
		Where("id = ?", id).
		Update("heartbeat_at", time.Now().UTC()).Error; err != nil {
		return fmt.Errorf("async job heartbeat: %w", err)
	}
	return nil
}

func (r *gormRepo) SetTotal(ctx context.Context, id uint64, total int) error {
	if err := r.db.WithContext(ctx).Model(&model.Job{}).
		Where("id = ?", id).
		Update("progress_total", total).Error; err != nil {
		return fmt.Errorf("async job set total: %w", err)
	}
	return nil
}

func (r *gormRepo) IncProgress(ctx context.Context, id uint64, deltaDone, deltaFailed int) error {
	if deltaDone == 0 && deltaFailed == 0 {
		return nil
	}
	updates := map[string]any{}
	if deltaDone != 0 {
		updates["progress_done"] = gorm.Expr("progress_done + ?", deltaDone)
	}
	if deltaFailed != 0 {
		updates["progress_failed"] = gorm.Expr("progress_failed + ?", deltaFailed)
	}
	if err := r.db.WithContext(ctx).Model(&model.Job{}).
		Where("id = ?", id).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("async job inc progress: %w", err)
	}
	return nil
}

func (r *gormRepo) MarkFinished(ctx context.Context, id uint64, status model.Status, result datatypes.JSON, errMsg string) error {
	now := time.Now().UTC()
	updates := map[string]any{
		"status":      status,
		"finished_at": now,
		"error":       errMsg,
	}
	if result != nil {
		updates["result"] = result
	}
	res := r.db.WithContext(ctx).Model(&model.Job{}).
		Where("id = ? AND status = ?", id, model.StatusRunning).
		Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("async job mark finished: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("async job mark finished: job %d not in running state", id)
	}
	return nil
}

func (r *gormRepo) ReapStale(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-olderThan)
	now := time.Now().UTC()
	// heartbeat_at IS NULL 也算 stale —— 理论上 MarkRunning 已填首次 heartbeat,
	// 仍为 NULL 说明崩在 MarkRunning 和首次 Heartbeat 之间。
	res := r.db.WithContext(ctx).Model(&model.Job{}).
		Where("status = ? AND (heartbeat_at IS NULL OR heartbeat_at < ?)", model.StatusRunning, cutoff).
		Updates(map[string]any{
			"status":      model.StatusFailed,
			"error":       "reaped: no heartbeat (process likely crashed)",
			"finished_at": now,
		})
	if res.Error != nil {
		return 0, fmt.Errorf("async job reap stale: %w", res.Error)
	}
	return res.RowsAffected, nil
}
