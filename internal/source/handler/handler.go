// handler.go source 模块 HTTP handler 定义。
package handler

import (
	"net/http"
	"sync/atomic"

	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	"github.com/eyrihe999-stack/Synapse/internal/source"
	"github.com/eyrihe999-stack/Synapse/internal/source/service"
	"github.com/gin-gonic/gin"
)

// SourceHandler 处理 source 模块所有 HTTP 请求的控制器。
type SourceHandler struct {
	svc    service.SourceService
	logger logger.LoggerInterface
	Ready  atomic.Bool //sayso-lint:ignore json-tag-missing
	Failed atomic.Bool //sayso-lint:ignore json-tag-missing
}

// NewSourceHandler 构造 SourceHandler。
func NewSourceHandler(svc service.SourceService, log logger.LoggerInterface) *SourceHandler {
	return &SourceHandler{svc: svc, logger: log}
}

// checkReady 前置守卫。Failed → 500,未 Ready → 503。
func (h *SourceHandler) checkReady(c *gin.Context) bool {
	if h.Failed.Load() {
		c.JSON(http.StatusInternalServerError, response.BaseResponse{
			Code:    source.CodeSourceInternal,
			Message: "migration failed, service unavailable",
		})
		return false
	}
	if !h.Ready.Load() {
		c.JSON(http.StatusServiceUnavailable, response.BaseResponse{
			Code:    source.CodeSourceInternal,
			Message: "service initializing",
		})
		return false
	}
	return true
}
