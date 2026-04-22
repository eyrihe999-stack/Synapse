// audit_handler.go 审计查询 HTTP 端点(M6)。
//
// 路由 GET /api/v2/orgs/:slug/audit-log
//
// query params(全部可选):
//   - actor_user_id  uint64
//   - target_type    string  ("group" / "group_member" / "source" / "source_acl" / "org_member" / "role")
//   - target_id      uint64
//   - action         string  (精确,如 "member.role_change")
//   - action_prefix  string  (前缀,如 "member.")
//   - before_id      uint64  (keyset 分页 cursor)
//   - limit          int     (1-100,默认 20)
//
// 视图作用域由 service 决定:有 audit.read_all 看全 org;否则强制 actor=self。
// 不在 router 上挂 RequirePerm —— 让普通成员也能进入,看 self 视图。
package handler

import (
	"net/http"
	"strconv"

	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	orghandler "github.com/eyrihe999-stack/Synapse/internal/organization/handler"
	"github.com/eyrihe999-stack/Synapse/internal/permission/service"
	"github.com/gin-gonic/gin"
)

// AuditHandler 审计查询的 HTTP 入口,独立持有 AuditQueryService。
//
// 与 PermHandler(权限组管理)分开,避免 PermHandler 字段无限膨胀;
// 共享 RegisterRoutes 入口由 router.go 编排。
type AuditHandler struct {
	svc service.AuditQueryService
}

// NewAuditHandler 构造一个 AuditHandler。
func NewAuditHandler(svc service.AuditQueryService) *AuditHandler {
	return &AuditHandler{svc: svc}
}

// ListAuditLog 列审计日志。
// GET /api/v2/orgs/:slug/audit-log
func (h *AuditHandler) ListAuditLog(c *gin.Context) {
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	callerPerms, _ := service.PermissionsFromContext(c.Request.Context(), org.ID)

	filter := service.AuditQueryFilter{
		TargetType:   c.Query("target_type"),
		Action:       c.Query("action"),
		ActionPrefix: c.Query("action_prefix"),
	}
	if raw := c.Query("actor_user_id"); raw != "" {
		if n, err := strconv.ParseUint(raw, 10, 64); err == nil {
			filter.ActorUserID = n
		}
	}
	if raw := c.Query("target_id"); raw != "" {
		if n, err := strconv.ParseUint(raw, 10, 64); err == nil {
			filter.TargetID = n
		}
	}
	if raw := c.Query("before_id"); raw != "" {
		if n, err := strconv.ParseUint(raw, 10, 64); err == nil {
			filter.BeforeID = n
		}
	}
	if raw := c.Query("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			filter.Limit = n
		}
	}

	resp, err := h.svc.ListAuditLog(c.Request.Context(), org.ID, userID, callerPerms, filter)
	if err != nil {
		response.InternalServerError(c, "list audit log failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}
