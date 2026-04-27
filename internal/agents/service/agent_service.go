// agent_service.go HTTP CRUD + rotate-key 业务。
//
// 权限模型(V1):
//   - read (Get/List)                    :所有 org 成员可
//   - Create                              :仅 owner / admin(见下方说明)
//   - Update / Delete / RotateKey         :owner / admin / 该 agent 创建者
//
// 为什么 Create 严格:当前 agent 全部是系统 agent(apikey 身份,长期有效凭据),
// 等同于"给自己开一条不受 session 管控的后门",必须限制在 org 管理层级。
// 未来 kind=user 的 agent(代表某 user 短期身份)可按 member 放开 —— 到时按 kind 分流。
//
// TODO: 接 permissions 系统时,把硬编码 slug 校验换成 HasOrgPermission("agents.manage.system")
package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/eyrihe999-stack/Synapse/internal/agents"
	"github.com/eyrihe999-stack/Synapse/internal/agents/model"
	"github.com/eyrihe999-stack/Synapse/internal/organization"
)

// CreateInput HTTP Create agent 的入参。
type CreateInput struct {
	OrgID       uint64 // 由 handler 从 JWT 上下文填,不要信任前端传
	CallerUID   uint64 // 创建者 user id
	DisplayName string
}

// CreateOutput 创建成功时返给 handler —— 含一次性明文 apikey。
type CreateOutput struct {
	Agent  *model.Agent
	APIKey string // 明文,handler 返给前端,只此一次
}

