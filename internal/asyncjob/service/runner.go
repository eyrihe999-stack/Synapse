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
