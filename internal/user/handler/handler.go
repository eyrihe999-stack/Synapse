// handler.go user 模块 HTTP handler 定义。
//
// Handler 是模块唯一的 handler 入口,持有 UserService 接口的引用。
// 路由注册在 router.go,错误映射在 error_map.go。
package handler

import (
	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/user/service"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
	"github.com/gin-gonic/gin"
)

// Handler 处理 user 模块所有 HTTP 请求的控制器。
type Handler struct {
	svc service.UserService
	log logger.LoggerInterface
}

// NewHandler 构造一个 Handler 实例。
func NewHandler(svc service.UserService, log logger.LoggerInterface) *Handler {
	return &Handler{svc: svc, log: log}
}

// Register 用户注册。POST /api/v1/auth/register
func (h *Handler) Register(c *gin.Context) {
	var req service.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request", err.Error())
		return
	}

	req.LoginIP = c.ClientIP()
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
