// Package handler document 模块 HTTP 接口。
package handler

import (
	"net/http"
	"strconv"

	"github.com/eyrihe999-stack/Synapse/internal/document"
	"github.com/eyrihe999-stack/Synapse/internal/document/service"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
	"github.com/gin-gonic/gin"
)

// DocumentHandler 封装 document 模块所有 HTTP handler。
type DocumentHandler struct {
	svc     service.DocumentService
	orgPort service.OrgPort
	logger  logger.LoggerInterface
}

// NewDocumentHandler 构造 DocumentHandler。
func NewDocumentHandler(
	svc service.DocumentService,
	orgPort service.OrgPort,
	log logger.LoggerInterface,
) *DocumentHandler {
	return &DocumentHandler{
		svc:     svc,
		orgPort: orgPort,
		logger:  log,
	}
}

// parseUintParam 解析 gin path 参数为 uint64,失败自动返回 400。
func parseUintParam(c *gin.Context, key string) (uint64, bool) {
	raw := c.Param(key)
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || v == 0 {
		c.JSON(http.StatusOK, response.BaseResponse{Code: document.CodeDocumentInvalidRequest, Message: "Invalid " + key})
		return 0, false
	}
	return v, true
}
