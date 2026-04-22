// handler.go user 模块 HTTP handler 定义。
//
// Handler 是模块唯一的 handler 入口,持有 UserService 接口的引用。
// 路由注册在 router.go,错误映射在 error_map.go。
package handler

import (
	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/user/service"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/common/oidcclient"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	"github.com/gin-gonic/gin"
)

// Handler 处理 user 模块所有 HTTP 请求的控制器。
type Handler struct {
	svc           service.UserService
	log           logger.LoggerInterface
	googleOIDC    *oidcclient.GoogleClient // nil = google 登录未启用,相关端点返 ErrOAuthProviderDisabled
	googleFERedir string                   // 前端回调页 origin,callback 完成后 302 到这里拼 exchange
	cookieSecure  bool                     // state cookie Secure 标志,生产 true,dev 可 false
}

// NewHandler 构造一个 Handler 实例。
// googleOIDC 可传 nil → /auth/oauth/google/* 返 ErrOAuthProviderDisabled。
func NewHandler(
	svc service.UserService,
	log logger.LoggerInterface,
	googleOIDC *oidcclient.GoogleClient,
	googleFERedir string,
	cookieSecure bool,
) *Handler {
	return &Handler{
		svc:           svc,
		log:           log,
		googleOIDC:    googleOIDC,
		googleFERedir: googleFERedir,
		cookieSecure:  cookieSecure,
	}
}

// Register 用户注册。POST /api/v1/auth/register
func (h *Handler) Register(c *gin.Context) {
	var req service.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request", err.Error())
		return
	}

	req.LoginIP = c.ClientIP()
	req.UserAgent = c.Request.UserAgent()
	if req.DeviceID == "" {
		req.DeviceID = "default"
	}

	resp, err := h.svc.Register(c.Request.Context(), req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}

	response.Created(c, "User registered successfully", resp)
}

// Login 用户登录。POST /api/v1/auth/login
func (h *Handler) Login(c *gin.Context) {
	var req service.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request", err.Error())
		return
	}

	req.LoginIP = c.ClientIP()
	req.UserAgent = c.Request.UserAgent()
	if req.DeviceID == "" {
		req.DeviceID = "default"
	}

	resp, err := h.svc.Login(c.Request.Context(), req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}

	response.Success(c, "Login successful", resp)
}

// SendEmailCode 发送邮箱验证码。POST /api/v1/auth/email/send-code
func (h *Handler) SendEmailCode(c *gin.Context) {
	var req service.SendEmailCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request", err.Error())
		return
	}
	req.LoginIP = c.ClientIP()

	resp, err := h.svc.SendEmailCode(c.Request.Context(), req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}

	response.Success(c, "Verification code sent", resp)
}

// RequestPasswordReset 发起密码重置流程(发邮件)。POST /api/v1/auth/password-reset/request
//
// 无论邮箱是否存在都返成功消息 —— 防账户枚举。service 层已按此语义实现。
func (h *Handler) RequestPasswordReset(c *gin.Context) {
	var req service.RequestPasswordResetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request", err.Error())
		return
	}

	if err := h.svc.RequestPasswordReset(c.Request.Context(), req); err != nil {
		h.handleServiceError(c, err)
		return
	}

	response.Success(c, "If the email exists, a reset link has been sent", nil)
}

// ConfirmPasswordReset 凭 token + 新密码完成重置。POST /api/v1/auth/password-reset/confirm
//
// 成功后 service 层会 LogoutAll,客户端需要重新登录。
func (h *Handler) ConfirmPasswordReset(c *gin.Context) {
	var req service.ConfirmPasswordResetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request", err.Error())
		return
	}

	if err := h.svc.ConfirmPasswordReset(c.Request.Context(), req); err != nil {
		h.handleServiceError(c, err)
		return
	}

	response.Success(c, "Password updated, please login again", nil)
}

// RefreshToken 刷新认证凭证。POST /api/v1/auth/refresh
func (h *Handler) RefreshToken(c *gin.Context) {
	var req service.RefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request", err.Error())
		return
	}

	req.LoginIP = c.ClientIP()

	resp, err := h.svc.RefreshToken(c.Request.Context(), req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}

	response.Success(c, "Token refreshed", resp)
}

// GetProfile 获取当前用户资料。GET /api/v1/users/me
func (h *Handler) GetProfile(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Authentication required", "")
		return
	}

	profile, err := h.svc.GetProfile(c.Request.Context(), userID)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}

	response.Success(c, "ok", profile)
}

// UpdateProfile 更新当前用户资料。PATCH /api/v1/users/me
func (h *Handler) UpdateProfile(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Authentication required", "")
		return
	}

	var req service.UpdateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request", err.Error())
		return
	}

	profile, err := h.svc.UpdateProfile(c.Request.Context(), userID, req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}

	response.Success(c, "Profile updated", profile)
}

