// Package service asyncjob 的调度 + 状态机层。
//
// runner.go: Runner / ProgressReporter 接口定义。每种 Kind 写一个 Runner 实现,
// 在 NewService 时注册进去(见 service.go)。
package service

import (
	"context"

	"github.com/eyrihe999-stack/Synapse/internal/asyncjob/model"
)

// Runner 执行某一类任务的业务逻辑。实现者:
//   - 纯同步 + 观察 ctx.Done() 主动退出(服务 shutdown 时 ctx 会被 cancel)
//   - 失败返 error,成功返 result(会被 service marshal 进 Job.Result jsonb 列)
//   - 通过 ProgressReporter 汇报进度,不直接碰 repo
//
// 为什么 result 是 any 而不是 json.RawMessage:让 runner 返结构化 dto,
// 序列化责任下沉到 service 层统一做(方便加日志、做大小校验)。
type Runner interface {
	Kind() string
	Run(ctx context.Context, job *model.Job, reporter ProgressReporter) (result any, err error)
}

// ProgressReporter Runner 回报进度的唯一通道。
//
// 语义:
//   - SetTotal: 首次确定总量时调。后续再调会覆盖(一般只调一次)。
//   - Inc: done/failed 增量累加,线程安全。建议"每处理完一条调一次",
//     频率过高(>10Hz)时 service 层会合批写 DB 避免压力,但 Runner 不用关心。
type ProgressReporter interface {
	SetTotal(total int) error
	Inc(deltaDone, deltaFailed int) error
}

// ConcurrentRunner 可选接口,Runner 如需支持"同 user 同 kind 多任务并发"就实现此接口并返 true。
//
// 语义:
//
//   - **不实现**(默认):Schedule 走防重,同一 (user_id, kind) 已有 active 任务 → 返 ErrDuplicateJob
//     适用:飞书 / GitLab sync 这类"天然不该并发"的长任务
//   - **实现且 AllowConcurrent() == true**:Schedule 跳过防重,允许同 user 同 kind 任意多任务同时排队
//     适用:文档 upload —— 用户一次拖 N 个文件应当 N 个任务并行
//
// 注意:"并发"只是指 FindActive 防重的跳过,实际并发度仍受 Service.cfg.MaxConcurrency 约束。
type ConcurrentRunner interface {
	Runner
	AllowConcurrent() bool
}
