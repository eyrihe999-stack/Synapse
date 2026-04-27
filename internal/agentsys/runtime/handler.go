package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/agentsys/model"
	"github.com/eyrihe999-stack/Synapse/internal/agentsys/prompts"
	"github.com/eyrihe999-stack/Synapse/internal/agentsys/repository"
	"github.com/eyrihe999-stack/Synapse/internal/agentsys/scoped"
	agentsystools "github.com/eyrihe999-stack/Synapse/internal/agentsys/tools"
	"github.com/eyrihe999-stack/Synapse/internal/common/llm"
)

// handleMention 处理"被 @ 顶级 agent"的单条消息。
//
// 失败处理(实现 roadmap 的要求):
//   - LLM 超时 / 报错:post_message "我暂时回不上来"  + audit llm.error + 返回 nil(ACK)
//   - 某 tool 执行失败:post_message "执行工具 X 时出错: ..." + audit tool.error + 不中断后续 loop(让 LLM 决定是否继续)
//   - 达到 MaxToolRounds 还没收敛:post_message "思考轮数用完,给不出答案" + audit llm.error
//   - 写 audit 本身失败:log warn,不阻塞业务;实在挂了 handler 返 err 让上层重放
func (o *Orchestrator) handleMention(ctx context.Context, s *scoped.ScopedServices) error {
	// 一次 handleMention 分配一个短 traceID,所有子步骤(每轮 llm.call、tool dispatch、
	// post_message)在结构化日志里都带同一个 trace,便于从 SLS / docker logs 拼完整链路。
	traceID := newTraceID()
	o.logger.InfoCtx(ctx, "agentsys: mention received", map[string]any{
		"trace_id":     traceID,
		"channel_id":   s.ChannelID(),
		"operating_org_id": s.OperatingOrgID(),
	})

	// 组 prompt
	messages, err := o.buildInitialPrompt(ctx, s)
	if err != nil {
		// 读不到历史消息(DB 问题)不 ACK 重放 —— 可能是 channel 刚被删之类的瞬时不一致
		return fmt.Errorf("build prompt: %w", err)
	}

	tools := agentsystools.Schema()
	posted := false

	for round := 0; round < MaxToolRounds; round++ {
		req := llm.Request{
			Messages: messages,
			Tools:    tools,
			// Temperature 不显式设:gpt-5 / o1 系列新模型**只接受默认 temperature=1.0**,
			// 传其他值(如 0.3)会 400 "Unsupported parameter"。零值走 omitempty 不发送,
			// provider 自己用默认。未来如果要支持老模型再按 provider 分支调度。
			MaxOutputTokens: 2048,
		}
		llmStart := time.Now()
		resp, err := o.llm.Completions(ctx, req)
		llmLatency := time.Since(llmStart)
		if err != nil {
			o.logger.WarnCtx(ctx, "agentsys: llm call failed", map[string]any{
				"trace_id": traceID, "round": round,
				"latency_ms": llmLatency.Milliseconds(), "err": err.Error(),
			})
			return o.reportLLMFailure(ctx, s, err)
		}
		finish := "stop"
		if len(resp.ToolCalls) > 0 {
			finish = "tool_calls"
		} else if resp.Content == "" {
			finish = "empty"
		}
		o.logger.InfoCtx(ctx, "agentsys: llm call complete", map[string]any{
			"trace_id":          traceID,
			"round":             round,
			"prompt_tokens":     resp.Usage.PromptTokens,
			"completion_tokens": resp.Usage.CompletionTokens,
			"tool_call_count":   len(resp.ToolCalls),
			"content_len":       len(resp.Content),
			"finish":            finish,
			"latency_ms":        llmLatency.Milliseconds(),
		})

		// 记录 usage(best-effort;失败只 warn,不阻塞回复)
		o.recordUsage(ctx, s, resp.Usage)

		// 没有 tool_calls → 这是最终文本回复
		if len(resp.ToolCalls) == 0 {
			if resp.Content == "" {
				// LLM 什么都没说还不调 tool —— 视为 loop 结束(最后一轮可能空 content 也可能就结束)
				break
			}
			if _, perr := s.PostMessage(ctx, resp.Content, nil); perr != nil {
				// post 失败了,LLM 内容丢失 —— audit + 不 ACK 让上游重放
				return fmt.Errorf("post final message: %w", perr)
			}
			o.writeAuditOK(ctx, s, model.ActionPostMessage, 0, map[string]any{
				"round": round, "trace_id": traceID, "content_len": len(resp.Content),
			})
			o.logger.InfoCtx(ctx, "agentsys: post_message (final)", map[string]any{
				"trace_id": traceID, "round": round, "content_len": len(resp.Content),
			})
			posted = true
			break
		}

		// 有 tool_calls —— 把 assistant 的"我要调工具"消息保留到对话历史。
		// 必须带上 tool_calls **原样回灌**,后续 role=tool 消息的 tool_call_id
		// 才能对上,否则 provider 会报错"tool messages must follow a preceding
		// assistant message with tool_calls"。
		messages = append(messages, llm.Message{
			Role:      llm.RoleAssistant,
			Content:   resp.Content, // 可以为空字符串,JSON 序列化时保证 content 字段存在(llm.Message → azureChatMessage 已无 omitempty)
			ToolCalls: resp.ToolCalls,
		})

		// 执行每个 tool call,结果作为 role=tool 消息回喂
		for _, call := range resp.ToolCalls {
			toolStart := time.Now()
			result := agentsystools.Dispatch(ctx, s, call)
			toolLatency := time.Since(toolStart)
			resultSize := len(result)

			if agentsystools.IsErrorResult(result) {
				o.writeAuditErr(ctx, s, model.ActionToolError, map[string]any{
					"round": round, "tool": call.Name, "result": result,
					"trace_id": traceID,
				})
				o.logger.WarnCtx(ctx, "agentsys: tool dispatch failed", map[string]any{
					"trace_id":   traceID, "round": round, "tool": call.Name,
					"latency_ms": toolLatency.Milliseconds(), "result": truncate(result, 400),
				})
				// tool 失败时也要告诉 channel 用户发生了什么(实施决策:LLM tool 失败
				// 报错回 channel)。LLM 自己再决定是否继续 —— tool 结果告诉了它失败。
				//
				// 注意:如果 LLM 调的就是 post_message 然后失败了,这里不再补发
				// 一条"post_message 失败"以免死循环;依据 result 里的错误类型放行即可。
				if call.Name != agentsystools.ToolPostMessage {
					msg := fmt.Sprintf("执行工具 %s 时出错: %s", call.Name, extractErrorMessage(result))
					//sayso-lint:ignore err-swallow
					_, _ = s.PostMessage(ctx, msg, nil)
				}
			} else {
				// 成功 —— audit 分流:
				//   - post_message / create_task 写专属 action(带 target_id,保留语义)
				//   - 其它 tool(list_channel_members / list_recent_messages / list_channel_kb_refs)
				//     写通用 ActionToolOK,补齐可观测性(Layer 1 改动)
				switch call.Name {
				case agentsystools.ToolPostMessage:
					o.writeAuditOK(ctx, s, model.ActionPostMessage, 0, map[string]any{
						"round": round, "trace_id": traceID,
					})
					posted = true
				case agentsystools.ToolCreateTask:
					o.writeAuditOK(ctx, s, model.ActionCreateTask, 0, map[string]any{
						"round": round, "trace_id": traceID,
					})
				default:
					o.writeAuditOK(ctx, s, model.ActionToolOK, 0, map[string]any{
						"round":       round,
						"tool":        call.Name,
						"result_size": resultSize,
						"trace_id":    traceID,
						"summary":     summarizeToolResult(call.Name, result),
					})
				}
				o.logger.InfoCtx(ctx, "agentsys: tool dispatched", map[string]any{
					"trace_id":    traceID,
					"round":       round,
					"tool":        call.Name,
					"latency_ms":  toolLatency.Milliseconds(),
					"result_size": resultSize,
				})
			}
			messages = append(messages, llm.Message{
				Role:       llm.RoleTool,
				Content:    result,
				ToolCallID: call.ID,
			})
		}
	}

	if !posted {
		// 走完 MaxToolRounds 一句话没回(LLM 绕死在 tool 里,或者最后一轮空)
		// —— 兜底告诉用户
		//sayso-lint:ignore err-swallow
		_, _ = s.PostMessage(ctx, "我思考的轮数用完了,没办法给出明确答案,换种问法再试试?", nil)
		o.writeAuditErr(ctx, s, model.ActionLLMError, map[string]any{"reason": "max_tool_rounds_exceeded"})
	}
	return nil
}

