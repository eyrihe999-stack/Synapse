// handler.go 组织模块 HTTP handler 定义。
//
// OrgHandler 是模块唯一的 handler 入口,持有 4 个 service 接口的引用,
// 各个资源(org/role/member/invitation)的具体方法分布在同包的 *_handler.go 文件里,
// 通过 receiver 绑定到同一个 OrgHandler 结构。
package handler

import (
	"net/http"
	"sync/atomic"

	"github.com/eyrihe999-stack/Synapse/internal/organization"
	"github.com/eyrihe999-stack/Synapse/internal/organization/service"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
	"github.com/gin-gonic/gin"
)

// OrgHandler 处理组织模块所有 HTTP 请求的控制器。
//
// 持有 4 个 service 接口:
//   - orgSvc: 组织本体 CRUD / 设置 / 解散 / 查询
//   - memberSvc: 成员管理
//   - inviteSvc: 邀请全流程
//   - roleSvc: 角色管理 + 权限判断(同时作为 middleware 的权限检查后端)
//
// Ready/Failed 用于 migration 协调:启动时 RunMigrations 成功后设置 Ready=true,
// 失败时 Failed=true,所有业务请求在进入 handler 方法前由 checkReady 前置守卫。
type OrgHandler struct {
	orgSvc    service.OrgService
	memberSvc service.MemberService
	inviteSvc service.InvitationService
	roleSvc   service.RoleService
	logger    logger.LoggerInterface
	Ready     atomic.Bool //sayso-lint:ignore json-tag-missing
	Failed    atomic.Bool //sayso-lint:ignore json-tag-missing
}

// NewOrgHandler 构造一个 OrgHandler 实例。
func NewOrgHandler(
	orgSvc service.OrgService,
	memberSvc service.MemberService,
	inviteSvc service.InvitationService,
	roleSvc service.RoleService,
	log logger.LoggerInterface,
) *OrgHandler {
	return &OrgHandler{
		orgSvc:    orgSvc,
		memberSvc: memberSvc,
		inviteSvc: inviteSvc,
		roleSvc:   roleSvc,
		logger:    log,
	}
}

// checkReady 前置守卫。Failed → 500,未 Ready → 503。
// 返回 false 时已写入响应,调用方应直接 return。
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
