// login_audit.go 登录事件审计 + 新设备通知。
//
// 设计口径:
//   - 所有"record"方法都是 best-effort:DB/邮件失败不回滚业务,只打 log
//   - 同步写入 login_events 表;如果未来数据量大需要异步化,再引 AsyncRunner 改造
//   - 新设备判定:只看 success 类事件历史(password_success/oauth_success),失败事件不算"见过此设备"
//
// 调用点:
//   - service.go Register 成功 → recordRegister
//   - service.go Login 成功 / 失败 / 账号锁 → recordLoginSuccess / recordLoginFailure / recordAccountLocked
//   - oauth_login.go finishOAuthLogin 成功 → recordOAuthSuccess
package service

import (
	"context"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/user/model"
	"github.com/eyrihe999-stack/Synapse/internal/common/email"
)

// loginEventTimestampFormat 通知邮件里"When"字段的渲染格式。
const loginEventTimestampFormat = "2006-01-02 15:04:05 UTC"

// loginFailRecKeyPrefix per-email 每日失败事件写入上限的 Redis key 前缀。
// 完整 key: synapse:login_fail_rec:{email}:{YYYY-MM-DD} (UTC)
// 已登记到 internal/common/database/redis.go 的 Key Registry。
const loginFailRecKeyPrefix = "synapse:login_fail_rec"

// newDeviceMailKeyPrefix 新设备登录通知邮件的 per-user 日限 key 前缀。
// 完整 key: synapse:new_device_mail:{user_id}:{YYYY-MM-DD}
const newDeviceMailKeyPrefix = "synapse:new_device_mail"

// loginIPFailKeyPrefix per-IP 登录失败滑动窗口的 key 前缀 (P3)。
// 完整 key: synapse:login_ip_fail:{ip};TTL 15min,到期自解锁。
const loginIPFailKeyPrefix = "synapse:login_ip_fail"

// loginIPFailMax per-IP 登录失败上限。超过直接拒绝 15min。
// per-email 是 10 次(不同 email 各自独立);这层是跨 email 的 spray 防御,所以设更高的 20。
const loginIPFailMax = 20

// loginIPFailTTL per-IP 锁定时长。和 per-email 锁同 15min,保持行为一致。
const loginIPFailTTL = 15 * time.Minute

// loginIPFailKey 拼 per-IP 失败限流的完整 key。
func loginIPFailKey(ip string) string {
	return fmt.Sprintf("%s:%s", loginIPFailKeyPrefix, ip)
}

// isLoginIPRateLimited 入口前置检查:该 IP 当前累计失败 ≥ loginIPFailMax 就拒登。
// 只读 GetCounter,不递增(递增发生在失败路径的 incrLoginIPFail)。
func (s *userService) isLoginIPRateLimited(ctx context.Context, ip string) bool {
	if s.loginGuard == nil || ip == "" {
		return false
	}
	count, err := s.loginGuard.GetCounter(ctx, loginIPFailKey(ip))
	if err != nil {
		return false // fail-open
	}
	return count >= loginIPFailMax
}

// incrLoginIPFail 登录失败后 +1。
// 首次创建时设 15min TTL,在窗口内叠加;超过 loginIPFailMax 下次 isLoginIPRateLimited 就会拦。
func (s *userService) incrLoginIPFail(ctx context.Context, ip string) {
	if s.loginGuard == nil || ip == "" {
		return
	}
	//sayso-lint:ignore err-swallow
	_, _ = s.loginGuard.TouchCounter(ctx, loginIPFailKey(ip), loginIPFailTTL) // best-effort
}

// dailyFailureRecordCap per-email 每日最多写几条登录失败事件(password_failed + account_locked 合计)。
// 目的:防止攻击者用 spray 失败登录撑爆 login_events 表;首条就足以识别爆破信号,
// 后续锁定期内的重复尝试直接 skip 写入。设 20 留余量,正常用户一天不会达到。
const dailyFailureRecordCap = 20

// dailyNewDeviceMailCap per-user 每日最多发几封"新设备登录"告警邮件。
// 目的:防攻击者拿被盗凭证反复换 device_id 登录轰炸受害者邮箱。设 5 封够识别首次异常,
// 之后的同类事件只走审计表,不再打扰。
const dailyNewDeviceMailCap = 5

// shouldRecordFailure 判定本次失败事件是否该写入 login_events。
// 走 LoginGuard.TouchCounter 原子 INCR + EXPIRE IfNew,超过 dailyFailureRecordCap 返 false。
// Redis 故障时 fail-open(宁可多记也别漏记真爆破信号)。
func (s *userService) shouldRecordFailure(ctx context.Context, email string) bool {
	if s.loginGuard == nil || email == "" {
		return true
	}
	key := fmt.Sprintf("%s:%s:%s", loginFailRecKeyPrefix, email, time.Now().UTC().Format("2006-01-02"))
	count, err := s.loginGuard.TouchCounter(ctx, key, 24*time.Hour)
	if err != nil {
		return true // fail-open
	}
	return count <= dailyFailureRecordCap
}

// recordLoginSuccess 记成功登录;首次见到 (userID, deviceID) 组合时发新设备通知邮件。
//
// 参数:
//   - userID:已命中 user 的 ID
//   - email:落 DB 用,便于日志按邮箱检索
//   - deviceID/ip/userAgent:从 handler 透传
//   - eventType:区分 password_success / oauth_success,给通知邮件判断来源用
func (s *userService) recordLoginSuccess(ctx context.Context, userID uint64, emailAddr, deviceID, ip, userAgent, eventType string) {
	now := time.Now().UTC()

	// 先做新设备检测(必须在 Create 前,否则自己刚写的事件就命中 HasDeviceSeen)
	isNewDevice := s.isNewDevice(ctx, userID, deviceID)

	//sayso-lint:ignore err-swallow
	_ = s.repo.CreateLoginEvent(ctx, &model.LoginEvent{
		UserID:    userID,
		Email:     emailAddr,
		EventType: eventType,
		DeviceID:  deviceID,
		IP:        ip,
		UserAgent: userAgent,
		CreatedAt: now,
	}) // best-effort,失败已在 repo 层打 log

	if isNewDevice {
		s.notifyNewDeviceLogin(ctx, userID, emailAddr, ip, userAgent, now)
	}
}

