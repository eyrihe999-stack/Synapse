// Package catalog 是 orchestration layer 的"能力发现"子模块。
//
// 职责:对调用方(Claude.ai / 其它 agent)暴露"当前 org 有哪些可用 agent、它们的工具是什么",
// 作为 retrieval.Retriever 接入现有 MCP 网关 —— 自动获得 search_agent / fetch_agent 两个工具。
//
// 为什么不放 internal/retrieval/:
//   - retrieval 是"知识检索层"(code / document / image),搜的是内容
//   - catalog 是"编排层"(agent / 未来的 dispatch / plan),搜的是 peer
//   - 混在一起会让 retrieval 包随编排功能膨胀,拆分困难
//
// 数据源:
//   - agents + agent_publishes 表(权威,持久)通过 AgentSource port 读
//   - hub.Hub(可选,在线状态 + 实时工具列表)
//   - 离线 agent 也会出现在结果里,只是 `online=false`,下游 LLM 自己决定是否调用
//
// 查询语义:
//   - query 空 → 列全部 approved agent
//   - query 非空 → 对 display_name + description 做子串匹配(不区分大小写)
//   - filter.tags → AND 语义,必须全命中
//   - filter.agent_type / owner_uids → 精确 OR
package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/eyrihe999-stack/Synapse/internal/agent/hub"
	"github.com/eyrihe999-stack/Synapse/internal/retrieval"
)

// ModalityName retrieval 注册表里的 key。单独定义避免 retrieval 包"知道" agent 这个概念。
const ModalityName retrieval.Modality = "agent"

// AgentRecord catalog 所需的 agent 元信息快照,port 实现方填充。
type AgentRecord struct {
	ID          uint64
	OwnerUID    uint64
	Slug        string
	DisplayName string
	Description string
	AgentType   string
	Tags        []string
	Version     string
}

// AgentSource 从存储层读"当前 org 能见的 agent"。main.go 注入一个包 publish service 的 adapter。
type AgentSource interface {
	// ListApprovedForOrg 列出 orgID 下所有 approved publish 对应的 agent。
	// 单页 list 即可 —— 当前假设 org 下可用 agent < 1000。将来扩量改分页。
	ListApprovedForOrg(ctx context.Context, orgID uint64) ([]AgentRecord, error)

	// GetApproved 按 (agentID, orgID) 精确查,用于 FetchByID。
	// 非 approved / 不存在 返 (nil, nil)。
	GetApproved(ctx context.Context, agentID, orgID uint64) (*AgentRecord, error)
}

// Adapter 实现 retrieval.Retriever,把 agent discovery 作为 modality="agent" 的检索器。
type Adapter struct {
	source AgentSource
	hub    *hub.Hub // 可为 nil:不报告在线状态 / 工具列表,只列元信息
}

// New 构造。hub 可为 nil。
func New(source AgentSource, h *hub.Hub) *Adapter {
	if source == nil {
		panic("catalog: source must be non-nil")
	}
	return &Adapter{source: source, hub: h}
}

func (a *Adapter) Modality() retrieval.Modality { return ModalityName }

func (a *Adapter) FilterSchema() json.RawMessage { return agentFilterSchema() }

func (a *Adapter) Search(ctx context.Context, q retrieval.Query) ([]retrieval.Hit, error) {
	if q.OrgID == 0 {
		return nil, errors.New("catalog: orgID required")
	}

	var f AgentFilter
	if len(q.Filter) > 0 {
		if err := json.Unmarshal(q.Filter, &f); err != nil {
			return nil, fmt.Errorf("catalog: parse filter: %w", err)
		}
	}

	rows, err := a.source.ListApprovedForOrg(ctx, q.OrgID)
	if err != nil {
		return nil, err
	}

	needle := strings.ToLower(strings.TrimSpace(q.Text))

	hits := make([]retrieval.Hit, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		if !matchFilter(r, &f) {
			continue
		}
		if needle != "" && !matchText(r, needle) {
			continue
		}
		hits = append(hits, a.toHit(r))
	}

	// 排序:在线优先(score desc),其次名字字典序(stable)。
	slices.SortStableFunc(hits, func(i, j retrieval.Hit) int {
		if i.Score != j.Score {
			if i.Score > j.Score {
				return -1
			}
			return 1
		}
		return 0
	})

	if q.TopK > 0 && len(hits) > q.TopK {
		hits = hits[:q.TopK]
	}
	return hits, nil
}

