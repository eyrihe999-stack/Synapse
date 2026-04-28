// Package tools 顶级系统 agent 暴露给 LLM 的 function-calling 工具白名单。
//
// 关键设计:
//   - tool schema **不含** org_id / channel_id 参数 —— scope 由 ScopedServices 绑死
//   - dispatcher 只调 *scoped.ScopedServices 上的方法,禁止直接 import channel/task
//     service;这样"忘了过滤 org"这类 bug 不可能从这里泄露出去
//   - 每个 tool 的返回值序列化成 JSON 字符串,作为 tool role 消息回喂 LLM
//
// 工具白名单(V1):
//
//	post_message            LLM 给用户的最终回复(或中途提醒)
//	create_task             LLM 判断"需要派个任务",派到当前 channel
//	list_recent_messages    LLM 主动翻更早的上下文(默认 handler 已带 20 条,此处用于长对话)
//	list_channel_kb_refs    列出当前 channel 挂的 KB 文档 / 代码库
//
// 不提供:任何带 org_id / channel_id 的查询、任何跨 channel 的操作、
// 任何 admin / 查全局用户列表的工具。LLM 能看到的世界就是当前 channel。
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/agentsys/scoped"
	"github.com/eyrihe999-stack/Synapse/internal/common/llm"
)

// Tool 名字常量,和 llm.ToolDef.Name / LLM 回来的 ToolCall.Name 对齐。
const (
	ToolPostMessage        = "post_message"
	ToolCreateTask         = "create_task"
	ToolListRecentMessages = "list_recent_messages"
	ToolListChannelMembers = "list_channel_members"
	ToolSearchKB           = "search_kb"
	ToolGetKBDocument      = "get_kb_document"
)

