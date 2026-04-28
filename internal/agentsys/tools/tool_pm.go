// tool_pm.go agentsys 内 LLM 可调的 PM 编排 tool(PR-B B2)。
//
// 主要给 Project Architect 用,实现"用户在 Console 描述需求 → Architect 拆解 →
// 直接调 PM tool 落实"链路。Top-orchestrator 也能拿到这组 tool(Schema 里有),
// 但它的 prompt 不教它做项目编排,实际不会用。
//
// 实现策略:Architect 是 system agent 跨 org,而 pm.Service 接口需要 actor user_id
// (做 org membership 校验)。绕过方式:**借 project owner(projects.created_by)
// 的 user_id 作为 actor**。这有个副作用:event payload 的 actor_user_id 是 owner,
// 不是 Architect 本人 —— audit trail 不精确,但 v0 简化下可接受。后续 PR 可以加
// pm.Service 的 ByPrincipalAgent 接口让 system agent 直接调,绕过 IsMember 校验。
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/agentsys/scoped"
	"github.com/eyrihe999-stack/Synapse/internal/common/llm"
)

// PR-B B2 PM 编排 tool 名常量。
const (
	ToolCreateInitiative         = "create_initiative"
	ToolCreateVersion            = "create_version"
	ToolCreateWorkstream         = "create_workstream"
	ToolSplitWorkstreamIntoTasks = "split_workstream_into_tasks"
	ToolInviteToWorkstream       = "invite_to_workstream"
	ToolGetProjectRoadmap        = "get_project_roadmap"
)

// pmToolSchema 返回 6 个 PM tool 的 LLM-facing JSON schema。
//
// 字段约定:每个 tool 通过 channel 上下文(scoped.channelID)反查 project_id,
// 不要求 LLM 显式传 project_id —— Architect 只在 Console channel 触发,channel
// 唯一对应一个 project。例外:get_project_roadmap 不依赖 channel(可以查任意
// project),也仍然默认查"当前 channel 所属 project"。
func pmToolSchema() []llm.ToolDef {
	return []llm.ToolDef{
		{
			Name: ToolCreateInitiative,
			Description: "在当前 channel 所属 project 下创建 initiative(主题轴,'为什么做')。" +
				"name 必填;description / target_outcome 可选。返回创建的 initiative 元数据。",
			ParametersJSONSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"name"},
				"properties": map[string]any{
					"name": map[string]any{
						"type": "string", "minLength": 1, "maxLength": 128,
						"description": "Initiative 名(同 project 内唯一)",
					},
					"description":    map[string]any{"type": "string", "maxLength": 1024},
					"target_outcome": map[string]any{"type": "string", "maxLength": 4096},
				},
			},
		},
		{
			Name: ToolCreateVersion,
			Description: "在当前 channel 所属 project 下创建 version(发版窗口,'什么时候发')。" +
				"name 必填(如 'v1.5');status 必填,枚举 'planning' | 'active' | 'released' | 'cancelled'。",
			ParametersJSONSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"name", "status"},
				"properties": map[string]any{
					"name":   map[string]any{"type": "string", "minLength": 1, "maxLength": 64},
					"status": map[string]any{"type": "string", "enum": []string{"planning", "active", "released", "cancelled"}},
				},
			},
		},
		{
			Name: ToolCreateWorkstream,
			Description: "在某 initiative 下新建 workstream(工作切片,'怎么做')。可选挂某个 version。" +
				"创建后异步 lazy-create 一个 kind=workstream channel(consumer 几秒内完成),之后才能 split_workstream_into_tasks 或 invite_to_workstream。",
			ParametersJSONSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"initiative_id", "name"},
				"properties": map[string]any{
					"initiative_id": map[string]any{"type": "integer", "minimum": 1},
					"name":          map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
					"description":   map[string]any{"type": "string", "maxLength": 4096},
					"version_id": map[string]any{
						"type":        "integer",
						"description": "可选;不传 = 进 Backlog",
					},
				},
			},
		},
		{
			Name: ToolSplitWorkstreamIntoTasks,
			Description: "在某 workstream 关联的 channel 内,一次创建多个 task(batch)。" +
				"workstream channel 必须已 lazy-create(等 1-2 秒后调)。tasks 数组,每项包含 title(必填)+ description / assignee_principal_id 可选。",
			ParametersJSONSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"workstream_id", "tasks"},
				"properties": map[string]any{
					"workstream_id": map[string]any{"type": "integer", "minimum": 1},
					"tasks": map[string]any{
						"type":     "array",
						"minItems": 1,
						"maxItems": 20,
						"items": map[string]any{
							"type":     "object",
							"required": []string{"title"},
							"properties": map[string]any{
								"title":                 map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
								"description":           map[string]any{"type": "string", "maxLength": 4096},
								"assignee_principal_id": map[string]any{"type": "integer"},
								"is_lightweight":        map[string]any{"type": "boolean"},
							},
						},
					},
				},
			},
		},
		{
			Name: ToolInviteToWorkstream,
			Description: "把一组 principal 加进某 workstream 关联的 channel(角色 member)。幂等;已是成员的不重复加。",
			ParametersJSONSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"workstream_id", "principal_ids"},
				"properties": map[string]any{
					"workstream_id": map[string]any{"type": "integer", "minimum": 1},
					"principal_ids": map[string]any{
						"type":     "array",
						"minItems": 1,
						"items":    map[string]any{"type": "integer", "minimum": 1},
					},
				},
			},
		},
		{
			Name: ToolGetProjectRoadmap,
			Description: "查当前 channel 所属 project 的 roadmap:返 initiatives + versions + workstreams 三组列表 " +
				"(各自 active 行,含基本字段)。Architect 在创建新 initiative / workstream 前**必做**,避免重复。",
			ParametersJSONSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{},
			},
		},
	}
}

