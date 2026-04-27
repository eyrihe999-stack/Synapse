// Package handler task 模块 HTTP 端点。
//
// 权限在 service 层做;handler 只管参数解析 + 错误翻译 + DTO 转响应。
package handler

import (
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/task/service"
)

// Handler task 模块 HTTP endpoint 持有者。
type Handler struct {
	svc *service.Service
	log logger.LoggerInterface
}

// NewHandler 构造 Handler。svc 是 task.service.New() 的产物。
func NewHandler(svc *service.Service, log logger.LoggerInterface) *Handler {
	return &Handler{svc: svc, log: log}
}
