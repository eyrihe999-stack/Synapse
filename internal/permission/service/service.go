// service.go 权限模块 service 层共享类型与转换工具。
package service

import (
	"context"

	"github.com/eyrihe999-stack/Synapse/internal/permission/dto"
	"github.com/eyrihe999-stack/Synapse/internal/permission/model"
)

// OrgMembershipChecker 跨模块前置 guard:把 user 加入权限组前要确认该 user 已是该 org 的成员。
//
// main.go 用 organization service 的 IsMember 作为实现注入;
// nil 时 service 跳过 org 成员校验(单测/降级兜底)。
type OrgMembershipChecker interface {
	IsMember(ctx context.Context, orgID, userID uint64) (bool, error)
}

// OrgMembershipCheckerFunc 把一个 func 适配为 OrgMembershipChecker 接口。
type OrgMembershipCheckerFunc func(ctx context.Context, orgID, userID uint64) (bool, error)

// IsMember 让 OrgMembershipCheckerFunc 满足接口。
func (f OrgMembershipCheckerFunc) IsMember(ctx context.Context, orgID, userID uint64) (bool, error) {
	return f(ctx, orgID, userID)
}

// ─── model → dto 转换 ────────────────────────────────────────────────────────

// groupToDTO 把 Group 模型转为 GroupResponse。memberCount 由调用方查出后传入。
func groupToDTO(g *model.Group, memberCount int64) dto.GroupResponse {
	return dto.GroupResponse{
		ID:          g.ID,
		OrgID:       g.OrgID,
		Name:        g.Name,
		OwnerUserID: g.OwnerUserID,
		MemberCount: memberCount,
		CreatedAt:   g.CreatedAt.Unix(),
		UpdatedAt:   g.UpdatedAt.Unix(),
	}
}

// memberToDTO 把 GroupMember 模型转为 GroupMemberResponse。
func memberToDTO(m *model.GroupMember) dto.GroupMemberResponse {
	return dto.GroupMemberResponse{
		UserID:   m.UserID,
		JoinedAt: m.JoinedAt.Unix(),
	}
}
