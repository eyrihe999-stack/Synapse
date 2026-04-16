// service.go agent 模块 service 层共享类型、配置与转换工具。
//
// 包含:
//   - Config:service 层需要的配置项
//   - OrgPort:agent → organization 的跨模块接口(依赖倒置)
//   - model → dto 的转换工具
package service

import (
	"context"
	"encoding/json"

	"github.com/eyrihe999-stack/Synapse/internal/agent"
	"github.com/eyrihe999-stack/Synapse/internal/agent/dto"
	"github.com/eyrihe999-stack/Synapse/internal/agent/model"
	"gorm.io/datatypes"
)

// Config agent 模块 service 层配置(从 yaml 装填)。
type Config struct {
	// HealthCheckIntervalSeconds 健康检查间隔
	HealthCheckIntervalSeconds int
	// HealthFailThreshold 连续失败阈值
	HealthFailThreshold int
	// HealthCheckConcurrency 健康检查并发
	HealthCheckConcurrency int
	// HMACTimestampSkewSeconds 签名时间戳允许偏差
	HMACTimestampSkewSeconds int
	// HMACNonceCacheSeconds nonce 缓存时长
	HMACNonceCacheSeconds int
	// AuditBaseRetentionDays 基础审计保留
	AuditBaseRetentionDays int
	// AuditPayloadRetentionDays payload 保留
	AuditPayloadRetentionDays int
	// UserGlobalRatePerMinute 用户全局限流
	UserGlobalRatePerMinute int
	// OrgGlobalRatePerMinute org 全局限流
	OrgGlobalRatePerMinute int
	// UserAgentRatePerMinute 用户+agent 组合限流
	UserAgentRatePerMinute int
}

// DefaultConfig 返回默认配置,供测试或主流程缺省使用。
func DefaultConfig() Config {
	return Config{
		HealthCheckIntervalSeconds: agent.DefaultHealthCheckIntervalSeconds,
		HealthFailThreshold:        agent.DefaultHealthFailThreshold,
		HealthCheckConcurrency:     agent.DefaultHealthCheckConcurrency,
		HMACTimestampSkewSeconds:   agent.DefaultHMACTimestampSkewSeconds,
		HMACNonceCacheSeconds:      agent.DefaultHMACNonceCacheSeconds,
		AuditBaseRetentionDays:     agent.DefaultAuditBaseRetentionDays,
		AuditPayloadRetentionDays:  agent.DefaultAuditPayloadRetentionDays,
		UserGlobalRatePerMinute:    agent.DefaultUserGlobalRatePerMinute,
		OrgGlobalRatePerMinute:     agent.DefaultOrgGlobalRatePerMinute,
		UserAgentRatePerMinute:     agent.DefaultUserAgentRatePerMinute,
	}
}

// ─── OrgPort:agent → organization 的跨模块接口 ───────────────────────────────
//
// agent 模块的 service 不能直接 import organization/service,而是通过本地接口
// 定义所需能力,由 main.go 注入实现(adapter 模式)。
// 这样可以:
//   - 让 agent 独立编译和单元测试(mock 此接口)
//   - 保持依赖方向单向(agent → organization)
//   - 让 organization 不需要暴露 service 包给 agent

// OrgMembership 是 agent 模块需要的成员快照(子集,比 organization.Membership 简化)。
type OrgMembership struct {
	OrgID       uint64
	UserID      uint64
	RoleName    string
	Permissions map[string]struct{}
}

// Has 判断是否持有某权限点。
func (m *OrgMembership) Has(perm string) bool {
	if m == nil {
		return false
	}
	_, ok := m.Permissions[perm]
	return ok
}

// OrgInfo 是 agent 模块需要的 org 关键字段快照。
type OrgInfo struct {
	ID                 uint64
	Slug               string
	DisplayName        string
	Status             string
	RequireAgentReview bool
	RecordFullPayload  bool
}

// OrgPort agent 模块访问 organization 模块的端口。
//
// 在 main.go 里由 adapter 实现,包装 organization 的 OrgService + RoleService。
type OrgPort interface {
	// GetOrgBySlug 按 slug 查 org(active 才返回)。
	GetOrgBySlug(ctx context.Context, slug string) (*OrgInfo, error)
	// GetOrgByID 按 ID 查 org。
	GetOrgByID(ctx context.Context, orgID uint64) (*OrgInfo, error)
	// GetMembership 返回用户在 org 内的成员/权限快照,非成员返回非 nil error。
	GetMembership(ctx context.Context, orgID, userID uint64) (*OrgMembership, error)
}

// ─── model → dto 转换 ───────────────────────────────────────────────────────

