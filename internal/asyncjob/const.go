// const.go asyncjob 模块常量定义。
//
// 任务 Kind / Status 与模型强绑定,定义在 model/models.go,此处不再重复。
package asyncjob

import "time"

// ─── 默认值与上限 ─────────────────────────────────────────────────────────────

const (
	// DefaultMaxConcurrency 全局 goroutine 并发上限。超过会阻塞 Schedule 最多 5s 后返错。
	DefaultMaxConcurrency int64 = 8

	// DefaultStaleThreshold 心跳多久没更新算崩,启动时被 ReapStale 扫成 failed。
	DefaultStaleThreshold = 60 * time.Second

	// DefaultHeartbeatInterval Runner 心跳写入间隔。
	DefaultHeartbeatInterval = 10 * time.Second

	// PersistTimeout 任务终态回写超时。服务 shutdown 中 request ctx 已 cancel,
	// 用独立 timeout 确保状态落库,否则重启被 reap 误判。
	PersistTimeout = 5 * time.Second
)