func (a *Adapter) FetchByID(ctx context.Context, orgID uint64, id string) (*retrieval.Hit, error) {
	if orgID == 0 {
		return nil, errors.New("catalog: orgID required")
	}
	agentID, err := ParseID(id)
	if err != nil {
		return nil, err
	}
	rec, err := a.source.GetApproved(ctx, agentID, orgID)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, nil
	}
	hit := a.toHit(rec)
	return &hit, nil
}

// toHit 把 AgentRecord + 在线状态 + 工具列表打包成 retrieval.Hit。
func (a *Adapter) toHit(r *AgentRecord) retrieval.Hit {
	online := false
	var tools []json.RawMessage
	if a.hub != nil {
		if info, ok := a.hub.OnlineInfo(r.ID); ok {
			online = true
			tools = info.Tools
		}
	}

	// Score:在线 1.0,离线 0.5。给 LLM 一个轻量排序信号,"能调的优先看"。
	score := float32(0.5)
	scorer := "registry"
	if online {
		score = 1.0
		scorer = "registry+live"
	}

	meta := map[string]any{
		"slug":         r.Slug,
		"display_name": r.DisplayName,
		"owner_uid":    r.OwnerUID,
		"agent_type":   r.AgentType,
		"tags":         r.Tags,
		"version":      r.Version,
		"online":       online,
		"invoke_url":   fmt.Sprintf("/api/v2/agents/%d/%s/mcp", r.OwnerUID, r.Slug),
	}
	if len(tools) > 0 {
		// 工具原样带回,LLM 可以据此判断要不要连这个 agent。
		meta["tools"] = tools
	}

	b, _ := json.Marshal(meta)

	return retrieval.Hit{
		ID:       FormatID(r.ID),
		Modality: ModalityName,
		Score:    score,
		Scorer:   scorer,
		SourceRef: retrieval.SourceRef{
			Kind:  "agent",
			DocID: strconv.FormatUint(r.ID, 10),
		},
		Snippet:  r.Description,
		Metadata: b,
	}
}

// matchFilter tags AND,agent_type / owner_uids OR。
func matchFilter(r *AgentRecord, f *AgentFilter) bool {
	if len(f.Tags) > 0 {
		for _, want := range f.Tags {
			if !slices.Contains(r.Tags, want) {
				return false
			}
		}
	}
	if f.AgentType != "" && r.AgentType != f.AgentType {
		return false
	}
	if len(f.OwnerUIDs) > 0 && !slices.Contains(f.OwnerUIDs, r.OwnerUID) {
		return false
	}
	return true
}

// matchText 子串匹配 display_name + description。不做 fancy ranking —— agent 列表量小。
func matchText(r *AgentRecord, needle string) bool {
	if strings.Contains(strings.ToLower(r.DisplayName), needle) {
		return true
	}
	if strings.Contains(strings.ToLower(r.Description), needle) {
		return true
	}
	return false
}

// FormatID "agent:{id}" 跨模态 ID 前缀。
func FormatID(id uint64) string {
	return "agent:" + strconv.FormatUint(id, 10)
}

// ParseID "agent:42" → 42。
func ParseID(id string) (uint64, error) {
	const prefix = "agent:"
	if !strings.HasPrefix(id, prefix) {
		return 0, fmt.Errorf("catalog: invalid ID prefix: %q", id)
	}
	return strconv.ParseUint(strings.TrimPrefix(id, prefix), 10, 64)
}