// Schema 返回暴露给 LLM 的完整 tool 定义列表。
//
// JSON Schema 是 draft-07 的常用子集(OpenAI / Azure 均支持),不用高级特性。
// 更改此函数必须同步更新 dispatcher 的 switch case;两处漂移 LLM 就会调不通。
func Schema() []llm.ToolDef {
	base := []llm.ToolDef{
		{
			Name:        ToolPostMessage,
			Description: "在当前 channel 回复一条文本消息给用户。这通常是你对用户问题的最终答复。",
			ParametersJSONSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"body"},
				"properties": map[string]any{
					"body": map[string]any{
						"type":        "string",
						"description": "消息正文(UTF-8 文本,尽量简洁;复杂内容拆成多条或改用 create_task)",
						"maxLength":   8000,
					},
					"mention_principal_ids": map[string]any{
						"type":        "array",
						"description": "需要 @ 提醒的 principal_id 数组;不填则纯文本消息",
						"items":       map[string]any{"type": "integer"},
					},
				},
			},
		},
		{
			Name:        ToolCreateTask,
			Description: "在当前 channel 派发一个结构化任务给某个成员。适合复杂、多步骤、需人或 agent 异步完成的工作。",
			ParametersJSONSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"title", "description", "assignee_principal_id"},
				"properties": map[string]any{
					"title": map[string]any{
						"type":        "string",
						"description": "任务标题(≤128 字符,简明描述产物 / 目标)",
						"maxLength":   128,
					},
					"description": map[string]any{
						"type":        "string",
						"description": "任务说明(可多段,讲清楚输入 / 约束 / 验收标准)",
						"maxLength":   8000,
					},
					"assignee_principal_id": map[string]any{
						"type":        "integer",
						"description": "认领这个任务的 principal_id。必须是当前 channel 的成员。",
					},
					"output_spec_kind": map[string]any{
						"type":        "string",
						"description": "期望的产物形态(如 markdown / json / file);留空走默认",
					},
					"reviewer_principal_ids": map[string]any{
						"type":        "array",
						"description": "审批人 principal_id 列表;**留空** → 后端自动 fallback 为任务发起人(即 @ 你的那条消息的作者),由发起人自己验收。用户若在对话里点名让某人审,再把那人的 principal_id 放进来。",
						"items":       map[string]any{"type": "integer"},
					},
					"required_approvals": map[string]any{
						"type":        "integer",
						"description": "所需通过审批人数。**建议不传**,后端按 reviewer 数自动推(有 reviewer = 1,无 = 0)。只有当用户明确要求至少 N 人同意时才指定。",
					},
				},
			},
		},
		{
			Name:        ToolListRecentMessages,
			Description: "翻看当前 channel 最近的消息历史。handler 已经把最近若干条放进了你的上下文;此工具用于你需要更多上下文时主动拉取。",
			ParametersJSONSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"limit": map[string]any{
						"type":        "integer",
						"description": "最多返回多少条(1-50,缺省 20)",
						"minimum":     1,
						"maximum":     50,
					},
				},
			},
		},
		// ToolListChannelKBRefs 已退役 —— channel_kb_refs 表 + per-channel KB 挂载概念
		// 整体废弃。LLM 想看 KB 直接调 list_kb_documents tool(走 channel.project_id
		// JOIN project_kb_refs 算可见集)。
		{
			Name: ToolListChannelMembers,
			Description: "列出当前 channel 的所有成员及其 principal_id、display_name、kind(user/agent_system/agent_user)、role(owner/member/observer)。\n" +
				"**什么时候调用**:\n" +
				"- 用户让你派任务给某人(@某人 或 '指派给 xxx')—— 调这个 tool 拿到那个人的 principal_id 再 create_task\n" +
				"- 用户问'这个 channel 里有哪些人'\n" +
				"- 你看到消息 mentions 字段里有陌生 principal_id,想搞清楚是谁\n" +
				"**不要**反问用户'xxx 是谁',直接调这个 tool。",
			ParametersJSONSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{},
			},
		},
		{
			Name: ToolSearchKB,
			Description: "在当前 channel 挂载的知识库(KB)上做**语义检索**,返回与 query 最相关的 top-K chunks。\n" +
				"**用途**:用户问'我们之前关于 X 是怎么设计的'、'这个需求和现有架构哪里冲突'等需要从已有文档里找答案时,先调这个 tool 定位相关片段,再决定要不要 get_kb_document 拉完整文档。\n" +
				"**返回字段**:doc_id / doc_title / chunk_idx / content / heading_path / distance(0=完全一致, 2=反向)。\n" +
				"channel 没有挂任何 KB → 返空数组,这时直接回用户'当前 channel 没有挂载知识库'。\n" +
				"**只搜本 channel 可见的 KB**,不会跨 channel 取数据。",
			ParametersJSONSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"query"},
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "自然语言 query,会被 embed 成向量后做近邻搜索。可以是问题、关键词组合、或一段描述。",
						"maxLength":   2000,
					},
					"top_k": map[string]any{
						"type":        "integer",
						"description": "返回多少条 chunks(1-20,缺省 5)",
						"minimum":     1,
						"maximum":     20,
					},
				},
			},
		},
		{
			Name: ToolGetKBDocument,
			Description: "拉某篇 KB 文档的元数据 + 完整文本。\n" +
				"**用途**:search_kb 命中相关 chunks 后,需要看完整上下文(整篇 markdown 的章节关系 / 代码块原貌 / 表格)时调本 tool。\n" +
				"**文本来源**:markdown/纯文本走 OSS 原文(无损);二进制(pdf/docx)走 chunks 按 idx 拼接的提取文本。\n" +
				"返回里 `full_text_source` 字段会标记是 'oss' 还是 'chunks_join';`truncated=true` 表示文本超 200KB 被截断。\n" +
				"**只能拉本 channel 可见的文档**(其 source 在 channel kb_refs ∪ 直接挂的 doc)。",
			ParametersJSONSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"document_id"},
				"properties": map[string]any{
					"document_id": map[string]any{
						"type":        "integer",
						"description": "目标文档 id(从 search_kb / list_channel_kb_refs 等拿到)",
					},
				},
			},
		},
	}
	// PR-B B2:把 PM 编排 tool 追加到 schema 末尾。
	return append(base, pmToolSchema()...)
}

