// error_map.go user 模块 service 错误 → HTTP 响应的映射。
//
// 约定:
//   - 业务错误一律 HTTP 200 + body 业务码
//   - 仅 ErrUserInternal 走 500
package handler

import (
	"errors"
	"net/http"

	"github.com/eyrihe999-stack/Synapse/internal/user"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	"github.com/gin-gonic/gin"
)

// ErrOwnerOfActiveOrgs 的映射走下面 errors.As(*OwnerOfActiveOrgsError) 路径,
// 以便把 orgs 列表透出给前端;此处显式引用以让 error-map-completeness 跨文件检查识别覆盖关系。
var _ = user.ErrOwnerOfActiveOrgs

// handleServiceError 把 service 层的错误映射为 HTTP 响应。
func (h *Handler) handleServiceError(c *gin.Context, err error) {
	ctx := c.Request.Context()

	// M3.7 owner 孤儿态 guard 错误 —— 结构体携带阻塞列表,用 errors.As 提取后塞进响应 Result。
	// 放在 switch 之前,避免 errors.Is 命中祖先 sentinel 走通用分支漏掉 org 列表。
	var ownerErr *user.OwnerOfActiveOrgsError
	if errors.As(err, &ownerErr) {
		h.log.WarnCtx(ctx, "注销被 owner 身份阻塞", nil)
		c.JSON(http.StatusOK, response.BaseResponse{
			Code:    user.CodeOwnerOfActiveOrgs,
			Message: "Cannot delete account while you still own active organizations. Transfer ownership or dissolve them first.",
			Result:  map[string]any{"orgs": ownerErr.Orgs},
		})
		return
	}

	switch {
	// ─── 400 段 ─────
	case errors.Is(err, user.ErrInvalidEmail):
		h.log.WarnCtx(ctx, "邮箱格式非法", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeInvalidEmail, Message: "Invalid email format"})
	case errors.Is(err, user.ErrPasswordTooShort):
		h.log.WarnCtx(ctx, "密码长度不足", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodePasswordTooShort, Message: "Password too short"})
	case errors.Is(err, user.ErrPasswordTooCommon):
		h.log.WarnCtx(ctx, "密码过于常见", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodePasswordTooCommon, Message: "Password too common, please pick a more unique one"})
	case errors.Is(err, user.ErrInvalidEmailCode):
		h.log.WarnCtx(ctx, "邮箱验证码错误", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeInvalidEmailCode, Message: "Invalid verification code"})
	case errors.Is(err, user.ErrEmailCodeNotFound):
		h.log.WarnCtx(ctx, "邮箱验证码不存在或已过期", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeEmailCodeNotFound, Message: "Verification code expired or not found"})
	case errors.Is(err, user.ErrDailyLimitReached):
		h.log.WarnCtx(ctx, "单日发送次数达到上限", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeDailyLimitReached, Message: "Daily verification limit reached"})
	case errors.Is(err, user.ErrResetTokenInvalid):
		h.log.WarnCtx(ctx, "密码重置 token 无效或已过期", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeResetTokenInvalid, Message: "Reset link invalid or expired"})
	case errors.Is(err, user.ErrAccountLocked):
		h.log.WarnCtx(ctx, "账号临时锁定", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeAccountLocked, Message: "Account temporarily locked due to too many failed attempts"})
	case errors.Is(err, user.ErrRegisterRateExceeded):
		h.log.WarnCtx(ctx, "注册请求过于频繁", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeRegisterRateExceeded, Message: "Too many register requests, please try again later"})
	case errors.Is(err, user.ErrOAuthStateInvalid):
		h.log.WarnCtx(ctx, "OAuth state 校验失败", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeOAuthStateInvalid, Message: "OAuth state invalid or expired"})
	case errors.Is(err, user.ErrOAuthEmailUnverified):
		h.log.WarnCtx(ctx, "OAuth email 未验证", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeOAuthEmailUnverified, Message: "OAuth provider returned unverified email, cannot link existing account"})
	case errors.Is(err, user.ErrOAuthProviderDisabled):
		h.log.WarnCtx(ctx, "OAuth provider 未启用", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeOAuthProviderDisabled, Message: "OAuth provider not enabled"})
	case errors.Is(err, user.ErrOAuthExchangeExpired):
		h.log.WarnCtx(ctx, "OAuth exchange 已过期", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeOAuthExchangeExpired, Message: "Login session expired, please try again"})
	case errors.Is(err, user.ErrAccountAlreadyDeleted):
		h.log.WarnCtx(ctx, "账号已注销,重复调用", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeAccountAlreadyDeleted, Message: "Account already deleted"})
	case errors.Is(err, user.ErrDeletePasswordRequired):
		h.log.WarnCtx(ctx, "注销未提供密码二次确认", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeDeletePasswordRequired, Message: "Password required to delete account"})
	case errors.Is(err, user.ErrVerifyTokenInvalid):
		h.log.WarnCtx(ctx, "邮箱激活 token 无效/过期", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeVerifyTokenInvalid, Message: "Verification link invalid or expired"})
	case errors.Is(err, user.ErrEmailAlreadyVerified):
		h.log.InfoCtx(ctx, "邮箱已验证,无需再次激活", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeEmailAlreadyVerified, Message: "Email already verified"})
	case errors.Is(err, user.ErrAccountPendingVerify):
		h.log.WarnCtx(ctx, "账号邮箱未验证,操作被拦", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeAccountPendingVerify, Message: "Please verify your email before performing this action"})
	case errors.Is(err, user.ErrEmailSameAsCurrent):
		h.log.WarnCtx(ctx, "改邮箱:新邮箱和当前邮箱相同", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeEmailSameAsCurrent, Message: "New email is the same as the current email"})
	case errors.Is(err, user.ErrChangePasswordCodeRequired):
		h.log.WarnCtx(ctx, "OAuth-only 账号改密未提供邮箱 code", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeChangePasswordCodeRequired, Message: "Please verify with a code sent to your email before setting a password"})
	case errors.Is(err, user.ErrLocalPasswordRequired):
		h.log.WarnCtx(ctx, "OAuth-only 账号改邮箱被拦,要求先设本地密码", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeLocalPasswordRequired, Message: "Set a local password before changing email"})
	case errors.Is(err, user.ErrResendTooFrequent):
		h.log.WarnCtx(ctx, "重发激活邮件过于频繁", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeResendTooFrequent, Message: "Please wait before requesting another verification email"})
	case errors.Is(err, user.ErrRequestTooFrequent):
		h.log.WarnCtx(ctx, "请求过于频繁,触发 cooldown", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeRequestTooFrequent, Message: "Please wait a moment before trying again"})
	case errors.Is(err, user.ErrSessionExpired):
		h.log.WarnCtx(ctx, "会话绝对 TTL 到期", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeSessionExpired, Message: "Session expired, please login again"})
	case errors.Is(err, user.ErrLoginIPRateLimited):
		h.log.WarnCtx(ctx, "该 IP 登录失败次数过多,被临时锁定", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeLoginIPRateLimited, Message: "Too many failed login attempts from this IP, please try later"})

	// ─── 401 段 ─────
	case errors.Is(err, user.ErrInvalidCredentials):
		h.log.WarnCtx(ctx, "登录凭证无效", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeInvalidCredentials, Message: "Invalid credentials"})
	case errors.Is(err, user.ErrInvalidRefreshToken):
		h.log.WarnCtx(ctx, "refresh token 无效", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeInvalidRefreshToken, Message: "Invalid refresh token"})

	case errors.Is(err, user.ErrSessionLimitReached):
		h.log.WarnCtx(ctx, "设备数量已达上限", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeSessionLimitReached, Message: "Session limit reached, please logout from another device first"})

	case errors.Is(err, user.ErrSessionRevoked):
		h.log.WarnCtx(ctx, "session 已被吊销", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeSessionRevoked, Message: "Session revoked"})

	// ─── 404 段 ─────
	case errors.Is(err, user.ErrSessionNotFound):
		h.log.WarnCtx(ctx, "session 不存在", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeSessionNotFound, Message: "Session not found"})
	case errors.Is(err, user.ErrUserNotFound):
		h.log.WarnCtx(ctx, "用户不存在", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeUserNotFound, Message: "User not found"})

	// ─── 409 段 ─────
	case errors.Is(err, user.ErrEmailAlreadyRegistered):
		h.log.WarnCtx(ctx, "邮箱已注册", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeEmailAlreadyRegistered, Message: "Email already registered"})

	// ─── 500 段 ─────
	case errors.Is(err, user.ErrUserInternal):
		h.log.ErrorCtx(ctx, "user 内部错误", err, nil)
		response.InternalServerError(c, "Internal server error", "")

	default:
		h.log.ErrorCtx(ctx, "未映射的 user 错误", err, nil)
		response.InternalServerError(c, "Internal server error", "")
	}
}