// dispatchPMTool 总入口。按 toolName 分发,共享 caller 反查逻辑(channel→project→owner)。
func dispatchPMTool(ctx context.Context, s *scoped.ScopedServices, toolName, rawArgs string) string {
	if s.PM() == nil {
		return encodeError("pm service not configured (top-orchestrator path?)")
	}
	projectID, err := s.LookupProjectIDForChannel(ctx)
	if err != nil {
		return encodeError(fmt.Sprintf("lookup project id: %s", err.Error()))
	}
	actorUserID, err := s.LookupProjectOwnerUserID(ctx, projectID)
	if err != nil {
		return encodeError(fmt.Sprintf("lookup project owner: %s", err.Error()))
	}

	switch toolName {
	case ToolCreateInitiative:
		return dispatchCreateInitiative(ctx, s, projectID, actorUserID, rawArgs)
	case ToolCreateVersion:
		return dispatchCreateVersion(ctx, s, projectID, actorUserID, rawArgs)
	case ToolCreateWorkstream:
		return dispatchCreateWorkstream(ctx, s, actorUserID, rawArgs)
	case ToolSplitWorkstreamIntoTasks:
		return dispatchSplitWorkstreamIntoTasks(ctx, s, rawArgs)
	case ToolInviteToWorkstream:
		return dispatchInviteToWorkstream(ctx, s, actorUserID, rawArgs)
	case ToolGetProjectRoadmap:
		return dispatchGetProjectRoadmap(ctx, s, projectID)
	}
	return encodeError(fmt.Sprintf("unknown pm tool %q", toolName))
}

// ── argument types ──────────────────────────────────────────────────────────

type createInitiativeArgs struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	TargetOutcome string `json:"target_outcome"`
}

type createVersionArgs struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type createWorkstreamArgs struct {
	InitiativeID uint64  `json:"initiative_id"`
	Name         string  `json:"name"`
	Description  string  `json:"description"`
	VersionID    *uint64 `json:"version_id,omitempty"`
}

