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
	"github.com/eyrihe999-stack/Synapse/pkg/response"
	"github.com/gin-gonic/gin"
)

// handleServiceError 把 service 层的错误映射为 HTTP 响应。
func (h *Handler) handleServiceError(c *gin.Context, err error) {
	ctx := c.Request.Context()

	switch {
	// ─── 400 段 ─────
	case errors.Is(err, user.ErrInvalidEmail):
		h.log.WarnCtx(ctx, "邮箱格式非法", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeInvalidEmail, Message: "Invalid email format"})
	case errors.Is(err, user.ErrPasswordTooShort):
		h.log.WarnCtx(ctx, "密码长度不足", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodePasswordTooShort, Message: "Password too short"})

	// ─── 401 段 ─────
	case errors.Is(err, user.ErrInvalidCredentials):
		h.log.WarnCtx(ctx, "登录凭证无效", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeInvalidCredentials, Message: "Invalid credentials"})
	case errors.Is(err, user.ErrInvalidRefreshToken):
		h.log.WarnCtx(ctx, "refresh token 无效", nil)
		c.JSON(http.StatusOK, response.BaseResponse{Code: user.CodeInvalidRefreshToken, Message: "Invalid refresh token"})

	// ─── 404 段 ─────
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
