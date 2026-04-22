// errors.go asyncjob 模块错误码与哨兵错误变量定义。
//
// 错误码格式 HHHSSCCCC:
//   - HHH:HTTP 状态码 (400/404/409/500)
//   - SS:模块号 13 = asyncjob
//   - CCCC:业务码
//
// 业务错误统一以 HTTP 200 + body 业务码返回。
// 仅 ErrAsyncJobInternal 使用 500。
package asyncjob

import "errors"

// ─── 400 段:请求/业务校验 ────────────────────────────────────────────────────

const (
	// CodeAsyncJobInvalidRequest Schedule 入参缺字段 / payload 序列化失败
	CodeAsyncJobInvalidRequest = 400130010
	// CodeAsyncJobUnknownKind 未注册的 Runner kind
	CodeAsyncJobUnknownKind = 400130011
)

// ─── 404 段:资源不存在 ──────────────────────────────────────────────────────

const (
	// CodeAsyncJobNotFound Job ID 不存在
	CodeAsyncJobNotFound = 404130020
)

// ─── 409 段:冲突 ─────────────────────────────────────────────────────────────

const (
	// CodeAsyncJobDuplicate 同 (user_id, kind) 已有 queued/running 任务
	CodeAsyncJobDuplicate = 409130010
)

// ─── 500 段:内部错误 ────────────────────────────────────────────────────────

const (
	// CodeAsyncJobInternal 内部基础设施错误(DB / goroutine 提交失败等)
	CodeAsyncJobInternal = 500130000
)

// ─── Sentinel Errors ────────────────────────────────────────────────────────

var (
	// ─ 400 段 ─

	// ErrAsyncJobInvalidRequest Schedule 入参不合法(缺 user_id/kind 或 payload 无法序列化)
	ErrAsyncJobInvalidRequest = errors.New("asyncjob: invalid request")

	// ErrUnknownKind 调 Schedule 传了没注册 Runner 的 kind —— 配置/代码 bug。
	ErrUnknownKind = errors.New("asyncjob: unknown kind")

	// ─ 404 段 ─

	// ErrAsyncJobNotFound 按 ID 查不到 Job
	ErrAsyncJobNotFound = errors.New("asyncjob: job not found")

	// ─ 409 段 ─

	// ErrDuplicateJob Schedule 时发现该 (user_id, kind) 已有 queued/running 任务。
	// 调用方按场景自己决定是视为成功(返 existing id)还是报错。
	ErrDuplicateJob = errors.New("asyncjob: duplicate active job")

	// ─ 500 段 ─

	// ErrAsyncJobInternal 内部基础设施错误
	ErrAsyncJobInternal = errors.New("asyncjob: internal error")
)
