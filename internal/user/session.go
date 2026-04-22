// session.go user 模块 session 存储接口定义。
//
// 接口放在 user 包级别,避免 middleware → service 循环导入。
// Redis 实现在 service/session_store.go。
package user

import (
	"context"
	"time"
)

// SessionInfo 存储在 Redis 中的单条 session 信息。
//
// LoginAt 每次 Login/Refresh 都会更新为当前时间(体现"最近活跃"),
// SessionStartAt 只在首次创建该 device session 时写入,refresh 时保留旧值 ——
// 中间件用它做"绝对 TTL"校验,超过 cfg.JWT.AbsoluteSessionTTL 强制重登,
// 避免长期活跃被盗 token 通过定期 refresh 持续等于账号寿命的问题。
type SessionInfo struct {
	JTI            string `json:"jti"`
	DeviceName     string `json:"device_name"`
	LoginIP        string `json:"login_ip"`
	LoginAt        int64  `json:"login_at"`
	SessionStartAt int64  `json:"session_start_at,omitempty"` // 0 兼容历史 session,中间件 fallback LoginAt
}

// SessionEntry 用于列表展示的 session 条目,包含 device_id。
type SessionEntry struct {
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`
	LoginIP    string `json:"login_ip"`
	LoginAt    int64  `json:"login_at"`
}

// SessionStore 管理用户多设备 session 的存储接口。
//
// Redis key 格式: synapse:session:{user_id}:{device_id}
// TTL = refresh token 有效期,每次 refresh 重置。
type SessionStore interface {
	// Save 创建或更新一个设备 session。
	Save(ctx context.Context, userID uint64, deviceID string, info SessionInfo, ttl time.Duration) error
	// Get 获取指定设备的 session,不存在时返回 error。
	Get(ctx context.Context, userID uint64, deviceID string) (*SessionInfo, error)
	// List 返回用户的所有活跃 session。
	List(ctx context.Context, userID uint64) ([]SessionEntry, error)
	// Delete 删除指定设备的 session(踢下线)。
	Delete(ctx context.Context, userID uint64, deviceID string) error
	// DeleteAll 删除用户的所有 session(退出所有设备)。
	DeleteAll(ctx context.Context, userID uint64) error
}
