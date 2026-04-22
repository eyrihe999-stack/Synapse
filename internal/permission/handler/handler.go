// handler.go 权限模块 HTTP handler 定义。
package handler

import (
	"net/http"
	"sync/atomic"

	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	"github.com/eyrihe999-stack/Synapse/internal/permission"
	"github.com/eyrihe999-stack/Synapse/internal/permission/service"
	"github.com/gin-gonic/gin"
)

// PermHandler 处理权限模块所有 HTTP 请求的控制器。
//
// Ready/Failed 用于 migration 协调:启动时 RunMigrations 成功后设置 Ready=true,
// 失败时 Failed=true,所有业务请求在进入 handler 方法前由 checkReady 前置守卫。
type PermHandler struct {
	groupSvc service.GroupService
	logger   logger.LoggerInterface
	Ready    atomic.Bool //sayso-lint:ignore json-tag-missing
	Failed   atomic.Bool //sayso-lint:ignore json-tag-missing
}

// NewPermHandler 构造一个 PermHandler 实例。
func NewPermHandler(groupSvc service.GroupService, log logger.LoggerInterface) *PermHandler {
	return &PermHandler{groupSvc: groupSvc, logger: log}
}

// checkReady 前置守卫。Failed → 500,未 Ready → 503。
func (h *PermHandler) checkReady(c *gin.Context) bool {
	if h.Failed.Load() {
		c.JSON(http.StatusInternalServerError, response.BaseResponse{
			Code:    permission.CodePermInternal,
			Message: "migration failed, service unavailable",
		})
		return false
	}
	if !h.Ready.Load() {
		c.JSON(http.StatusServiceUnavailable, response.BaseResponse{
			Code:    permission.CodePermInternal,
			Message: "service initializing",
		})
		return false
	}
	return true
}
