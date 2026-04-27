package service

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	chanerr "github.com/eyrihe999-stack/Synapse/internal/channel"
	principalmodel "github.com/eyrihe999-stack/Synapse/internal/principal/model"
)

// principalOrgResolver 查 principals + users + agents 三表,回答"principal 是否属于 org"。
//
// 分叉逻辑(§3.5.4):
//   - principals.kind='user':取 users.id → orgChecker.IsMember(orgID, userID)
//   - principals.kind='agent':取 agents.org_id,直接比较
//
// 为什么不放 principal 模块:principal 是底层身份抽象,不应该向上依赖 users /
// agents / organization;这段分叉逻辑天然属于"业务编排"层,放 channel/service
// 里最合适。未来 org_members 改 principal-based 后这里可以简化成单查。
type principalOrgResolver struct {
	db         *gorm.DB
	orgChecker OrgMembershipChecker
}

// NewPrincipalOrgResolver 构造默认实现。db 句柄用于直接查 principals / users /
// agents 三表;orgChecker 用于 user 类型 principal 的二次校验。
func NewPrincipalOrgResolver(db *gorm.DB, orgChecker OrgMembershipChecker) PrincipalOrgResolver {
	return &principalOrgResolver{db: db, orgChecker: orgChecker}
}

// IsPrincipalInOrg 判定 principal 是否属于 org。
//
// 返回值语义:
//   - (true, nil):属于
//   - (false, nil):不属于(含 principal 不存在或类型未知的情况)
//   - (_, err):底层查询错误
//
// 不返"principal 不存在"的哨兵错误 —— channel/service 调用方拿到 false 就能
// 返 ErrPrincipalNotInOrg,上下游无需区分"不存在"和"存在但不在此 org"。
func (r *principalOrgResolver) IsPrincipalInOrg(ctx context.Context, principalID, orgID uint64) (bool, error) {
	var p principalmodel.Principal
	err := r.db.WithContext(ctx).Select("id", "kind").First(&p, principalID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lookup principal %d: %w: %w", principalID, err, chanerr.ErrChannelInternal)
	}

	switch p.Kind {
	case principalmodel.KindUser:
		var userID uint64
		err := r.db.WithContext(ctx).
			Table("users").
			Select("id").
			Where("principal_id = ?", principalID).
			Scan(&userID).Error
		if err != nil {
			return false, fmt.Errorf("lookup user by principal %d: %w: %w", principalID, err, chanerr.ErrChannelInternal)
		}
		if userID == 0 {
			return false, nil
		}
		return r.orgChecker.IsMember(ctx, orgID, userID)

	case principalmodel.KindAgent:
		var agentOrgID uint64
		err := r.db.WithContext(ctx).
			Table("agents").
			Select("org_id").
			Where("principal_id = ?", principalID).
			Scan(&agentOrgID).Error
		if err != nil {
			return false, fmt.Errorf("lookup agent by principal %d: %w: %w", principalID, err, chanerr.ErrChannelInternal)
		}
		return agentOrgID == orgID, nil

	default:
		return false, nil
	}
}