// agentToDTO 把 model.Agent 转为 dto.AgentResponse。
func agentToDTO(a *model.Agent) dto.AgentResponse {
	resp := dto.AgentResponse{
		ID:                 a.ID,
		OwnerUserID:        a.OwnerUserID,
		Slug:               a.Slug,
		DisplayName:        a.DisplayName,
		Description:        a.Description,
		Protocol:           a.Protocol,
		EndpointURL:        a.EndpointURL,
		IconURL:            a.IconURL,
		Tags:               unmarshalTags(a.Tags),
		HomepageURL:        a.HomepageURL,
		PriceTag:           a.PriceTag,
		Developer:          a.DeveloperContact,
		Version:            a.Version,
		TimeoutSeconds:     a.TimeoutSeconds,
		RateLimitPerMinute: a.RateLimitPerMinute,
		MaxConcurrent:      a.MaxConcurrent,
		Status:             a.Status,
		HealthStatus:       a.HealthStatus,
		CreatedAt:          a.CreatedAt.Unix(),
		UpdatedAt:          a.UpdatedAt.Unix(),
	}
	if a.HealthCheckedAt != nil {
		resp.HealthCheckedAt = a.HealthCheckedAt.Unix()
	}
	return resp
}

// methodToDTO 把 model.AgentMethod 转为 dto.MethodResponse。
func methodToDTO(m *model.AgentMethod) dto.MethodResponse {
	return dto.MethodResponse{
		ID:          m.ID,
		AgentID:     m.AgentID,
		MethodName:  m.MethodName,
		DisplayName: m.DisplayName,
		Description: m.Description,
		Transport:   m.Transport,
		Visibility:  m.Visibility,
		CreatedAt:   m.CreatedAt.Unix(),
		UpdatedAt:   m.UpdatedAt.Unix(),
	}
}

// publishToDTO 把 model.AgentPublish 转为 dto.PublishResponse。
func publishToDTO(p *model.AgentPublish) dto.PublishResponse {
	resp := dto.PublishResponse{
		ID:                p.ID,
		AgentID:           p.AgentID,
		OrgID:             p.OrgID,
		SubmittedByUserID: p.SubmittedByUserID,
		Status:            p.Status,
		ReviewNote:        p.ReviewNote,
		RevokedReason:     p.RevokedReason,
		CreatedAt:         p.CreatedAt.Unix(),
		UpdatedAt:         p.UpdatedAt.Unix(),
	}
	if p.ReviewedByUserID != nil {
		resp.ReviewedByUserID = *p.ReviewedByUserID
	}
	if p.ReviewedAt != nil {
		resp.ReviewedAt = p.ReviewedAt.Unix()
	}
	if p.RevokedAt != nil {
		resp.RevokedAt = p.RevokedAt.Unix()
	}
	return resp
}

// invocationToDTO 把 model.AgentInvocation 转为 dto.InvocationResponse。
func invocationToDTO(inv *model.AgentInvocation) dto.InvocationResponse {
	resp := dto.InvocationResponse{
		InvocationID:     inv.InvocationID,
		TraceID:          inv.TraceID,
		OrgID:            inv.OrgID,
		CallerUserID:     inv.CallerUserID,
		CallerRoleName:   inv.CallerRoleName,
		AgentID:          inv.AgentID,
		AgentOwnerUserID: inv.AgentOwnerUserID,
		MethodName:       inv.MethodName,
		Transport:        inv.Transport,
		StartedAt:        inv.StartedAt.UnixMilli(),
		Status:           inv.Status,
		ErrorCode:        inv.ErrorCode,
		ErrorMessage:     inv.ErrorMessage,
		ClientIP:         inv.ClientIP,
	}
	if inv.FinishedAt != nil {
		resp.FinishedAt = inv.FinishedAt.UnixMilli()
	}
	if inv.LatencyMs != nil {
		resp.LatencyMs = *inv.LatencyMs
	}
	if inv.RequestSizeBytes != nil {
		resp.RequestSizeBytes = *inv.RequestSizeBytes
	}
	if inv.ResponseSizeBytes != nil {
		resp.ResponseSizeBytes = *inv.ResponseSizeBytes
	}
	return resp
}

// marshalTags 把 []string tags 序列化为 JSON。
func marshalTags(tags []string) datatypes.JSON {
	if len(tags) == 0 {
		return datatypes.JSON([]byte("[]"))
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return datatypes.JSON([]byte("[]"))
	}
	return datatypes.JSON(b)
}

// unmarshalTags 把 JSON tags 字段反序列化为 []string。
func unmarshalTags(data datatypes.JSON) []string {
	if len(data) == 0 {
		return nil
	}
	var out []string
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return out
}
