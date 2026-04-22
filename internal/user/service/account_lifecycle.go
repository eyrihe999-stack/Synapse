// account_lifecycle.go M1.7 用户账号生命周期管理:自助注销。
//
// 本文件聚焦注销(status active/banned/pending_verify → deleted)的业务编排。
// 注销采用"pseudo 化"模式:行保留但 PII 清空 + status=deleted + deleted_at 落时间,
// 后续一切查询按"不存在"处理。
//
// GDPR 物理抹除(硬删 users 行 + 跨表级联清理)暂不实现 —— 等系统成熟、
// 跨模块 FK 和清理职责理顺后再统一规划。注销后的壳数据占用极小,短期可接受。
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/user"
	"github.com/eyrihe999-stack/Synapse/internal/user/model"
	"github.com/eyrihe999-stack/Synapse/internal/user/repository"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// DeleteAccountRequest 自助注销请求。
//
// Password 对"有本地密码"的用户强制(服务端 PasswordHash != "" 时必校验);
// OAuth-only 用户(PasswordHash == "")跳过密码校验 —— JWT + session 已证身份,
// 他们也没有"原始密码"可输。
//
// Reason 可选,仅作审计记录,上限 64 字节;前端可做下拉选项,也可自由填写。
type DeleteAccountRequest struct {
	Password string `json:"password"`
	Reason   string `json:"reason"`
}

// pseudoEmailFormat 注销后 email 的 pseudo 化格式。
// `@synapse.invalid` 走 RFC 6761 保留 TLD,保证不会意外送到真实邮箱。
// 带 user_id 保证不同 deleted 用户之间 pseudo email 唯一,仍可命中 unique index。
const pseudoEmailFormat = "deleted+%d@synapse.invalid"

// pseudoDisplayName 注销后的占位昵称。统一写死,不带 user_id 避免在 UI 暴露 id。
const pseudoDisplayName = "已注销用户"

// deleteReasonMaxLen 注销原因最大长度,与 DB 列宽一致(model.User.DeletedReason 64)。
const deleteReasonMaxLen = 64

// ExpireStats "清理过期 pending_verify 账号"一轮的统计。
type ExpireStats struct {
	Scanned int // 本轮扫出的过期 pending_verify 数
	Expired int // 实际 pseudo 化成功数
	Failed  int // 失败数(单条失败不中断批次)
}

// expireReasonPendingVerify 标记这批 user 被清理的原因。进 deleted_reason 字段供审计。
const expireReasonPendingVerify = "pending_verify_expired"

// ExpireStalePendingVerifyAccounts 清理长期未激活的 pending_verify 账号。
//
// 攻击面背景:OAuth 注册 email_verified=false 的场景会直接落 pending_verify,
// 永不过期则被攻击者拿来占位他人邮箱(真用户后续本地注册会撞 unique index)。
// 本方法扫 created_at 老于 staleDuration 的 pending_verify,走 pseudo 化流程:
//   - email → deleted+<uid>@synapse.invalid 释放原 email
//   - status → deleted,deleted_at=now(),deleted_reason="pending_verify_expired"
//   - 删 user 的 identity 绑定
//
// 这不是硬删,行保留;GDPR 物理抹除等系统成熟后统一规划。
//
// 幂等:pending_verify 过期 → 一次性 pseudo 化,下次扫不到。
//
// 返回 ExpireStats(扫出/成功/失败计数);错误场景:
//   - 扫描失败 → 返回 ErrUserInternal
//   - 单条 pseudo 化失败不中断批次,计入 stats.Failed 并继续
func (s *userService) ExpireStalePendingVerifyAccounts(ctx context.Context, staleDuration time.Duration, batchLimit int) (ExpireStats, error) {
	var stats ExpireStats
	cutoff := time.Now().UTC().Add(-staleDuration)
	ids, err := s.repo.ListStalePendingVerify(ctx, cutoff, batchLimit)
	if err != nil {
		s.log.ErrorCtx(ctx, "cleanup:扫过期 pending_verify 失败", err, map[string]interface{}{"cutoff": cutoff})
		return stats, fmt.Errorf("list stale: %w: %w", err, user.ErrUserInternal)
	}
	stats.Scanned = len(ids)
	now := time.Now().UTC()
	for _, id := range ids {
		updates := map[string]any{
			"email":           fmt.Sprintf(pseudoEmailFormat, id),
			"password_hash":   "",
			"display_name":    pseudoDisplayName,
			"avatar_url":      "",
			"last_login_at":   nil,
			"status":          model.StatusDeleted,
			"deleted_at":      now,
			"deleted_reason":  expireReasonPendingVerify,
		}
		if txErr := s.repo.WithTx(ctx, func(tx repository.Repository) error {
			if err := tx.DeleteIdentitiesByUserID(ctx, id); err != nil {
				//sayso-lint:ignore sentinel-wrap,log-coverage
				return err // 外层 txErr 统一 log+wrap
			}
			//sayso-lint:ignore sentinel-wrap,log-coverage
			return tx.MarkUserDeleted(ctx, id, updates) // 外层 txErr 统一 log+wrap
		}); txErr != nil {
			s.log.ErrorCtx(ctx, "cleanup:pending_verify pseudo 化失败", txErr, map[string]interface{}{"user_id": id})
			stats.Failed++
			continue
		}
		stats.Expired++
	}
	if stats.Scanned > 0 {
		s.log.InfoCtx(ctx, "cleanup 批次完成", map[string]interface{}{
			"cutoff": cutoff, "scanned": stats.Scanned, "expired": stats.Expired, "failed": stats.Failed,
		})
	}
	return stats, nil
}

