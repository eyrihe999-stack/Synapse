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
	"strings"

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
	// PR-B' Architect 增强:让 LLM 能基于 KB 理解需求 + 看 org 成员名册让用户分配 task。
	ToolListProjectKBRefs     = "list_project_kb_refs"
	ToolGetKBDocumentContent  = "get_kb_document_content"
	ToolListOrgMembers        = "list_org_members"
)

// kbDocContentMaxBytes Architect get_kb_document_content 单次返回上限。
// Architect 只需要 PRD 类摘要级文档,50KB 够长(约 20-30 页 markdown);超过的截断 + 提示。
const kbDocContentMaxBytes = 50 * 1024

// requiredTaskDescSections split_workstream_into_tasks 的 description 必须包含的 5 段标题。
// 由 dispatchSplitWorkstreamIntoTasks 强制校验,缺一拒批。Prompt 里也写了模板,
// 这是双层保险 —— LLM 不可能偷懒只填一句话。
var requiredTaskDescSections = []string{
	"【背景】", "【目标】", "【产出物】", "【参考】", "【验收标准】",
}

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
		{
			Name: ToolListProjectKBRefs,
			Description: "列出当前 project 挂载的全部 KB(source 或 document)。Architect 用这个" +
				"看项目下挂了哪些参考材料(PRD / 设计文档 / 数据源);如果有 PRD 类 doc,接下来用 " +
				"get_kb_document_content 读全文,理解用户意图后再做规划。",
			ParametersJSONSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{},
			},
		},
		{
			Name: ToolGetKBDocumentContent,
			Description: "读 KB 中某个 document 的全文(text/markdown)。doc 必须是当前 project 挂载的(否则" +
				"权限拒绝)。返回值:title + content 字符串(content 上限 50KB,超出截断并标记 truncated:true)。" +
				"用法:先 list_project_kb_refs 拿到 doc id,再调本 tool 读全文。",
			ParametersJSONSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"doc_id"},
				"properties": map[string]any{
					"doc_id": map[string]any{
						"type": "integer", "minimum": 1,
						"description": "KB 文档 id(从 list_project_kb_refs 返回的 kb_document_id 拿)",
					},
				},
			},
		},
		{
			Name: ToolListOrgMembers,
			Description: "列出当前 project 所属 org 的全部活跃 user 成员(不含 agent / banned 用户)。" +
				"Architect 在分配 task assignee 时**必查**,把名单展示给用户,让用户告诉你'前端谁、" +
				"后端谁、测试谁',然后再用对应的 principal_id 去 split_workstream_into_tasks。" +
				"**绝对不要**自己决定谁干哪个 task。",
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
		return dispatchSplitWorkstreamIntoTasks(ctx, s, actorUserID, rawArgs)
	case ToolInviteToWorkstream:
		return dispatchInviteToWorkstream(ctx, s, actorUserID, rawArgs)
	case ToolGetProjectRoadmap:
		return dispatchGetProjectRoadmap(ctx, s, projectID)
	case ToolListProjectKBRefs:
		return dispatchListProjectKBRefs(ctx, s, projectID)
	case ToolGetKBDocumentContent:
		return dispatchGetKBDocumentContent(ctx, s, rawArgs)
	case ToolListOrgMembers:
		return dispatchListOrgMembers(ctx, s, projectID)
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
	v, err := s.PM().Version.Create(ctx, projectID, actorUserID, a.Name, a.Status, nil)
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

func dispatchSplitWorkstreamIntoTasks(ctx context.Context, s *scoped.ScopedServices, actorUserID uint64, rawArgs string) string {
	var a splitTasksArgs
	if err := json.Unmarshal([]byte(rawArgs), &a); err != nil {
		return encodeError(fmt.Sprintf("invalid arguments: %s", err.Error()))
	}
	if a.WorkstreamID == 0 || len(a.Tasks) == 0 {
		return encodeError("workstream_id and tasks are required")
	}

	// 硬约束:任何 task 带 assignee_principal_id 时,本 turn 必须先调过 list_org_members。
	// 这保证 LLM "看过" org 名册才能分配人,不能凭空想当然写 principal_id。
	// 如果 EnableProjectPreScan=true,handler.buildProjectPreScan 已经预先 mark 过,放行。
	hasAssignee := false
	for _, t := range a.Tasks {
		if t.AssigneePrincipalID != 0 {
			hasAssignee = true
			break
		}
	}
	if hasAssignee && !s.HasToolBeenCalled(ToolListOrgMembers) {
		return encodeError("assignee guard: 本 turn 必须先调 list_org_members 拿 org 成员名册," +
			"再用对应的 principal_id 做 assignee。" +
			"流程:list_org_members → post_message 把名单展示给用户 → 等用户回复'前端谁/后端谁/测试谁' → 再 split 带 assignee。" +
			"如果不想现在分配,可以先创建 task 时省略 assignee_principal_id 字段。")
	}

	// 硬约束:每个 task 的 description 必须包含 5 段上下文(背景/目标/产出物/参考/验收标准)。
	// LLM 不能偷懒输出一句话 description —— 任何一段缺失都拒绝整批,具体错误回喂 LLM 让它重试。
	// 这是 prompt 之外的 hardness 层,不依赖 LLM 自觉。
	var formatErrors []string
	for i, t := range a.Tasks {
		var missing []string
		for _, sec := range requiredTaskDescSections {
			if !strings.Contains(t.Description, sec) {
				missing = append(missing, sec)
			}
		}
		if len(missing) > 0 {
			formatErrors = append(formatErrors,
				fmt.Sprintf("task[%d] %q missing required section(s): %s",
					i, t.Title, strings.Join(missing, ", ")))
		}
	}
	if len(formatErrors) > 0 {
		return encodeError(fmt.Sprintf(
			"task description format check failed (each task description MUST contain all 5 sections: %s):\n%s\n\n"+
				"请重新生成 split_workstream_into_tasks 调用,每个 task.description 必须按这个模板填(缺哪段补哪段):\n"+
				"【背景】\n[1-3 句:为什么做这个 task,关联 PRD / 上游需求,引用具体 KB doc 标题]\n\n"+
				"【目标】\n[1-2 句:这个 task 要交付什么,具体到产物形态]\n\n"+
				"【产出物】\n- [文件 / 接口 / UI 截图 / 文档,逐项列出]\n\n"+
				"【参考】\n- [KB doc 标题 / 关联代码位置]\n\n"+
				"【验收标准】\n- [可勾选的具体条件]",
			strings.Join(requiredTaskDescSections, " "),
			strings.Join(formatErrors, "\n"),
		))
	}

	w, err := s.PM().Workstream.Get(ctx, a.WorkstreamID)
	if err != nil {
		return encodeError(err.Error())
	}
	if w.ChannelID == nil || *w.ChannelID == 0 {
		return encodeError("workstream channel not yet created (consumer is async); retry in a few seconds")
	}

	// 幂等把 Architect + 所有 assignee 一并加入 ws channel:
	//   - Architect 自身:否则 task service 的 requireChannelMember(creator=architect_pid)
	//     会拒绝创建 task,报 "task: forbidden"
	//   - assignee:否则 task service 的 IsChannelMember(assignee_pid) 校验失败,报
	//     "task: assignee not in channel" —— 用户给的成员分配只覆盖 ws "成员",
	//     不一定包含每个 task 的 assignee(实测确实有人指 ws 成员外的 principal)
	// InviteToChannel 走 INSERT IGNORE,已是成员的不重复加。一次调用比每个 task
	// 失败后再补 invite 节省 round-trip,也避免 LLM 看到 partial fail 后乱重试。
	pidsToInvite := []uint64{s.ActorPrincipalID()}
	seen := map[uint64]bool{s.ActorPrincipalID(): true}
	for _, t := range a.Tasks {
		if t.AssigneePrincipalID != 0 && !seen[t.AssigneePrincipalID] {
			seen[t.AssigneePrincipalID] = true
			pidsToInvite = append(pidsToInvite, t.AssigneePrincipalID)
		}
	}
	if _, _, err := s.PM().Workstream.InviteToChannel(ctx, a.WorkstreamID, actorUserID, pidsToInvite); err != nil {
		return encodeError(fmt.Sprintf("ensure ws channel membership (architect+assignees): %s", err.Error()))
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

// ── PR-B' Architect 增强 dispatcher ────────────────────────────────────────

func dispatchListProjectKBRefs(ctx context.Context, s *scoped.ScopedServices, projectID uint64) string {
	refs, err := s.ListProjectKBRefs(ctx, projectID)
	if err != nil {
		return encodeError(err.Error())
	}
	return encodeOK(map[string]any{
		"project_id": projectID,
		"refs":       refs,
		"hint": "如果 refs 里有 kb_document_id != 0 的条目,接下来用 get_kb_document_content " +
			"读 doc 全文(每个 doc 一次调用);kb_source_id 是整源挂载,目前 LLM 不读全源," +
			"知道挂了什么源即可。",
	})
}

type getKBDocContentArgs struct {
	DocID uint64 `json:"doc_id"`
}

func dispatchGetKBDocumentContent(ctx context.Context, s *scoped.ScopedServices, rawArgs string) string {
	var a getKBDocContentArgs
	if err := json.Unmarshal([]byte(rawArgs), &a); err != nil {
		return encodeError(fmt.Sprintf("invalid arguments: %s", err.Error()))
	}
	if a.DocID == 0 {
		return encodeError("doc_id is required")
	}
	doc, err := s.GetKBDocument(ctx, a.DocID)
	if err != nil {
		return encodeError(err.Error())
	}
	content := doc.FullText
	truncated := doc.Truncated
	if len(content) > kbDocContentMaxBytes {
		content = content[:kbDocContentMaxBytes]
		truncated = true
	}
	title := ""
	fileName := ""
	if doc.Document != nil {
		title = doc.Document.Title
		fileName = doc.Document.FileName
	}
	return encodeOK(map[string]any{
		"doc_id":           a.DocID,
		"title":            title,
		"file_name":        fileName,
		"content":          content,
		"truncated":        truncated,
		"chunk_count":      doc.ChunkCount,
		"full_text_source": doc.FullTextSource,
	})
}

func dispatchListOrgMembers(ctx context.Context, s *scoped.ScopedServices, projectID uint64) string {
	rows, err := s.ListProjectOrgMembers(ctx, projectID)
	if err != nil {
		return encodeError(err.Error())
	}
	// 标记本 turn 已调过 list_org_members,后续 split_workstream_into_tasks 带 assignee 才允许放行
	s.MarkToolCalled(ToolListOrgMembers)
	return encodeOK(map[string]any{
		"project_id": projectID,
		"members":    rows,
		"hint": "把这份名单展示给用户,问'前端谁/后端谁/测试谁/产品谁'。" +
			"用户告诉你后,用对应的 principal_id 调 split_workstream_into_tasks 的 assignee_principal_id。" +
			"绝对不要自己决定谁干哪个 task。",
	})
}