// reportLLMFailure LLM 调用永久性失败(网络/认证/服务器):回 channel 错误消息
// + audit + 返回 nil(ACK,避免反复重试烧钱)。
func (o *Orchestrator) reportLLMFailure(ctx context.Context, s *scoped.ScopedServices, llmErr error) error {
	//sayso-lint:ignore err-swallow
	_, _ = s.PostMessage(ctx, "我暂时回不上来,请稍后再试。", nil)
	o.writeAuditErr(ctx, s, model.ActionLLMError, map[string]any{"err": llmErr.Error()})
	// 按实施决策:LLM 超时 → ACK 避免烧钱;返 nil
	return nil
}

// buildInitialPrompt 组最初的消息序列:system prompt + 历史消息。
//
// 成员信息走 tool(list_channel_members),不预载到 prompt —— 更可靠(LLM 不会幻觉
// 成员名)、更可观察(audit_events 能看到调用轨迹)、省 token。代价是派任务场景多
// 一轮 LLM 调用。
//
// 每条消息带 `mentions=...` 字段,LLM 看到谁被 @ 后,调 list_channel_members 拿名字。
func (o *Orchestrator) buildInitialPrompt(ctx context.Context, s *scoped.ScopedServices) ([]llm.Message, error) {
	// KB refs 提示(失败降级)
	kbRefsNote := ""
	if refs, err := s.ListChannelKBRefs(ctx); err == nil && len(refs) > 0 {
		kbRefsNote = fmt.Sprintf("\n\n当前 channel 挂载了 %d 份知识库引用。需要时可调用 list_channel_kb_refs 查看详情。", len(refs))
	}

	// 历史消息 + 每条的 mentions(让 LLM 知道谁 @ 了谁,再决定是否调 list_channel_members 查名字)
	// ListRecentMessages 底层按 id DESC 返(最新在前),这里倒着遍历,喂给 LLM 按"旧 → 新"阅读顺序。
	msgs, err := s.ListRecentMessages(ctx, RecentMessageWindow)
	if err != nil {
		return nil, err
	}
	var historyBuf strings.Builder
	historyBuf.WriteString("以下是当前 channel 最近的对话(从旧到新),请基于这些上下文回应最后一条被 @ 你的消息。\n")
	historyBuf.WriteString("每条消息格式:`[msg_id=X author_pid=Y mentions=Z1,Z2 reply_to=M] 正文`。\n")
	historyBuf.WriteString("- mentions 是被 @ 的 principal_id 列表;需要知道对应的人是谁,调 list_channel_members 工具。\n")
	historyBuf.WriteString("- reply_to 是本条消息引用的上一条消息 id(- 表示不是回复);据此还原 thread 结构。\n\n")
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		mentionStr := "-"
		if len(m.Mentions) > 0 {
			parts := make([]string, 0, len(m.Mentions))
			for _, pid := range m.Mentions {
				parts = append(parts, fmt.Sprintf("%d", pid))
			}
			mentionStr = strings.Join(parts, ",")
		}
		replyStr := "-"
		if m.Message.ReplyToMessageID != nil {
			replyStr = fmt.Sprintf("%d", *m.Message.ReplyToMessageID)
		}
		historyBuf.WriteString(fmt.Sprintf("[msg_id=%d author_pid=%d mentions=%s reply_to=%s] %s\n",
			m.Message.ID, m.Message.AuthorPrincipalID, mentionStr, replyStr, m.Message.Body))
	}

	return []llm.Message{
		{Role: llm.RoleSystem, Content: prompts.TopOrchestrator + kbRefsNote},
		{Role: llm.RoleUser, Content: historyBuf.String()},
	}, nil
}

