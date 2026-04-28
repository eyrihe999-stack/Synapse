// Package handler pm 模块 HTTP 端点。
//
// 入口:Handler struct 聚合 Service,每个子 handler 是独立文件。
// 路由注册和中间件装配见 router.go。
package handler

import (
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/pm/service"
)

// Handler pm 模块所有 HTTP endpoint 的持有者。
type Handler struct {
	svc *service.Service
	log logger.LoggerInterface
}

// NewHandler 构造 Handler。svc 是 pm.service.New() 的产物。
func NewHandler(svc *service.Service, log logger.LoggerInterface) *Handler {
	return &Handler{svc: svc, log: log}
}