// DeleteAccount 自助注销当前账号。
//
// 流程:
//  1. 查 living 用户(含 pending_verify/banned/active);已 deleted 返 ErrAccountAlreadyDeleted
//  2. 如果用户有 PasswordHash:必须校验 req.Password,避免"盗 session 注销受害者账号"
//  3. 事务内:pseudo 化 users 行 + 删 user_identities + 标 status=deleted
//  4. 事务外(失败不影响注销既成事实):踢全部 session
//
// 成功后 access token 在下一次 session 校验时会被判无效(session 已清),
// 客户端当次请求正常返回 —— 不在响应里塞任何 token。
//
// 返回 ErrUserNotFound / ErrAccountAlreadyDeleted / ErrInvalidCredentials /
//
//	ErrDeletePasswordRequired / ErrUserInternal。
func (s *userService) DeleteAccount(ctx context.Context, userID uint64, req DeleteAccountRequest) error {
	u, err := s.repo.FindLivingByID(ctx, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.log.WarnCtx(ctx, "注销:用户不存在或已 deleted", nil)
			return fmt.Errorf("find user: %w", user.ErrUserNotFound)
		}
		s.log.ErrorCtx(ctx, "注销:查询用户失败", err, nil)
		return fmt.Errorf("find user: %w: %w", err, user.ErrUserInternal)
	}

	// 有本地密码的用户必须二次确认;OAuth-only 账号 PasswordHash 为空,跳过。
	if u.PasswordHash != "" {
		if req.Password == "" {
			s.log.WarnCtx(ctx, "注销:本地账号未提供密码二次确认", nil)
			return fmt.Errorf("password required: %w", user.ErrDeletePasswordRequired)
		}
		if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)); err != nil {
			s.log.WarnCtx(ctx, "注销:密码二次确认失败", nil)
			return fmt.Errorf("password mismatch: %w", user.ErrInvalidCredentials)
		}
	}

	// M3.7 owner 孤儿态 guard:用户若是任一 active org 的 owner,阻塞注销,
	// 前端引导转让(走 ownership_transfer 邀请)或解散 org。
	if s.ownerChecker != nil {
		//sayso-lint:ignore err-shadow
		ownedOrgs, err := s.ownerChecker.ListActiveOrgsOwnedBy(ctx, u.ID)
		if err != nil {
			s.log.ErrorCtx(ctx, "注销:查询 owner 身份失败", err, map[string]interface{}{"user_id": u.ID})
			return fmt.Errorf("check owner: %w: %w", err, user.ErrUserInternal)
		}
		if len(ownedOrgs) > 0 {
			s.log.WarnCtx(ctx, "注销:用户仍持有 active org,阻塞自注销", map[string]interface{}{
				"user_id": u.ID, "org_count": len(ownedOrgs),
			})
			//sayso-lint:ignore sentinel-wrap
			return &user.OwnerOfActiveOrgsError{Orgs: ownedOrgs}
		}
	}

	reason := req.Reason
	if len(reason) > deleteReasonMaxLen {
		reason = reason[:deleteReasonMaxLen]
	}

	now := time.Now().UTC()
	updates := map[string]any{
		"email":           fmt.Sprintf(pseudoEmailFormat, u.ID),
		"password_hash":   "",
		"display_name":    pseudoDisplayName,
		"avatar_url":      "",
		"last_login_at":   nil,
		"status":          model.StatusDeleted,
		"deleted_at":      now,
		"deleted_reason":  reason,
	}

	if err := s.repo.WithTx(ctx, func(tx repository.Repository) error {
		if err := tx.DeleteIdentitiesByUserID(ctx, u.ID); err != nil {
			//sayso-lint:ignore log-coverage
			return fmt.Errorf("delete identities: %w", err) // 外层 err 统一 log
		}
		if err := tx.MarkUserDeleted(ctx, u.ID, updates); err != nil {
			//sayso-lint:ignore log-coverage
			return fmt.Errorf("mark user deleted: %w", err) // 外层 err 统一 log
		}
		return nil
	}); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// MarkUserDeleted 的 RowsAffected=0 分支:竞争场景下另一请求先完成注销。
			s.log.WarnCtx(ctx, "注销:事务内检测到已 deleted(竞争)", nil)
			return fmt.Errorf("already deleted: %w", user.ErrAccountAlreadyDeleted)
		}
		s.log.ErrorCtx(ctx, "注销事务失败", err, nil)
		return fmt.Errorf("delete account tx: %w: %w", err, user.ErrUserInternal)
	}

	// session 踢下线放在事务外:就算 Redis 挂了也不回滚已落库的 pseudo 化;
	// best-effort 失败仅告警,客户端持有的 access token 会在过期后自然失效,
	// session 查询走 Redis 拿不到后也立即无法续期。
	if err := s.sessionStore.DeleteAll(ctx, u.ID); err != nil {
		s.log.WarnCtx(ctx, "注销:清理 session 失败,但账号已 pseudo 化", map[string]interface{}{
			"user_id": u.ID, "err": err.Error(),
		})
	}

	s.log.InfoCtx(ctx, "账号已注销", map[string]interface{}{
		"user_id": u.ID, "reason": reason,
	})
	return nil
}