// Dispatch 按 tool name 派发到 scoped 的对应方法。
//
// 返回值:序列化的 JSON 字符串(成功 payload 或结构化错误),由 handler 当作
// role=tool 的消息内容回喂 LLM。
//
// 绝对不返回 Go error 让调用方再分层处理 —— 所有 tool 错误都 JSON 化塞进
// content,LLM 能直接读懂并向用户解释。这样 tool 失败不会让整个 LLM loop 崩,
// 也便于审计(handler 只需要判断"结果是否含 error 键"即可写 ActionToolError)。
func Dispatch(ctx context.Context, s *scoped.ScopedServices, call llm.ToolCall) string {
	switch call.Name {
	case ToolPostMessage:
		return dispatchPostMessage(ctx, s, call.ArgumentsJSON)
	case ToolCreateTask:
		return dispatchCreateTask(ctx, s, call.ArgumentsJSON)
	case ToolListRecentMessages:
		return dispatchListRecentMessages(ctx, s, call.ArgumentsJSON)
	case ToolListChannelMembers:
		return dispatchListChannelMembers(ctx, s)
	case ToolSearchKB:
		return dispatchSearchKB(ctx, s, call.ArgumentsJSON)
	case ToolGetKBDocument:
		return dispatchGetKBDocument(ctx, s, call.ArgumentsJSON)
	case ToolCreateInitiative, ToolCreateVersion, ToolCreateWorkstream,
		ToolSplitWorkstreamIntoTasks, ToolInviteToWorkstream, ToolGetProjectRoadmap:
		return dispatchPMTool(ctx, s, call.Name, call.ArgumentsJSON)
	default:
		return encodeError(fmt.Sprintf("unknown tool %q", call.Name))
	}
}

// ─── 各 tool 的 argument 结构 ────────────────────────────────────────────────

type postMessageArgs struct {
	Body                string   `json:"body"`
	MentionPrincipalIDs []uint64 `json:"mention_principal_ids"`
}

type createTaskArgs struct {
	Title                string   `json:"title"`
	Description          string   `json:"description"`
	AssigneePrincipalID  uint64   `json:"assignee_principal_id"`
	OutputSpecKind       string   `json:"output_spec_kind"`
	ReviewerPrincipalIDs []uint64 `json:"reviewer_principal_ids"`
	RequiredApprovals    int      `json:"required_approvals"`
}

type listRecentMessagesArgs struct {
	Limit int `json:"limit"`
}

type searchKBArgs struct {
	Query string `json:"query"`
	TopK  int    `json:"top_k"`
}

type getKBDocumentArgs struct {
	DocumentID uint64 `json:"document_id"`
}

// ─── Tool 执行逻辑 ──────────────────────────────────────────────────────────

func dispatchPostMessage(ctx context.Context, s *scoped.ScopedServices, rawArgs string) string {
	var args postMessageArgs
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return encodeError(fmt.Sprintf("invalid arguments: %s", err.Error()))
	}
	out, err := s.PostMessage(ctx, args.Body, args.MentionPrincipalIDs)
	if err != nil {
		return encodeError(err.Error())
	}
	return encodeOK(map[string]any{
		"message_id": out.Message.ID,
		"mentions":   out.Mentions,
	})
}

func dispatchCreateTask(ctx context.Context, s *scoped.ScopedServices, rawArgs string) string {
	var args createTaskArgs
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return encodeError(fmt.Sprintf("invalid arguments: %s", err.Error()))
	}
	out, err := s.CreateTask(ctx, scoped.CreateTaskArgs{
		Title:                args.Title,
		Description:          args.Description,
		OutputSpecKind:       args.OutputSpecKind,
		AssigneePrincipalID:  args.AssigneePrincipalID,
		ReviewerPrincipalIDs: args.ReviewerPrincipalIDs,
		RequiredApprovals:    args.RequiredApprovals,
	})
	if err != nil {
		return encodeError(err.Error())
	}
	return encodeOK(map[string]any{
		"task_id":   out.Task.ID,
		"status":    out.Task.Status,
		"reviewers": len(out.Reviewers),
	})
}

