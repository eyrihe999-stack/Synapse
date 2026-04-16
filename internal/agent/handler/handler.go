// handler.go agent 模块 HTTP handler 定义。
//
// AgentHandler 是模块唯一的 handler 入口,持有各个 service 接口的引用。
// 资源相关的具体方法分布在同包的其他文件里:
//   - agent_handler.go:Agent CRUD + Method + Secret + Health
//   - publish_handler.go:发布流程
//   - invoke_handler.go:invoke 网关 + 取消接口
//   - audit_handler.go:审计查询
//   - middleware.go:OrgContext 代理中间件(复用 organization 的同名中间件)
package handler

import (
	"net/http"
	"sync/atomic"

	"github.com/eyrihe999-stack/Synapse/internal/agent"
	"github.com/eyrihe999-stack/Synapse/internal/agent/service"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
	"github.com/gin-gonic/gin"
)

// AgentHandler 处理 agent 模块所有 HTTP 请求的控制器。
type AgentHandler struct {
	registrySvc service.RegistryService
	publishSvc  service.PublishService
	gatewaySvc  service.GatewayService
	auditSvc    service.AuditService
	cancels     *service.CancelRegistry
	orgPort     service.OrgPort
	logger      logger.LoggerInterface
	Ready       atomic.Bool //sayso-lint:ignore json-tag-missing
	Failed      atomic.Bool //sayso-lint:ignore json-tag-missing
}

// NewAgentHandler 构造一个 AgentHandler 实例。
func NewAgentHandler(
	registrySvc service.RegistryService,
	publishSvc service.PublishService,
	gatewaySvc service.GatewayService,
	auditSvc service.AuditService,
	cancels *service.CancelRegistry,
	orgPort service.OrgPort,
	log logger.LoggerInterface,
) *AgentHandler {
	return &AgentHandler{
		registrySvc: registrySvc,
		publishSvc:  publishSvc,
		gatewaySvc:  gatewaySvc,
		auditSvc:    auditSvc,
		cancels:     cancels,
		orgPort:     orgPort,
		logger:      log,
	}
}

// checkReady 前置守卫。Failed → 500,未 Ready → 503。
// 返回 false 时已写入响应,调用方应直接 return。
func (h *AgentHandler) checkReady(c *gin.Context) bool {
	if h.Failed.Load() {
		c.JSON(http.StatusInternalServerError, response.BaseResponse{
			Code:    agent.CodeAgentInternal,
			Message: "migration failed, service unavailable",
		})
		return false
	}
	if !h.Ready.Load() {
		c.JSON(http.StatusServiceUnavailable, response.BaseResponse{
			Code:    agent.CodeAgentInternal,
			Message: "service initializing",
		})
		return false
	}
	return true
}
