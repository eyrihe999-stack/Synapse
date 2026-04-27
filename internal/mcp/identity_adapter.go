package mcp

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// Identity whoami tool 返回的当前 caller 身份。
type Identity struct {
	AgentPrincipalID  uint64 `json:"agent_principal_id"`
	AgentID           string `json:"agent_id"`            // agt_xxx slug
	AgentDisplayName  string `json:"agent_display_name"`
	AgentKind         string `json:"agent_kind"`          // "user" | "system"
	OwnerUserID       uint64 `json:"owner_user_id,omitempty"`
	OwnerDisplayName  string `json:"owner_display_name,omitempty"`
}

// IdentityFacade whoami tool 用的极小查询面 —— 按 agent.principal_id 拿 agent
// 自身 + 关联 user(owner)的可读信息。**跳过 RBAC**:caller 已被 BearerAuth
// 验过身份,这里只是把它已有的 token 翻译成 LLM 可读的"我是谁"。
type IdentityFacade interface {
	LookupByAgentPrincipal(ctx context.Context, agentPrincipalID uint64) (*Identity, error)
}

// IdentityAdapter raw SQL 实现。一次 LEFT JOIN 拿齐 agent + owner user。
// 由 main.go 注入 *gorm.DB(主库句柄)。
type IdentityAdapter struct {
	DB *gorm.DB
}

// LookupByAgentPrincipal 不存在返 (nil, ErrIdentityNotFound)。
func (a *IdentityAdapter) LookupByAgentPrincipal(ctx context.Context, agentPrincipalID uint64) (*Identity, error) {
	if agentPrincipalID == 0 {
		return nil, errors.New("identity: empty principal_id")
	}
	type row struct {
		AgentPrincipalID uint64 `gorm:"column:agent_principal_id"`
		AgentID          string `gorm:"column:agent_id"`
		AgentDisplayName string `gorm:"column:agent_display_name"`
		AgentKind        string `gorm:"column:agent_kind"`
		OwnerUserID      uint64 `gorm:"column:owner_user_id"`
		OwnerDisplayName string `gorm:"column:owner_display_name"`
	}
	var r row
	err := a.DB.WithContext(ctx).Raw(`
		SELECT a.principal_id   AS agent_principal_id,
		       a.agent_id       AS agent_id,
		       a.display_name   AS agent_display_name,
		       a.kind           AS agent_kind,
		       COALESCE(a.owner_user_id, 0) AS owner_user_id,
		       COALESCE(u.display_name, '') AS owner_display_name
		FROM agents a
		LEFT JOIN users u ON u.id = a.owner_user_id
		WHERE a.principal_id = ?
		LIMIT 1
	`, agentPrincipalID).Scan(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("identity: agent principal %d not found", agentPrincipalID)
	}
	if err != nil {
		return nil, fmt.Errorf("identity: lookup: %w", err)
	}
	if r.AgentPrincipalID == 0 {
		return nil, fmt.Errorf("identity: agent principal %d not found", agentPrincipalID)
	}
	return &Identity{
		AgentPrincipalID: r.AgentPrincipalID,
		AgentID:          r.AgentID,
		AgentDisplayName: r.AgentDisplayName,
		AgentKind:        r.AgentKind,
		OwnerUserID:      r.OwnerUserID,
		OwnerDisplayName: r.OwnerDisplayName,
	}, nil
}