type splitTasksArgs struct {
	WorkstreamID uint64 `json:"workstream_id"`
	Tasks        []struct {
		Title               string `json:"title"`
		Description         string `json:"description"`
		AssigneePrincipalID uint64 `json:"assignee_principal_id"`
		IsLightweight       bool   `json:"is_lightweight"`
	} `json:"tasks"`
}

type inviteArgs struct {
	WorkstreamID uint64   `json:"workstream_id"`
	PrincipalIDs []uint64 `json:"principal_ids"`
}

// ── dispatchers ─────────────────────────────────────────────────────────────

func dispatchCreateInitiative(ctx context.Context, s *scoped.ScopedServices, projectID, actorUserID uint64, rawArgs string) string {
	var a createInitiativeArgs
	if err := json.Unmarshal([]byte(rawArgs), &a); err != nil {
		return encodeError(fmt.Sprintf("invalid arguments: %s", err.Error()))
	}
	init, err := s.PM().Initiative.Create(ctx, projectID, actorUserID, a.Name, a.Description, a.TargetOutcome)
	if err != nil {
		return encodeError(err.Error())
	}
	return encodeOK(map[string]any{
		"initiative_id": init.ID,
		"name":          init.Name,
		"status":        init.Status,
	})
}

func dispatchCreateVersion(ctx context.Context, s *scoped.ScopedServices, projectID, actorUserID uint64, rawArgs string) string {
	var a createVersionArgs
	if err := json.Unmarshal([]byte(rawArgs), &a); err != nil {
		return encodeError(fmt.Sprintf("invalid arguments: %s", err.Error()))
	}
	v, err := s.PM().Version.Create(ctx, projectID, actorUserID, a.Name, a.Status)
	if err != nil {
		return encodeError(err.Error())
	}
	return encodeOK(map[string]any{
		"version_id": v.ID,
		"name":       v.Name,
		"status":     v.Status,
	})
}

func dispatchCreateWorkstream(ctx context.Context, s *scoped.ScopedServices, actorUserID uint64, rawArgs string) string {
	var a createWorkstreamArgs
	if err := json.Unmarshal([]byte(rawArgs), &a); err != nil {
		return encodeError(fmt.Sprintf("invalid arguments: %s", err.Error()))
	}
	w, err := s.PM().Workstream.Create(ctx, a.InitiativeID, actorUserID, a.VersionID, a.Name, a.Description)
	if err != nil {
		return encodeError(err.Error())
	}
	return encodeOK(map[string]any{
		"workstream_id": w.ID,
		"name":          w.Name,
		"initiative_id": w.InitiativeID,
		"version_id":    w.VersionID,
		"note":          "channel is being lazy-created asynchronously; wait 1-2s before split_workstream_into_tasks / invite_to_workstream",
	})
}

func dispatchSplitWorkstreamIntoTasks(ctx context.Context, s *scoped.ScopedServices, rawArgs string) string {
	var a splitTasksArgs
	if err := json.Unmarshal([]byte(rawArgs), &a); err != nil {
		return encodeError(fmt.Sprintf("invalid arguments: %s", err.Error()))
	}
	if a.WorkstreamID == 0 || len(a.Tasks) == 0 {
		return encodeError("workstream_id and tasks are required")
	}

	w, err := s.PM().Workstream.Get(ctx, a.WorkstreamID)
	if err != nil {
		return encodeError(err.Error())
	}
	if w.ChannelID == nil || *w.ChannelID == 0 {
		return encodeError("workstream channel not yet created (consumer is async); retry in a few seconds")
	}

	// Architect 在 console channel,但 task 要落到 workstream channel。
	// 直接调 task service 用 actor=Architect principal(系统 agent 创建,审计可识别)
	type taskOK struct {
		Index  int    `json:"index"`
		TaskID uint64 `json:"task_id"`
		Title  string `json:"title"`
	}
	type taskFail struct {
		Index int    `json:"index"`
		Title string `json:"title"`
		Error string `json:"error"`
	}
	created := []taskOK{}
	failed := []taskFail{}

	for i, t := range a.Tasks {
		out, err := s.CreateTaskInChannel(ctx, *w.ChannelID, t.Title, t.Description,
			t.AssigneePrincipalID, t.IsLightweight)
		if err != nil {
			failed = append(failed, taskFail{Index: i, Title: t.Title, Error: err.Error()})
			continue
		}
		created = append(created, taskOK{Index: i, TaskID: out.ID, Title: t.Title})
	}
	return encodeOK(map[string]any{
		"workstream_id": a.WorkstreamID,
		"channel_id":    *w.ChannelID,
		"created":       created,
		"failed":        failed,
		"summary":       fmt.Sprintf("%d created, %d failed", len(created), len(failed)),
	})
}

