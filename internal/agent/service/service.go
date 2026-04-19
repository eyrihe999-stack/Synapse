// service.go agent 模块 service 层共享类型、配置与转换工具。
package service

import (
	"context"
	"encoding/json"

	"github.com/eyrihe999-stack/Synapse/internal/agent"
	"github.com/eyrihe999-stack/Synapse/internal/agent/dto"
	"github.com/eyrihe999-stack/Synapse/internal/agent/model"
	"gorm.io/datatypes"
)

// Config agent 模块 service 层配置,包含默认上下文轮数与聊天限流。
// 注:单次请求超时上下界(Min/MaxTimeoutSeconds)是硬编码常量,见 internal/agent/const.go。
type Config struct {
	DefaultMaxContextRounds int
	ChatRateLimitPerMinute  int
	// AllowPrivateEndpoints 是否允许 agent endpoint 指向 RFC1918 / IPv6 ULA 私网地址。
	// Docker / K8s 同网络内部署 agent 时必须 true;agent 仅公网部署时可设 false 收紧。
	// 注:loopback(127.x/::1)与 link-local(169.254.x,含云元数据)始终拦截,
	// 与此开关无关。详见 internal/agent/endpoint_guard.go。
	AllowPrivateEndpoints bool
}

// DefaultConfig 返回默认配置。
func DefaultConfig() Config {
	return Config{
		DefaultMaxContextRounds: agent.DefaultMaxContextRounds,
		ChatRateLimitPerMinute:  agent.DefaultChatRateLimitPerMinute,
		AllowPrivateEndpoints:   true,
	}
}

// ─── OrgPort:agent → organization 的跨模块接口 ──────────────────────────────

// OrgMembership 是 agent 模块需要的成员快照，包含角色和权限点集合。
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

// OrgInfo 是 agent 模块需要的 org 关键字段快照，包含审核与日志配置。
type OrgInfo struct {
	ID                 uint64
	Slug               string
	DisplayName        string
	Status             string
	RequireAgentReview bool
	RecordFullPayload  bool
}

// UserProfile agent 模块需要的用户公开信息快照。
type UserProfile struct {
	ID          uint64
	DisplayName string
}

// OrgPort agent 模块访问 organization 模块的端口，用于获取 org 信息、成员关系和用户公开信息。
type OrgPort interface {
	GetOrgBySlug(ctx context.Context, slug string) (*OrgInfo, error)
	GetOrgByID(ctx context.Context, orgID uint64) (*OrgInfo, error)
	GetMembership(ctx context.Context, orgID, userID uint64) (*OrgMembership, error)
	GetUserDisplayName(ctx context.Context, userID uint64) string
}

// ─── model → dto 转换 ───────────────────────────────────────────────────────

// agentToDTO 将 model.Agent 转换为 dto.AgentResponse。
func agentToDTO(a *model.Agent) dto.AgentResponse {
	return dto.AgentResponse{
		ID:               a.ID,
		OwnerUserID:      a.OwnerUserID,
		Slug:             a.Slug,
		DisplayName:      a.DisplayName,
		Description:      a.Description,
		AgentType:        a.AgentType,
		EndpointURL:      a.EndpointURL,
		ContextMode:      a.ContextMode,
		MaxContextRounds: a.MaxContextRounds,
		HasAuthToken:     len(a.AuthTokenEncrypted) > 0,
		TimeoutSeconds:   a.TimeoutSeconds,
		IconURL:          a.IconURL,
		Tags:             unmarshalTags(a.Tags),
		DataSources:      unmarshalDataSources(a.DataSources),
		Version:          a.Version,
		Status:           a.Status,
		CreatedAt:        a.CreatedAt.Unix(),
		UpdatedAt:        a.UpdatedAt.Unix(),
	}
}

// publishToDTO 将 model.AgentPublish 转换为 dto.PublishResponse。
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
	if p.Agent != nil {
		resp.AgentSlug = p.Agent.Slug
		resp.AgentDisplayName = p.Agent.DisplayName
		resp.AgentOwnerUID = p.Agent.OwnerUserID
		resp.AgentType = p.Agent.AgentType
		resp.AgentDescription = p.Agent.Description
		resp.AgentIconURL = p.Agent.IconURL
		resp.AgentContextMode = p.Agent.ContextMode
		resp.AgentTags = unmarshalTags(p.Agent.Tags)
		resp.AgentVersion = p.Agent.Version
		resp.AgentUpdatedAt = p.Agent.UpdatedAt.Unix()
	}
	return resp
}

// sessionToDTO 将 model.AgentSession 转换为 dto.SessionResponse。
func sessionToDTO(s *model.AgentSession) dto.SessionResponse {
	return dto.SessionResponse{
		SessionID:   s.SessionID,
		AgentID:     s.AgentID,
		Title:       s.Title,
		ContextMode: s.ContextMode,
		CreatedAt:   s.CreatedAt.Unix(),
		UpdatedAt:   s.UpdatedAt.Unix(),
	}
}

// messageToDTO 将 model.AgentMessage 转换为 dto.MessageResponse。
func messageToDTO(m *model.AgentMessage) dto.MessageResponse {
	return dto.MessageResponse{
		ID:        m.ID,
		Role:      m.Role,
		Content:   m.Content,
		CreatedAt: m.CreatedAt.Unix(),
	}
}

// marshalTags 将字符串切片序列化为 JSON 存储格式；空切片返回 "[]"。
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

// unmarshalTags 将 JSON 存储格式反序列化为字符串切片；数据为空或解析失败返回 nil。
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

// marshalDataSources tags 一样的序列化(复用 JSON 数组存储格式)。
func marshalDataSources(sources []string) datatypes.JSON {
	if len(sources) == 0 {
		return datatypes.JSON([]byte("[]"))
	}
	b, err := json.Marshal(sources)
	if err != nil {
		return datatypes.JSON([]byte("[]"))
	}
	return datatypes.JSON(b)
}

// unmarshalDataSources 反序列化 data_sources JSON。
func unmarshalDataSources(data datatypes.JSON) []string {
	if len(data) == 0 {
		return nil
	}
	var out []string
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return out
}
