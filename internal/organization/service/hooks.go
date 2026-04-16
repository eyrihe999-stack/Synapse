// hooks.go 跨模块联动的回调注册机制。
//
// organization 模块在成员被移除或 org 解散时,需要通知 agent 模块
// 级联处理其发布关系(把 agent 对该 org 的 publish binding 标记为 revoked)。
//
// 为了保持依赖方向单向(agent → organization 而不是反向),这里用 hook 注入模式:
//
//  1. organization 模块在 service 包中定义两个回调类型(OnMemberRemovedHook /
//     OnOrgDissolvedHook)和 HookRegistry
//  2. agent 模块实现这些回调(publishSvc.RevokeByMember / RevokeByOrg)
//  3. main.go 在启动时调用 hooks.RegisterXxx 注入 agent 的实现
//  4. organization 在主事务提交后调用 hooks.FireXxx 同步触发所有回调
//
// 关键约束:
//   - Hooks 在主事务提交之后同步执行(不走 AsyncRunner),保证业务强一致
//   - Hook 执行失败只打 ErrorCtx,不中断主流程(避免 hook 失败影响主业务)
//   - gateway 层的 lazy filter 作为兜底,即使 hook 丢了也能避免调用到过期 binding
//
// 这是目前 sayso 里首次使用该 pattern,新模块要注意:
//   - 不要在 hook 里做耗时操作(会阻塞主请求返回)
//   - 不要在 hook 里再触发新的 hook(避免级联耦合)
package service

import (
	"context"
	"sync"

	"github.com/eyrihe999-stack/Synapse/pkg/logger"
)

// OnMemberRemovedHook 在成员从 org 中被移除后调用。
// 参数:
//   - ctx: 请求上下文
//   - orgID: 被移除成员所在的 org
//   - userID: 被移除的成员 user_id
//   - reason: 移除原因(RoleChangeReasonLeave 等)
//
// 返回 error 会被记录但不会中断主流程。
type OnMemberRemovedHook func(ctx context.Context, orgID, userID uint64, reason string) error

// OnOrgDissolvedHook 在 org 被解散后调用。
type OnOrgDissolvedHook func(ctx context.Context, orgID uint64) error

// HookRegistry 集中管理外部注册的回调。线程安全(启动时注册,运行时只读)。
type HookRegistry struct {
	mu            sync.RWMutex
	memberRemoved []OnMemberRemovedHook
	orgDissolved  []OnOrgDissolvedHook
	logger        logger.LoggerInterface
}

// NewHookRegistry 构造一个 HookRegistry。logger 用于 hook 执行失败时的错误日志。
func NewHookRegistry(log logger.LoggerInterface) *HookRegistry {
	return &HookRegistry{logger: log}
}

// snapshotMemberRemoved 在读锁内拷贝一份 hook 切片,defer 释放锁以 panic-safe。
func (r *HookRegistry) snapshotMemberRemoved() []OnMemberRemovedHook {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]OnMemberRemovedHook, len(r.memberRemoved))
	copy(out, r.memberRemoved)
	return out
}

// snapshotOrgDissolved 在读锁内拷贝一份 hook 切片,defer 释放锁以 panic-safe。
func (r *HookRegistry) snapshotOrgDissolved() []OnOrgDissolvedHook {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]OnOrgDissolvedHook, len(r.orgDissolved))
	copy(out, r.orgDissolved)
	return out
}

// RegisterMemberRemoved 注册成员移除回调。
// 典型调用方:main.go 初始化时 agentPublishSvc.RevokeByMember 通过此方法注入。
func (r *HookRegistry) RegisterMemberRemoved(h OnMemberRemovedHook) {
	if h == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.memberRemoved = append(r.memberRemoved, h)
}

// RegisterOrgDissolved 注册 org 解散回调。
func (r *HookRegistry) RegisterOrgDissolved(h OnOrgDissolvedHook) {
	if h == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.orgDissolved = append(r.orgDissolved, h)
}

// FireMemberRemoved 同步调用所有已注册的 OnMemberRemovedHook。
// 任一 hook 返回 error 会被记录为 ErrorCtx 但不会中断后续 hook 的执行。
func (r *HookRegistry) FireMemberRemoved(ctx context.Context, orgID, userID uint64, reason string) {
	hooks := r.snapshotMemberRemoved()
	for i, h := range hooks {
		if err := h(ctx, orgID, userID, reason); err != nil && r.logger != nil {
			r.logger.ErrorCtx(ctx, "OnMemberRemoved hook 执行失败", err, map[string]any{
				"hook_index": i,
				"org_id":     orgID,
				"user_id":    userID,
				"reason":     reason,
			})
		}
	}
}

// FireOrgDissolved 同步调用所有已注册的 OnOrgDissolvedHook。
func (r *HookRegistry) FireOrgDissolved(ctx context.Context, orgID uint64) {
	hooks := r.snapshotOrgDissolved()
	for i, h := range hooks {
		if err := h(ctx, orgID); err != nil && r.logger != nil {
			r.logger.ErrorCtx(ctx, "OnOrgDissolved hook 执行失败", err, map[string]any{
				"hook_index": i,
				"org_id":     orgID,
			})
		}
	}
}