func dispatchInviteToWorkstream(ctx context.Context, s *scoped.ScopedServices, actorUserID uint64, rawArgs string) string {
	var a inviteArgs
	if err := json.Unmarshal([]byte(rawArgs), &a); err != nil {
		return encodeError(fmt.Sprintf("invalid arguments: %s", err.Error()))
	}
	added, channelID, err := s.PM().Workstream.InviteToChannel(ctx, a.WorkstreamID, actorUserID, a.PrincipalIDs)
	if err != nil {
		return encodeError(err.Error())
	}
	return encodeOK(map[string]any{
		"workstream_id": a.WorkstreamID,
		"channel_id":    channelID,
		"invited":       added,
	})
}

// dispatchGetProjectRoadmap 返聚合 roadmap。当前实现只列 active(non-archived) entity,
// 不做 cross-table JOIN —— 三次独立查询足够小数据集 + 易读输出给 LLM。
func dispatchGetProjectRoadmap(ctx context.Context, s *scoped.ScopedServices, projectID uint64) string {
	pm := s.PM()
	inits, err := pm.Initiative.List(ctx, projectID, 200, 0)
	if err != nil {
		return encodeError(fmt.Sprintf("list initiatives: %s", err.Error()))
	}
	versions, err := pm.Version.List(ctx, projectID)
	if err != nil {
		return encodeError(fmt.Sprintf("list versions: %s", err.Error()))
	}
	workstreams, err := pm.Workstream.ListByProject(ctx, projectID, 500, 0)
	if err != nil {
		return encodeError(fmt.Sprintf("list workstreams: %s", err.Error()))
	}

	initSlim := make([]map[string]any, 0, len(inits))
	for _, i := range inits {
		if i.ArchivedAt != nil {
			continue
		}
		initSlim = append(initSlim, map[string]any{
			"id": i.ID, "name": i.Name, "status": i.Status, "is_system": i.IsSystem,
			"target_outcome": i.TargetOutcome,
		})
	}
	verSlim := make([]map[string]any, 0, len(versions))
	for _, v := range versions {
		if v.Status == "cancelled" {
			continue
		}
		verSlim = append(verSlim, map[string]any{
			"id": v.ID, "name": v.Name, "status": v.Status,
			"is_system": v.IsSystem, "target_date": v.TargetDate, "released_at": v.ReleasedAt,
		})
	}
	wsSlim := make([]map[string]any, 0, len(workstreams))
	for _, w := range workstreams {
		if w.ArchivedAt != nil {
			continue
		}
		wsSlim = append(wsSlim, map[string]any{
			"id": w.ID, "name": w.Name, "status": w.Status,
			"initiative_id": w.InitiativeID, "version_id": w.VersionID,
			"channel_id": w.ChannelID,
		})
	}
	return encodeOK(map[string]any{
		"project_id":  projectID,
		"initiatives": initSlim,
		"versions":    verSlim,
		"workstreams": wsSlim,
	})
}
