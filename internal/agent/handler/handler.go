// handler.go agent 模块 HTTP handler 定义。
package handler

import (
	"net/http"
	"strconv"

	"github.com/eyrihe999-stack/Synapse/internal/agent"
	"github.com/eyrihe999-stack/Synapse/internal/agent/service"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
	"github.com/gin-gonic/gin"
)

// AgentHandler 处理 agent 模块所有 HTTP 请求。
type AgentHandler struct {
	registrySvc service.RegistryService
	publishSvc  service.PublishService
	chatSvc     service.ChatService
	orgPort     service.OrgPort
	logger      logger.LoggerInterface
}

// NewAgentHandler 构造 AgentHandler。
func NewAgentHandler(
	registrySvc service.RegistryService,
	publishSvc service.PublishService,
	chatSvc service.ChatService,
	orgPort service.OrgPort,
	log logger.LoggerInterface,
) *AgentHandler {
	return &AgentHandler{
		registrySvc: registrySvc,
		publishSvc:  publishSvc,
		chatSvc:     chatSvc,
		orgPort:     orgPort,
		logger:      log,
	}
}

// parseUintParam 从 gin path 参数解析 uint64。
func parseUintParam(c *gin.Context, key string) (uint64, bool) {
	raw := c.Param(key)
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || v == 0 {
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentInvalidRequest, Message: "Invalid " + key})
		return 0, false
	}
	return v, true
}
