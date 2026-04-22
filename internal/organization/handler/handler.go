// handler.go 组织模块 HTTP handler 定义。
package handler

import (
	"net/http"
	"sync/atomic"

	"github.com/eyrihe999-stack/Synapse/internal/organization"
	"github.com/eyrihe999-stack/Synapse/internal/organization/service"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	"github.com/gin-gonic/gin"
)

// OrgHandler 处理组织模块所有 HTTP 请求的控制器。
//
// Ready/Failed 用于 migration 协调:启动时 RunMigrations 成功后设置 Ready=true,
// 失败时 Failed=true,所有业务请求在进入 handler 方法前由 checkReady 前置守卫。
type OrgHandler struct {
	orgSvc        service.OrgService
	memberSvc     service.MemberService
	roleSvc       service.RoleService
	invitationSvc service.InvitationService
	logger        logger.LoggerInterface
	Ready         atomic.Bool //sayso-lint:ignore json-tag-missing
	Failed        atomic.Bool //sayso-lint:ignore json-tag-missing
}

// NewOrgHandler 构造一个 OrgHandler 实例。
func NewOrgHandler(
	orgSvc service.OrgService,
	memberSvc service.MemberService,
	roleSvc service.RoleService,
	invitationSvc service.InvitationService,
	log logger.LoggerInterface,
) *OrgHandler {
	return &OrgHandler{
		orgSvc:        orgSvc,
		memberSvc:     memberSvc,
		roleSvc:       roleSvc,
		invitationSvc: invitationSvc,
		logger:        log,
	}
}

// checkReady 前置守卫。Failed → 500,未 Ready → 503。
func (h *OrgHandler) checkReady(c *gin.Context) bool {
	if h.Failed.Load() {
		c.JSON(http.StatusInternalServerError, response.BaseResponse{
			Code:    organization.CodeOrgInternal,
			Message: "migration failed, service unavailable",
		})
		return false
	}
	if !h.Ready.Load() {
		c.JSON(http.StatusServiceUnavailable, response.BaseResponse{
			Code:    organization.CodeOrgInternal,
			Message: "service initializing",
		})
		return false
	}
	return true
}
