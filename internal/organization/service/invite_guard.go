// invite_guard.go 邀请邮件发送的 Redis 限流抽象。
//
// 四道闸(Create 走三道、Resend 走四道):
//
//   synapse:inv_resend_cd:{invitation_id}      60s    cooldown   防 Resend 按钮轰炸
//   synapse:inv_send_email:{email}:{date}      24h    daily cap  跨 org/inviter 的受害者防护
//   synapse:inv_send_inviter:{user_id}:{date}  24h    daily cap  单 inviter 爆发上限
//   synapse:inv_send_org:{org_id}:{date}       24h    daily cap  单 org 兜底
//
// 实现直接包 RedisInterface.IncrAndExpireIfNew,和 user 模块 LoginGuard 保持
// 行为一致但不跨模块 import —— InvitationService 需要的就这两个原语,抽不到
// 公共包以降低耦合成本。
package service

import (
	"context"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/common/database"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
)

// InviteGuard 邀请邮件发送限流的抽象,便于 service 层测试注入 fake。
type InviteGuard interface {
	// TouchCounter 原子 INCR + EXPIRE(首次创建才设 TTL),返回递增后的当前值。
	// 调用方按阈值决策:cooldown 场景 count>1 拒,daily cap 场景 count>threshold 拒。
	//
	// 约定:key 必须由调用方预拼好(含 synapse: 前缀),并登记到
	// internal/common/database/redis.go 的 Key Registry。
	TouchCounter(ctx context.Context, key string, ttl time.Duration) (int64, error)
}

type redisInviteGuard struct {
	redis database.RedisInterface
	log   logger.LoggerInterface
}

// NewInviteGuard 基于 Redis 的 InviteGuard 实现。
func NewInviteGuard(rdb database.RedisInterface, log logger.LoggerInterface) InviteGuard {
	return &redisInviteGuard{redis: rdb, log: log}
}

// TouchCounter INCR + EXPIRE IfNew。Redis 故障打 ErrorCtx + 返 (0, err),
// 调用方通常 fail-open(允许发邮件),避免 Redis 挂整条主流程跟着挂。
func (g *redisInviteGuard) TouchCounter(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	n, err := g.redis.IncrAndExpireIfNew(ctx, key, ttl)
	if err != nil {
		g.log.ErrorCtx(ctx, "InviteGuard TouchCounter 失败", err, map[string]any{"key": key})
		return 0, fmt.Errorf("touch counter: %w", err)
	}
	return n, nil
}
