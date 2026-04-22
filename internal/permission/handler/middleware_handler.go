// middleware_handler.go 权限模块对外提供的 gin 中间件。
//
// PermContextMiddleware:在 OrgContextMiddleware 之后用,把当前 user 在该 org
// 加入的所有 group id 一次性查出来,塞进 request ctx,
// 后续 PermissionService.GroupsOfUser 调用直接走 ctx 不打 DB。
//
// 必须在 JWTAuth + OrgContextMiddleware 之后挂(依赖已注入的 user_id 和 org)。
package handler

import (
	"net/http"

	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	orghandler "github.com/eyrihe999-stack/Synapse/internal/organization/handler"
	"github.com/eyrihe999-stack/Synapse/internal/permission"
	"github.com/eyrihe999-stack/Synapse/internal/permission/service"
	"github.com/gin-gonic/gin"
)

// PermContextMiddleware 加载 user 在当前 org 的 group ids + role permissions 注入 ctx。
//
// 一次中间件查两件事:
//  1. groups_of(U, org)  - 给 ACL 判定用(M3)
//  2. permissions_of(U, org) - 给 RequirePerm 用(M4)
//
// 失败行为:
//   - 缺 user / org context(中间件顺序错)→ 500
//   - DB 查失败 → 500
//   - user 不在任何 group → groupIDs 为空,继续
//   - user 不是成员(理论上 OrgContextMiddleware 已经拦截了)→ permissions 为空
func PermContextMiddleware(permSvc service.PermissionService, log logger.LoggerInterface) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := middleware.GetUserID(c)
		if !ok {
			response.InternalServerError(c, "Missing user context (middleware order)", "")
			c.Abort()
			return
		}
		org, ok := orghandler.GetOrg(c)
		if !ok {
			response.InternalServerError(c, "Missing org context (middleware order)", "")
			c.Abort()
			return
		}
		ctx := c.Request.Context()
		// 注:这里直接打 DB(不走 service 的 GroupsOfUser/PermissionsOfUser 的 ctx 命中路径)——
		// 中间件本身就是"塞 ctx 的源",service 调用方后续从 ctx 读。
		groupIDs, err := permSvc.GroupsOfUser(ctx, org.ID, userID)
		if err != nil {
			log.ErrorCtx(ctx, "PermContextMiddleware 查 user groups 失败", err, map[string]any{
				"org_id": org.ID, "user_id": userID,
			})
			response.InternalServerError(c, "Internal server error", "")
			c.Abort()
			return
		}
		perms, err := permSvc.PermissionsOfUser(ctx, org.ID, userID)
		if err != nil {
			log.ErrorCtx(ctx, "PermContextMiddleware 查 user permissions 失败", err, map[string]any{
				"org_id": org.ID, "user_id": userID,
			})
			response.InternalServerError(c, "Internal server error", "")
			c.Abort()
			return
		}
		ctx = service.WithUserGroups(ctx, org.ID, groupIDs)
		ctx = service.WithPermissions(ctx, org.ID, perms)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// RequirePerm 返回一个 gin middleware,要求当前 user 在 org 上下文里拥有指定 perm。
//
// 必须在 PermContextMiddleware 之后挂(从 ctx 读 permissions)。
//
// 失败行为:
//   - 缺 user / org / perm context → 500
//   - perm 不在用户 permissions 列表 → 200 + 业务错误码 CodePermForbidden
func RequirePerm(perm string, log logger.LoggerInterface) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := middleware.GetUserID(c)
		if !ok {
			response.InternalServerError(c, "Missing user context", "")
			c.Abort()
			return
		}
		org, ok := orghandler.GetOrg(c)
		if !ok {
			response.InternalServerError(c, "Missing org context", "")
			c.Abort()
			return
		}
		userPerms, ok := service.PermissionsFromContext(c.Request.Context(), org.ID)
		if !ok {
			// PermContextMiddleware 没跑 → 配置错
			log.ErrorCtx(c.Request.Context(), "RequirePerm 缺 perm context", nil, map[string]any{
				"required_perm": perm, "user_id": userID, "org_id": org.ID,
			})
			response.InternalServerError(c, "permission context not loaded", "")
			c.Abort()
			return
		}
		if !containsPerm(userPerms, perm) {
			log.WarnCtx(c.Request.Context(), "RequirePerm 拒绝", map[string]any{
				"required_perm": perm, "user_id": userID, "org_id": org.ID, "have": userPerms,
			})
			c.JSON(http.StatusOK, response.BaseResponse{
				Code:    permission.CodePermForbidden,
				Message: "Forbidden: missing permission " + perm,
			})
			c.Abort()
			return
		}
		c.Next()
	}
}

func containsPerm(perms []string, want string) bool {
	for _, p := range perms {
		if p == want {
			return true
		}
	}
	return false
}