// recordLoginFailure 记失败登录(密码错/验证码错/账号不存在)。
// userID 可能为 0(账号不存在);Email 必传,用于按邮箱聚合看被针对性爆破的风险。
// 前置走 shouldRecordFailure per-email 每日配额,防爆破撑爆表。
func (s *userService) recordLoginFailure(ctx context.Context, userID uint64, emailAddr, deviceID, ip, userAgent, reason string) {
	if !s.shouldRecordFailure(ctx, emailAddr) {
		return
	}
	//sayso-lint:ignore err-swallow
	_ = s.repo.CreateLoginEvent(ctx, &model.LoginEvent{
		UserID:    userID,
		Email:     emailAddr,
		EventType: model.LoginEventPasswordFailed,
		DeviceID:  deviceID,
		IP:        ip,
		UserAgent: userAgent,
		Reason:    reason,
		CreatedAt: time.Now().UTC(),
	}) // best-effort
}

// recordAccountLocked 记账号因连续失败触顶被锁。userID 通常此时也未命中(账号可能不存在),保持 0 兜底。
// 和 password_failed 共享同一个 daily cap:锁定期内攻击者反复尝试不会继续撑表。
func (s *userService) recordAccountLocked(ctx context.Context, emailAddr, deviceID, ip, userAgent string) {
	if !s.shouldRecordFailure(ctx, emailAddr) {
		return
	}
	//sayso-lint:ignore err-swallow
	_ = s.repo.CreateLoginEvent(ctx, &model.LoginEvent{
		Email:     emailAddr,
		EventType: model.LoginEventAccountLocked,
		DeviceID:  deviceID,
		IP:        ip,
		UserAgent: userAgent,
		CreatedAt: time.Now().UTC(),
	}) // best-effort
}

// recordRegister 记注册事件(不走新设备通知 —— 刚注册的账号根本不存在"旧设备")。
func (s *userService) recordRegister(ctx context.Context, userID uint64, emailAddr, deviceID, ip, userAgent string) {
	//sayso-lint:ignore err-swallow
	_ = s.repo.CreateLoginEvent(ctx, &model.LoginEvent{
		UserID:    userID,
		Email:     emailAddr,
		EventType: model.LoginEventRegister,
		DeviceID:  deviceID,
		IP:        ip,
		UserAgent: userAgent,
		CreatedAt: time.Now().UTC(),
	}) // best-effort
}

// isNewDevice 查 (userID, deviceID) 以前是否成功登录过。
// 失败或不确定一律返 false —— 宁可漏发一次通知也不要因审计表故障妨碍登录主流程。
func (s *userService) isNewDevice(ctx context.Context, userID uint64, deviceID string) bool {
	if userID == 0 || deviceID == "" {
		return false
	}
	seen, err := s.repo.HasDeviceSeen(ctx, userID, deviceID)
	if err != nil {
		s.log.WarnCtx(ctx, "查询新设备失败,跳过通知", map[string]interface{}{
			"user_id": userID, "device_id": deviceID, "err": err.Error(),
		})
		return false
	}
	return !seen
}

// notifyNewDeviceLogin best-effort 发新设备登录通知邮件,带 per-user 每日上限。
// 第 N 次(count > dailyNewDeviceMailCap)直接 skip 不发,但审计事件照常写。
func (s *userService) notifyNewDeviceLogin(ctx context.Context, userID uint64, emailAddr, ip, userAgent string, when time.Time) {
	if s.emailSender == nil || s.emailCfg == nil {
		return
	}
	if emailAddr == "" {
		return
	}
	// per-user daily cap 防轰炸:TouchCounter 原子 INCR + EXPIRE(首次建立时 TTL 24h)。
	// 超限直接 skip 邮件 —— 攻击者拿凭证 + 疯狂换 device_id 也最多打扰 dailyNewDeviceMailCap 封。
	if s.loginGuard != nil {
		key := fmt.Sprintf("%s:%d:%s", newDeviceMailKeyPrefix, userID, when.Format("2006-01-02"))
		count, cErr := s.loginGuard.TouchCounter(ctx, key, 24*time.Hour)
		if cErr == nil && count > dailyNewDeviceMailCap {
			s.log.WarnCtx(ctx, "新设备登录通知邮件触发日限,本次跳过", map[string]interface{}{
				"user_id": userID, "count": count, "cap": dailyNewDeviceMailCap,
			})
			return
		}
	}
	locale := s.emailCfg.Locale
	whenStr := when.Format(loginEventTimestampFormat)
	subject, body := email.BuildNewDeviceLoginEmail(locale, whenStr, ip, userAgent)
	if sendErr := s.emailSender.SendVerificationEmail(ctx, emailAddr, subject, body); sendErr != nil {
		s.log.WarnCtx(ctx, "新设备登录通知邮件发送失败", map[string]interface{}{
			"user_id": userID, "email": emailAddr, "err": sendErr.Error(),
		})
		return
	}
	s.log.InfoCtx(ctx, "新设备登录通知邮件已发送", map[string]interface{}{
		"user_id": userID, "email": emailAddr, "ip": ip,
	})
}
