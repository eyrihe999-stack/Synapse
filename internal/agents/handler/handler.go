// Package handler agents 模块 HTTP 入口 —— agent 档案 CRUD + rotate-key。
//
// 路径风格对齐 permission 模块:/api/v2/orgs/:slug/agents/*。
// 上层中间件链:JWTAuthWithSession → OrgContextMiddleware(注入 org + 校验成员)。
// 组内 owner/admin/创建者的精细校验在 service 层。
package handler

import (
	"github.com/eyrihe999-stack/Synapse/internal/agents/service"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
)

// Handler HTTP 入口。
type Handler struct {
	svc *service.AgentService
	log logger.LoggerInterface
}

// New 构造。svc 和 log 都不可为 nil(main.go 保证)。
func New(svc *service.AgentService, log logger.LoggerInterface) *Handler {
	return &Handler{svc: svc, log: log}
}