// ListSessions 查看当前用户的活跃设备列表。GET /api/v1/users/me/sessions
func (h *Handler) ListSessions(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Authentication required", "")
		return
	}

	sessions, err := h.svc.ListSessions(c.Request.Context(), userID)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}

	response.Success(c, "ok", sessions)
}

// KickSession 踢掉指定设备。DELETE /api/v1/users/me/sessions/:device_id
func (h *Handler) KickSession(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Authentication required", "")
		return
	}

	deviceID := c.Param("device_id")
	if deviceID == "" {
		response.BadRequest(c, "Missing device_id", "")
		return
	}

	if err := h.svc.KickSession(c.Request.Context(), userID, deviceID); err != nil {
		h.handleServiceError(c, err)
		return
	}

	response.Success(c, "Session kicked", nil)
}

// DeleteAccount 自助注销当前账号。DELETE /api/v1/users/me
//
// Body 可选:{"password": "xxx", "reason": "..."}。
// 本地账号必须带 password 做二次确认;OAuth-only 账号(无本地密码)省略即可。
// 成功后 service 层会清掉全部 session,当前 access token 也会立即失效,
// 客户端应直接跳回登录/落地页,不再续期。
func (h *Handler) DeleteAccount(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Authentication required", "")
		return
	}

	var req service.DeleteAccountRequest
	// 允许空 body(OAuth-only 用户无密码)。ShouldBindJSON 在 body 为空时会报 EOF,
	// 此处容忍:只有在 body 非空但格式错误时才返 400。
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			response.BadRequest(c, "Invalid request", err.Error())
			return
		}
	}

	if err := h.svc.DeleteAccount(c.Request.Context(), userID, req); err != nil {
		h.handleServiceError(c, err)
		return
	}

	response.Success(c, "Account deleted", nil)
}

// VerifyEmail M1.1 邮箱激活。POST /api/v1/auth/email/verify
//
// 公开接口(无 JWT),body 只需 token。成功后 status=active + email_verified_at 写入。
// 同一个 token 一次性消费,TTL(默认 24h)兜底。
func (h *Handler) VerifyEmail(c *gin.Context) {
	var req struct {
		Token string `json:"token" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request", err.Error())
		return
	}
	if err := h.svc.VerifyEmail(c.Request.Context(), req.Token); err != nil {
		h.handleServiceError(c, err)
		return
	}
	response.Success(c, "Email verified", nil)
}

// ChangePassword 已登录用户改密。POST /api/v1/users/me/password
//
// Body: { old_password?, new_password }。本地账号必须带 old_password;
// 成功后 service 层会 LogoutAll,当前 token 立即失效,前端应重定向登录页。
func (h *Handler) ChangePassword(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Authentication required", "")
		return
	}
	var req service.ChangePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request", err.Error())
		return
	}
	if err := h.svc.ChangePassword(c.Request.Context(), userID, req); err != nil {
		h.handleServiceError(c, err)
		return
	}
	response.Success(c, "Password changed, please login again", nil)
}

// ChangeEmail 已登录用户改邮箱。POST /api/v1/users/me/email
//
// Body: { new_email, password?, code }。用户需先调 /auth/email/send-code 发给 new_email 一个码;
// 本接口消费该码完成切换。成功后 service 层 LogoutAll,客户端需用新 email 重登。
func (h *Handler) ChangeEmail(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Authentication required", "")
		return
	}
	var req service.ChangeEmailRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request", err.Error())
		return
	}
	if err := h.svc.ChangeEmail(c.Request.Context(), userID, req); err != nil {
		h.handleServiceError(c, err)
		return
	}
	response.Success(c, "Email changed, please login again", nil)
}

// ResendEmailVerification M1.1 重发激活邮件。POST /api/v1/users/me/email/resend-verification
//
// 需 JWT + session,给"OAuth 新建 + email_verified=false"的用户手动触发补发的口子。
// 共用邮箱单日配额(synapse:email_rl),不会变成新的洪水入口。
func (h *Handler) ResendEmailVerification(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Authentication required", "")
		return
	}
	if err := h.svc.ResendEmailVerification(c.Request.Context(), userID); err != nil {
		h.handleServiceError(c, err)
		return
	}
	response.Success(c, "Verification email sent", nil)
}

// LogoutAll 退出所有设备。POST /api/v1/users/me/sessions/logout-all
func (h *Handler) LogoutAll(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Authentication required", "")
		return
	}

	if err := h.svc.LogoutAll(c.Request.Context(), userID); err != nil {
		h.handleServiceError(c, err)
		return
	}

	response.Success(c, "All sessions logged out", nil)
}