func dispatchListRecentMessages(ctx context.Context, s *scoped.ScopedServices, rawArgs string) string {
	var args listRecentMessagesArgs
	if rawArgs != "" {
		//sayso-lint:ignore err-swallow
		_ = json.Unmarshal([]byte(rawArgs), &args)
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 20
	}
	msgs, err := s.ListRecentMessages(ctx, limit)
	if err != nil {
		return encodeError(err.Error())
	}
	// 压成给 LLM 看的精简结构:id / author / body / created_at
	slim := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		slim = append(slim, map[string]any{
			"id":                  m.Message.ID,
			"author_principal_id": m.Message.AuthorPrincipalID,
			"body":                m.Message.Body,
			"mentions":            m.Mentions,
			"created_at":          m.Message.CreatedAt,
		})
	}
	return encodeOK(map[string]any{"messages": slim})
}

func dispatchListChannelMembers(ctx context.Context, s *scoped.ScopedServices) string {
	members, err := s.ListMembersWithProfile(ctx)
	if err != nil {
		return encodeError(err.Error())
	}
	slim := make([]map[string]any, 0, len(members))
	for _, m := range members {
		name := m.DisplayName
		if name == "" {
			name = fmt.Sprintf("principal#%d", m.PrincipalID)
		}
		kind := m.Kind
		if kind == "" {
			kind = "unknown"
		}
		slim = append(slim, map[string]any{
			"principal_id":    m.PrincipalID,
			"display_name":    name,
			"kind":            kind,
			"is_global_agent": m.IsGlobalAgent,
			"role":            m.Role,
		})
	}
	return encodeOK(map[string]any{"members": slim})
}

// dispatchListChannelKBRefs 已删除(对应 ToolListChannelKBRefs 退役)。

func dispatchSearchKB(ctx context.Context, s *scoped.ScopedServices, rawArgs string) string {
	var args searchKBArgs
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return encodeError(fmt.Sprintf("invalid arguments: %s", err.Error()))
	}
	hits, err := s.SearchKB(ctx, args.Query, args.TopK)
	if err != nil {
		return encodeError(err.Error())
	}
	slim := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		slim = append(slim, map[string]any{
			"doc_id":        h.DocID,
			"doc_title":     h.DocTitle,
			"doc_file_name": h.DocFileName,
			"mime_type":     h.DocMIMEType,
			"chunk_idx":     h.ChunkIdx,
			"content":       h.Content,
			"heading_path":  h.HeadingPath,
			"distance":      h.Distance,
		})
	}
	return encodeOK(map[string]any{"hits": slim})
}

func dispatchGetKBDocument(ctx context.Context, s *scoped.ScopedServices, rawArgs string) string {
	var args getKBDocumentArgs
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return encodeError(fmt.Sprintf("invalid arguments: %s", err.Error()))
	}
	if args.DocumentID == 0 {
		return encodeError("document_id is required")
	}
	res, err := s.GetKBDocument(ctx, args.DocumentID)
	if err != nil {
		return encodeError(err.Error())
	}
	d := res.Document
	return encodeOK(map[string]any{
		"id":                  d.ID,
		"title":               d.Title,
		"file_name":           d.FileName,
		"mime_type":           d.MIMEType,
		"version":             d.Version,
		"chunk_count":         res.ChunkCount,
		"content_byte_size":   d.ContentByteSize,
		"knowledge_source_id": d.KnowledgeSourceID,
		"updated_at":          d.UpdatedAt,
		"full_text":           res.FullText,
		"full_text_source":    res.FullTextSource,
		"truncated":           res.Truncated,
	})
}

// encodeOK / encodeError 统一用 JSON 包装返回,确保 LLM 看到的结构稳定。
// ok=true/false 让 LLM 能迅速判断要不要重试 / 告知用户失败。
func encodeOK(data map[string]any) string {
	payload := map[string]any{"ok": true, "data": data}
	raw, _ := json.Marshal(payload) //sayso-lint:ignore err-swallow
	return string(raw)
}

func encodeError(msg string) string {
	payload := map[string]any{"ok": false, "error": msg}
	raw, _ := json.Marshal(payload) //sayso-lint:ignore err-swallow
	return string(raw)
}

// IsErrorResult 给 handler 用 —— 判断 Dispatch 返回的 JSON 字符串是不是错误结果,
// 用来决定是否写 audit_events action=tool.error。
func IsErrorResult(s string) bool {
	var probe struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(s), &probe); err != nil {
		// 无法解析视作错误(理论上 encodeOK/encodeError 都能 parse)
		return true
	}
	return !probe.OK
}