// ─── 审计 + 计费辅助 ────────────────────────────────────────────────────────

func (o *Orchestrator) writeAuditOK(ctx context.Context, s *scoped.ScopedServices, action model.AuditEventAction, targetID uint64, detail map[string]any) {
	o.writeAudit(ctx, s, action, targetID, detail)
}

func (o *Orchestrator) writeAuditErr(ctx context.Context, s *scoped.ScopedServices, action model.AuditEventAction, detail map[string]any) {
	o.writeAudit(ctx, s, action, 0, detail)
}

func (o *Orchestrator) writeAudit(ctx context.Context, s *scoped.ScopedServices, action model.AuditEventAction, targetID uint64, detail map[string]any) {
	ev := &model.AuditEvent{
		ActorPrincipalID: s.ActorPrincipalID(),
		OperatingOrgID:   s.OperatingOrgID(),
		ChannelID:        s.ChannelID(),
		Action:           string(action),
		TargetID:         targetID,
		Detail:           repository.DetailJSON(detail),
		CreatedAt:        time.Now(),
	}
	if err := o.auditRepo.Insert(ctx, ev); err != nil {
		// 不让"审计写失败"挡住业务;log warn,继续。
		o.logger.WarnCtx(ctx, "agentsys: audit insert failed", map[string]any{
			"action": action, "err": err.Error(),
		})
	}
}