// Create 新建 agent 记录 + 生成 agent_id + apikey。
//
// 权限:**仅 org owner / admin** 可创建(V1 所有 agent 都是 system kind,
// 属于基础设施级资源,不允许普通成员随意开长期凭据)。
//
// kind 硬写 system,未来放开用户级 agent 时再按 kind 分流。
//
// 可能返回:
//   - ErrAgentDisplayNameInvalid:display_name 空或超长
//   - ErrAgentForbidden         :非 owner/admin
//   - ErrAgentInternal          :DB / 密钥生成失败
func (s *AgentService) Create(ctx context.Context, in CreateInput) (*CreateOutput, error) {
	name := strings.TrimSpace(in.DisplayName)
	if name == "" || len(name) > 128 {
		return nil, fmt.Errorf("display_name invalid: %w", agents.ErrAgentDisplayNameInvalid)
	}
	slug, err := s.roleLookup.GetMemberRoleSlug(ctx, in.OrgID, in.CallerUID)
	if err != nil {
		s.log.ErrorCtx(ctx, "agents: role lookup failed", err, map[string]any{
			"org_id": in.OrgID, "caller_uid": in.CallerUID,
		})
		return nil, fmt.Errorf("role lookup: %w: %w", err, agents.ErrAgentInternal)
	}
	// 只认 owner / admin;普通成员 + 非成员一律 403。
	if slug != organization.SystemRoleSlugOwner && slug != organization.SystemRoleSlugAdmin {
		return nil, agents.ErrAgentForbidden
	}

	agentID, err := genAgentID()
	if err != nil {
		return nil, fmt.Errorf("gen agent_id: %w: %w", err, agents.ErrAgentInternal)
	}
	apikey, err := genAPIKey()
	if err != nil {
		return nil, fmt.Errorf("gen apikey: %w: %w", err, agents.ErrAgentInternal)
	}

	now := nowFunc()
	a := &model.Agent{
		AgentID:      agentID,
		OrgID:        in.OrgID,
		Kind:         agents.KindSystem, // V1 硬写;未来开放用户级时按入参 / 上下文决定
		APIKey:       apikey,
		DisplayName:  name,
		Enabled:      true,
		CreatedByUID: in.CallerUID,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.repo.Create(ctx, a); err != nil {
		return nil, err // 已是 sentinel wrap
	}
	s.log.InfoCtx(ctx, "agents: created", map[string]any{
		"agent_id": a.AgentID, "org_id": a.OrgID, "kind": a.Kind, "created_by": a.CreatedByUID,
	})
	return &CreateOutput{Agent: a, APIKey: apikey}, nil
}

// Get 按逻辑 agent_id 查。校验 caller 是 org 成员即可(read)。
func (s *AgentService) Get(ctx context.Context, callerUID, callerOrgID uint64, agentID string) (*model.Agent, error) {
	a, err := s.repo.FindByAgentID(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if a.OrgID != callerOrgID {
		// 跨 org 查 → 不泄漏存在性,一律 not found
		return nil, agents.ErrAgentNotFound
	}
	slug, err := s.roleLookup.GetMemberRoleSlug(ctx, a.OrgID, callerUID)
	if err != nil {
		return nil, fmt.Errorf("role lookup: %w: %w", err, agents.ErrAgentInternal)
	}
	if slug == "" {
		return nil, agents.ErrAgentForbidden
	}
	return a, nil
}

// List 列 caller 所在 org 的 agent。必须是 org 成员。
func (s *AgentService) List(ctx context.Context, callerUID, callerOrgID uint64, offset, limit int) ([]*model.Agent, int64, error) {
	slug, err := s.roleLookup.GetMemberRoleSlug(ctx, callerOrgID, callerUID)
	if err != nil {
		return nil, 0, fmt.Errorf("role lookup: %w: %w", err, agents.ErrAgentInternal)
	}
	if slug == "" {
		return nil, 0, agents.ErrAgentForbidden
	}
	if limit <= 0 {
		limit = agents.ListDefaultLimit
	}
	if limit > agents.ListMaxLimit {
		limit = agents.ListMaxLimit
	}
	if offset < 0 {
		offset = 0
	}
	return s.repo.ListByOrg(ctx, callerOrgID, offset, limit)
}

// UpdateInput PATCH agent 的入参。nil 指针表示"不改此字段"。
type UpdateInput struct {
	DisplayName *string
	Enabled     *bool
}

// Update 改 display_name / enabled。enabled 从 true → false 时额外踢当前连接。
// 权限:owner/admin/创建者。
func (s *AgentService) Update(ctx context.Context, callerUID, callerOrgID uint64, agentID string, in UpdateInput) (*model.Agent, error) {
	a, err := s.getForWrite(ctx, callerUID, callerOrgID, agentID)
	if err != nil {
		return nil, err
	}

	var displayName *string
	if in.DisplayName != nil {
		name := strings.TrimSpace(*in.DisplayName)
		if name == "" || len(name) > 128 {
			return nil, fmt.Errorf("display_name invalid: %w", agents.ErrAgentDisplayNameInvalid)
		}
		displayName = &name
	}

	wasEnabled := a.Enabled
	if err := s.repo.Update(ctx, a.ID, displayName, in.Enabled); err != nil {
		return nil, err
	}

	// 禁用 → 踢连接
	if in.Enabled != nil && wasEnabled && !*in.Enabled {
		if kicked := s.disconnector.Disconnect(a.AgentID, "agent disabled"); kicked {
			s.log.InfoCtx(ctx, "agents: kicked after disable", map[string]any{"agent_id": a.AgentID})
		}
	}

	// 重新读一遍返新状态
	fresh, err := s.repo.FindByAgentID(ctx, agentID)
	if err != nil {
		return nil, err
	}
	return fresh, nil
}

// Delete 硬删 + 踢连接。权限:owner/admin/创建者。
func (s *AgentService) Delete(ctx context.Context, callerUID, callerOrgID uint64, agentID string) error {
	a, err := s.getForWrite(ctx, callerUID, callerOrgID, agentID)
	if err != nil {
		return err
	}
	if err := s.repo.Delete(ctx, a.ID); err != nil {
		return err
	}
	_ = s.disconnector.Disconnect(a.AgentID, "agent deleted")
	s.log.InfoCtx(ctx, "agents: deleted", map[string]any{
		"agent_id": a.AgentID, "caller_uid": callerUID,
	})
	return nil
}

// RotateKey 生成新 apikey,旧 key 立即失效,当前连接被踢。
// 权限:owner/admin/创建者。
func (s *AgentService) RotateKey(ctx context.Context, callerUID, callerOrgID uint64, agentID string) (*model.Agent, string, error) {
	a, err := s.getForWrite(ctx, callerUID, callerOrgID, agentID)
	if err != nil {
		return nil, "", err
	}
	newKey, err := genAPIKey()
	if err != nil {
		return nil, "", fmt.Errorf("gen apikey: %w: %w", err, agents.ErrAgentInternal)
	}
	if err := s.repo.UpdateAPIKey(ctx, a.ID, newKey); err != nil {
		return nil, "", err
	}
	_ = s.disconnector.Disconnect(a.AgentID, "apikey rotated")
	s.log.InfoCtx(ctx, "agents: key rotated", map[string]any{
		"agent_id": a.AgentID, "caller_uid": callerUID,
	})
	fresh, err := s.repo.FindByAgentID(ctx, agentID)
	if err != nil {
		return nil, "", err
	}
	return fresh, newKey, nil
}

// ─── 内部 ─────────────────────────────────────────────────────────────────────

// getForWrite 取 agent 并校验 caller 有 write 权限(owner/admin/创建者)。
// 跨 org 访问一律 not found(不泄漏存在性)。
func (s *AgentService) getForWrite(ctx context.Context, callerUID, callerOrgID uint64, agentID string) (*model.Agent, error) {
	a, err := s.repo.FindByAgentID(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if a.OrgID != callerOrgID {
		return nil, agents.ErrAgentNotFound
	}
	// 创建者直通
	if a.CreatedByUID == callerUID {
		return a, nil
	}
	// 否则要 owner/admin
	slug, err := s.roleLookup.GetMemberRoleSlug(ctx, a.OrgID, callerUID)
	if err != nil {
		return nil, fmt.Errorf("role lookup: %w: %w", err, agents.ErrAgentInternal)
	}
	if slug != organization.SystemRoleSlugOwner && slug != organization.SystemRoleSlugAdmin {
		return nil, agents.ErrAgentForbidden
	}
	return a, nil
}

// BootstrapUserAgent OAuth 授权成功时自动建"个人 agent"。
//
// 与 Create 的差异:
//   - Create 要求 caller ∈ (owner/admin) 且 kind 硬写 system;
//     适合管理员建系统服务 agent
//   - BootstrapUserAgent 跳过 RBAC 检查(调用方 = OAuth consent 流程,
//     用户已通过密码 + consent 显式授权,再叠 RBAC 无意义);
//     kind 硬写 user,owner_user_id 填 ownerUserID
//
// orgID 由调用方提供(main.go 里的 AgentBootstrapper 实现从 user 的第一个 org
// FindUserAgentByDisplayName 按 (orgID, ownerUserID, display_name, kind=user) 查 agent。
// 给 OAuth bootstrapper 做"同 user + 同 client 复用"的查重使用。
// 不存在返 (nil, ErrAgentNotFound);其他错误返内部错误。
func (s *AgentService) FindUserAgentByDisplayName(ctx context.Context, orgID, ownerUserID uint64, displayName string) (*model.Agent, error) {
	return s.repo.FindByOwnerAndDisplayName(ctx, orgID, ownerUserID, displayName)
}

// 取)。agent_id 自动生成 agt_<base64url>。APIKey 也生成(NOT NULL 约束要求),
// 但 OAuth 场景下不会被握手使用 —— 客户端走 Bearer access_token。
//
// 返回 (agent.ID, principal.id, err)。principal 由 Agent.BeforeCreate hook 自动建。
func (s *AgentService) BootstrapUserAgent(ctx context.Context, orgID, ownerUserID uint64, displayName string) (uint64, uint64, error) {
	name := strings.TrimSpace(displayName)
	if name == "" || len(name) > 128 {
		return 0, 0, fmt.Errorf("display_name invalid: %w", agents.ErrAgentDisplayNameInvalid)
	}
	if orgID == 0 || ownerUserID == 0 {
		return 0, 0, fmt.Errorf("org_id and owner_user_id required: %w", agents.ErrAgentInternal)
	}

	agentID, err := genAgentID()
	if err != nil {
		return 0, 0, fmt.Errorf("gen agent_id: %w: %w", err, agents.ErrAgentInternal)
	}
	apikey, err := genAPIKey()
	if err != nil {
		return 0, 0, fmt.Errorf("gen apikey: %w: %w", err, agents.ErrAgentInternal)
	}

	now := nowFunc()
	owner := ownerUserID
	a := &model.Agent{
		AgentID:      agentID,
		OrgID:        orgID,
		Kind:         agents.KindUser,
		OwnerUserID:  &owner,
		APIKey:       apikey,
		DisplayName:  name,
		Enabled:      true,
		CreatedByUID: ownerUserID,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.repo.Create(ctx, a); err != nil {
		return 0, 0, err // 已是 sentinel wrap
	}
	s.log.InfoCtx(ctx, "agents: bootstrapped user agent for OAuth", map[string]any{
		"agent_id": a.AgentID, "org_id": a.OrgID, "owner_user_id": ownerUserID,
	})
	return a.ID, a.PrincipalID, nil
}

// ─── 密钥 / ID 生成 ──────────────────────────────────────────────────────────

// genAgentID 生成 agt_<base64url>;长度约 26 字符。
func genAgentID() (string, error) {
	buf := make([]byte, agents.AgentIDRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return agents.AgentIDPrefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// genAPIKey 生成 sk_<base64url>;长度约 46 字符。
func genAPIKey() (string, error) {
	buf := make([]byte, agents.APIKeyRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return agents.APIKeyPrefix + base64.RawURLEncoding.EncodeToString(buf), nil
}