// recordUsage 写 llm_usage 表。pricing 未登记的模型 cost 记 0 + warn。
func (o *Orchestrator) recordUsage(ctx context.Context, s *scoped.ScopedServices, usage llm.Usage) {
	modelTag := o.llm.Model()
	cost, err := llm.EstimateCostUSD(modelTag, usage)
	if err != nil {
		o.logger.WarnCtx(ctx, "agentsys: missing price, cost=0", map[string]any{
			"model": modelTag, "err": err.Error(),
		})
		cost = 0
	}
	rec := &model.LLMUsage{
		OperatingOrgID:   s.OperatingOrgID(),
		ActorPrincipalID: s.ActorPrincipalID(),
		Model:            modelTag,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		CostUSD:          cost,
		ChannelID:        s.ChannelID(),
		CreatedAt:        time.Now(),
	}
	if err := o.usageRepo.Insert(ctx, rec); err != nil {
		o.logger.WarnCtx(ctx, "agentsys: usage insert failed", map[string]any{
			"err": err.Error(),
		})
		return
	}
	// 同时写一条 llm.call audit(便于按 actor 轨迹查审计)
	o.writeAuditOK(ctx, s, model.ActionLLMCall, 0, map[string]any{
		"model":             modelTag,
		"prompt_tokens":     usage.PromptTokens,
		"completion_tokens": usage.CompletionTokens,
		"cost_usd":          cost,
	})
}

// isOverBudget 查当天(UTC 零点起)的 SUM(cost_usd) 是否已达 cfg 上限。
func (o *Orchestrator) isOverBudget(ctx context.Context, orgID uint64) (bool, error) {
	now := time.Now().UTC()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	spent, err := o.usageRepo.SumCostSince(ctx, orgID, dayStart)
	if err != nil {
		return false, err
	}
	return spent >= o.cfg.DailyBudgetPerOrgUSD, nil
}

// handleBudgetExceeded 预算超限时:回 channel 一条消息 + 写 audit(action=skip.budget)。
func (o *Orchestrator) handleBudgetExceeded(ctx context.Context, orgID, channelID uint64) {
	// 这里需要一个 scoped 实例去 post,直接现场构造即可(不进 LLM,没 tool-loop)
	s := scoped.New(orgID, channelID, o.topOrchestratorPID, o.scopedDeps)
	//sayso-lint:ignore err-swallow
	_, _ = s.PostMessage(ctx, "今日本组织的 LLM 预算已用完,我先休息一下,明天继续。", nil)
	o.writeAuditErr(ctx, s, model.ActionSkipBudget, map[string]any{"reason": "daily_budget_exceeded"})
}

// ─── 小工具 ────────────────────────────────────────────────────────────────

// newTraceID 生成一次 handleMention 的 trace id。8 字节 hex(16 字符)够本地 debug +
// SLS 查询匹配;不做分布式全局唯一,跨进程场景未来加 org/channel 前缀即可。
func newTraceID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 极端情况退化为纯时间戳,也够单机去重
		return fmt.Sprintf("t%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// truncate 截断超长字符串,给日志字段控体积用(SLS 单字段通常 ≤ 1KB 才高效索引)。
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...[truncated]"
}

// summarizeToolResult 为每个成功的 tool 结果产出一句话摘要,写入 audit 的 detail.summary。
// 不存完整 result(隐私 + 体积),只存"这次调了 list_channel_members 返回 3 人"这种维度。
// result 是 tools.Dispatch 返回的 JSON 字符串,形如 `{"ok":true,"data":{...}}`。
func summarizeToolResult(toolName, result string) string {
	var payload struct {
		OK   bool            `json:"ok"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil || !payload.OK {
		return ""
	}
	switch toolName {
	case agentsystools.ToolListChannelMembers:
		var d struct {
			Members []json.RawMessage `json:"members"`
		}
		//sayso-lint:ignore err-swallow
		_ = json.Unmarshal(payload.Data, &d)
		return fmt.Sprintf("%d members", len(d.Members))
	case agentsystools.ToolListRecentMessages:
		var d struct {
			Messages []json.RawMessage `json:"messages"`
		}
		//sayso-lint:ignore err-swallow
		_ = json.Unmarshal(payload.Data, &d)
		return fmt.Sprintf("%d messages", len(d.Messages))
	case agentsystools.ToolListChannelKBRefs:
		var d struct {
			KBRefs []json.RawMessage `json:"kb_refs"`
		}
		//sayso-lint:ignore err-swallow
		_ = json.Unmarshal(payload.Data, &d)
		return fmt.Sprintf("%d kb_refs", len(d.KBRefs))
	}
	return ""
}

// extractErrorMessage 从 tools.Dispatch 返回的错误 JSON 里抽出人读的 error 字段。
// 失败降级为整段原文(最多 200 字)。
func extractErrorMessage(raw string) string {
	var probe struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &probe); err == nil && probe.Error != "" {
		return probe.Error
	}
	if len(raw) > 200 {
		return raw[:200] + "..."
	}
	return raw
}
