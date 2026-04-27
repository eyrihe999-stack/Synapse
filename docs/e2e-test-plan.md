# Synapse 端到端测试计划

## 目的

把协作链路(project / channel / message / task / mention / 顶级 agent / MCP)从 UI 到 DB 再到 Redis stream 一次打通验证,每条用例覆盖:
- 用户操作
- 前端 API + 后端代码路径
- DB 数据变化(SQL 可复制验证)
- Redis 事件 / consumer group 变化
- audit_events / llm_usage 变化(证明 orchestrator 是否调 LLM)
- UI 可见效果

## 状态约定

- ✅ **过**:验证完成,符合预期
- 🟡 **进行中**:已给用例描述,等验证
- ⏳ **待测**:还没到
- 🔴 **阻塞**:遇到 bug 需先修
- 🧭 **已跳过**:有意不测(标注原因)

## 使用说明

逐条按序做。遇到不符合预期的立即停下,不要继续往下 —— 后面用例依赖前面用例的数据状态。

---

## 环境基线(测试开始时)

### 容器
- backend:`localhost:5001/synapse:<TAG>`(端口 8080)
- web:`localhost:5001/synapse-web:<TAG>`(端口 3080)
- 基础设施:MySQL 13306 / Redis 6379 / Postgres 15432

### 账号 / principal 对照表(plan B 清理 + 孤儿 principal 清完后)

| principal_id | 主体 | 备注 |
|---|---|---|
| 1 | user `hechenyang@lunalabs.cn`(Eyri He) | Skynet owner |
| 2 | user `eyrihe998@gmail.com`(刘华强) | |
| 3 | user `2679793215@qq.com`(郝哥-瓜摊老板) | |
| 4 | user `849814765@qq.com`(xym) | |
| 7 | agent `synapse-top-orchestrator`(Synapse) | 全局系统 agent,必保 |
| 12 | agent `Eyri He 的 Claude`(agent_id=agt_z4hPYfoXlXhmu14OOYX4xw) | user_id=1 的个人 agent |

业务数据全清零(projects / channels / tasks / messages / audit_events / llm_usage / oauth_* 均为 0),Redis streams 也被 DEL 过。

### 常用验证命令

```bash
# 1. 查 DB(最常用)
dbq() {
  docker exec synapse-mysql mysql -uroot -p123456 \
    --default-character-set=utf8mb4 synapse -e "$1" 2>&1 | grep -v Warning
}

# 2. 数值 count 三连
dbcount() {
  docker exec synapse-mysql mysql -uroot -p123456 synapse -Nse "
  SELECT 'channel_messages', COUNT(*) FROM channel_messages
  UNION ALL SELECT 'channel_message_mentions', COUNT(*) FROM channel_message_mentions
  UNION ALL SELECT 'audit_events', COUNT(*) FROM audit_events
  UNION ALL SELECT 'llm_usage', COUNT(*) FROM llm_usage;"
}

# 3. Redis stream
redc() { docker exec redis redis-cli "$@"; }
# XLEN / XINFO GROUPS stream / XPENDING stream group

# 4. 后端日志
blog() {
  docker logs synapse-synapse-1 --since "${2:-2m}" 2>&1 \
    | grep -iE "$1" | tail -30
}
```

### 判定"orchestrator 是否调 LLM"的金标准

不看 Redis `Last Delivered ID`(每条消息 orchestrator 都会拉去看一眼,这个 ID 一定前进)—— **看 `audit_events` / `llm_usage` 表的 count 是否增加**。

---

## 测试用例

### 阶段 A:项目 & Channel 骨架

#### A1 ✅ 新建项目

**数据流**:前端 `ProjectsPage.handleCreate` → `POST /api/v2/projects` → `project_service.Create`(校验 org 成员)→ `repo.CreateProject` → INSERT projects。**不发 stream 事件**。

**验证 SQL**:
```sql
SELECT id, org_id, name, description, created_by, created_at, archived_at FROM projects;
```
预期 1 行:`demo-proj` / `org_id=1` / `archived_at=NULL`。

#### A2 ✅ 新建 channel(auto-include Synapse)

**看点**:创建 channel 同事务自动把"auto_include_in_new_channels=TRUE 的 agent" 加为成员。SQL 使用 sentinel 护栏 `(org_id=0 OR org_id=?)`,全局 Synapse(org_id=0)被命中。

**代码位置**:`channel/service/channel_service.go:Create` → `autoIncludeAgents`(`:251`)+ `channel/repository/kb_ref.go:LookupAutoIncludeAgentPrincipals`(`:65`)。

**验证 SQL**:
```sql
-- channel
SELECT id, org_id, project_id, name, purpose, status, created_by FROM channels;
-- members(应 2 行:你 owner + Synapse member)
SELECT cm.channel_id, cm.principal_id, cm.role,
       COALESCE(u.display_name, a.display_name) AS name,
       CASE WHEN u.id IS NOT NULL THEN 'user'
            WHEN a.id IS NOT NULL THEN CONCAT('agent_', a.kind) ELSE '?' END AS kind
FROM channel_members cm
LEFT JOIN users u ON u.principal_id = cm.principal_id
LEFT JOIN agents a ON a.principal_id = cm.principal_id
ORDER BY cm.joined_at;
```
Redis `XLEN synapse:channel:events` 不变(创建 channel 不发事件)。

#### A3 ✅ 加 / 改 / 删 channel 成员

**三个子操作**:
1. 加:`POST /channels/:id/members { principal_id, role }` → `member_service.Add` 校验 owner + target principal 属于 channel 所在 org
2. 改角色:`PATCH /channels/:id/members/:pid/role` → `UpdateRole` 有"最后一个 owner 不能降级"守卫
3. 删:`DELETE /channels/:id/members/:pid` → `Remove` 同守卫(不能删最后 owner)

**MVP 约束**:observer 和 member 后端权限**完全相同**(`message_service.go:22` 注释),observer 只是社交标签。

Redis 无事件(成员变更不发 stream)。

#### A4 ✅ 挂载知识源(KB ref)到 channel

**代码路径**:`channel/service/kb_ref_service.go:Add` → `channel_kb_refs` INSERT。`kb_source_id` 和 `kb_document_id` 二选一;必须属于 channel 所在 org。

**挂载后的语义**:`orchestrator.handler.buildInitialPrompt` 会拼一句"当前 channel 挂载了 N 份知识库引用"进 system prompt,LLM 可调 `list_channel_kb_refs` tool 看详情。

**验证 SQL**:
```sql
SELECT r.id, r.channel_id, r.kb_source_id, r.added_by, r.added_at, s.name
FROM channel_kb_refs r
LEFT JOIN sources s ON s.id = r.kb_source_id
WHERE r.channel_id = 1;
```
Redis 不发事件。

**注**:实际执行时遇到了"@Synapse 问资料不回复"的现象,已定位到**不是挂载 bug**,而是 `@` 没从 MentionPicker 选 → mentions 数组空 → orchestrator 跳过。修复见文末"已知问题"。

---

### 阶段 B:普通消息

#### B1 ✅ 发不 @ 任何人的纯文本消息

**关键**:第一次把事件推进 Redis stream,orchestrator 消费但 ACK 跳过(不调 LLM)。

**代码流**:
1. `message_service.postCore` 事务:INSERT channel_messages + AddMessageMentions([])
2. 事务后 `publishMessagePosted` XADD `synapse:channel:events`,fields 里**无** `mentioned_principal_ids`
3. `orchestrator.handleEvent`:parseMentionCSV("")=[],containsPID([],7)=false → return nil ACK

**验证**:
- `channel_messages` +1,`channel_message_mentions` +0
- Redis XLEN +1,Pending(top-orchestrator)回到 0
- **`audit_events` / `llm_usage` count 不变** ← 金标准

#### B2 ✅ @ 普通用户成员(非 agent)

**前置**:把另一个 user(如 `hechenyang@lunalabs.cn`)加进 channel。

**关键差别于 B1**:事件 fields **有** `mentioned_principal_ids=<user pid>`;但 orchestrator 看到不含 principal_id=7 → 仍 ACK 跳过;无服务端推送 → 被 @ 的 user/agent **不会被主动通知**,自己下次打开对话走 pull 才能感知。但 DB 里 `channel_message_mentions` 会留痕。

**前端**:`handleSend` 里 `effectiveMentions.length > 0` → `startPolling()` 启动 20s 轮询。前端显示"等待 agent 回复..."20 秒,最终 timeout 收起。

**验证**:
- `channel_messages` +1,`channel_message_mentions` +1(principal_id=P)
- Redis stream 事件 fields 有 `mentioned_principal_ids="P"`
- `audit_events` / `llm_usage` 不变
- UI 等待 20s 蓝点消失,无新气泡

---

### 阶段 C:@Synapse 单轮响应

#### C1 ✅ @Synapse 简单问候

**看点**:第一次看到 orchestrator 真走 LLM,全链路活动:
- `audit_events` 出现 `llm.call` + `post_message`
- `llm_usage` +1 行,带 token 数 + cost
- UI 出现 Synapse 的渐变气泡(蓝紫色,bot 图标,`全局` 徽章)

**代码流**:
1. orchestrator 判定 containsPID=true → 进 handler
2. 预算检查(当前 `daily_budget_per_org_usd=0` 代表不限,跳过)
3. 构造 `scoped.ScopedServices`(绑 orgID/channelID/actorPID=7)
4. `buildInitialPrompt`:拼 system prompt + KB refs 提示 + 最近 20 条历史消息
5. LLM 调用(gpt-5.4@azure,`max_completion_tokens=2048`,Temperature 走默认 1.0)
6. LLM 返 content(无 tool_calls)→ `scoped.PostMessage(content)` → INSERT channel_messages(author_principal_id=7)
7. 写 audit `llm.call` + `post_message`

**验证**:
```sql
SELECT id, action, target_id, JSON_EXTRACT(detail,'$.prompt_tokens') AS p,
       JSON_EXTRACT(detail,'$.completion_tokens') AS c,
       JSON_EXTRACT(detail,'$.cost_usd') AS cost, created_at
FROM audit_events ORDER BY id DESC LIMIT 5;

SELECT id, prompt_tokens, completion_tokens, cost_usd FROM llm_usage ORDER BY id DESC LIMIT 3;
```

Redis:事件 fields 含 `mentioned_principal_ids="7"`;orchestrator 回复的那条 post_message 又会 XADD 一条(递归,但**没人 @ Synapse 自己**,下一轮直接 ACK)。

#### C2 ✅ @Synapse "这个 channel 有谁"(触发 list_channel_members tool)

**看点**:**多轮 tool-loop**:
- 第 1 轮 LLM 返 tool_calls=[list_channel_members]
- handler 调 `tools.Dispatch` → `scoped.ListMembersWithProfile` → raw SQL JOIN users/agents
- 第 2 轮 LLM 拿 tool result 回答"当前 channel 有 X 人:Y, Z..."+ post_message

**验证**:`audit_events` 连续出现多条(audit 模型里每轮 llm.call 都记一条),且 action=post_message 只有最后一条。

---

### 阶段 D:@Synapse 多轮 tool-loop(派任务)

#### D1 ✅ @Synapse 派任务给 channel 内的某人(代派语义)

**流程**:
1. 你发 `@Synapse 帮我派个任务给 @郝哥,写 PRD 初稿`
2. orchestrator 第 1 轮:LLM 返 tool_calls=[list_channel_members]
3. 第 2 轮:LLM 基于成员名册返 create_task(assignee_principal_id=<郝哥 pid>)
4. 第 3 轮:LLM 返 post_message "已派给 郝哥"
5. DB:`tasks` 新增 1 行,`audit_events` ≥4 条 llm.call + 1 条 post_message + 1 条 create_task + 1 条 tool.ok

**代派语义**(2026-04-24 改造后):
- `created_by_principal_id` = **发起人**(那条 @Synapse 消息的作者),不是 Synapse 自己
- `created_via_principal_id` = **代派 agent**(Synapse 的 pid=7);手动创建 = 0
- `task_reviewers` 空时 service 自动 fallback = `[发起人]`;`required_approvals` 自动 = len(reviewers)
- tool schema 已调整 description,LLM 不再主动传 `required_approvals=1`

**验证 SQL**:
```sql
SELECT id, channel_id, title, status, assignee_principal_id,
       created_by_principal_id, created_via_principal_id, required_approvals
FROM tasks;

SELECT tr.task_id, tr.principal_id, COALESCE(u.display_name,a.display_name) AS reviewer
FROM task_reviewers tr
LEFT JOIN users u ON u.principal_id=tr.principal_id
LEFT JOIN agents a ON a.principal_id=tr.principal_id;

SELECT action, target_id FROM audit_events
WHERE channel_id=1 AND created_at > NOW() - INTERVAL 5 MINUTE
ORDER BY id;
```

---

### 阶段 E:任务生命周期

#### E1 ✅ 无审批任务(create → submit → approved)

1. UI 创建任务:assignee=你,reviewer 留空 → DB `required_approvals=0`
2. 自己 claim(open → in_progress)
3. 自己 submit → **因 required_approvals=0,状态直接跳 approved + closed_at=now**(service.go submit 分支)
4. task 生命周期结束

**验证**:`tasks.status='approved'`,`submitted_at=closed_at`(同秒),`task_submissions` 有 1 行,`task_reviewers` / `task_reviews` 为空,`created_via_principal_id=0`(手动 vs 代派)。

#### E2 ✅ 有审批任务(完整闭环)

**实际走的是代派路径**(D1 的 task id=2):发起人=你,代派=Synapse,assignee=郝哥,reviewer fallback=你,required=1。

1. 郝哥 claim(open → in_progress),Redis `synapse:task:events` 多一条 `task.claimed`
2. 郝哥 submit markdown 产物 → 进 submitted 态,OSS 落 `synapse/1/tasks/2/...md`
3. 你(reviewer)进任务详情页,选 approved → `task_reviews` +1,`task.status=approved`,`closed_at` 落盘

**验证**:
```sql
SELECT status, submitted_at, closed_at FROM tasks WHERE id=<id>;
SELECT decision, reviewer_principal_id FROM task_reviews WHERE task_id=<id>;
```
Redis:`XLEN synapse:task:events` 增加 3(claimed / submitted / reviewed)。

#### E3 ✅ request_changes 路径

同 E2,reviewer 选 "request_changes" → task 状态变 `revision_requested`,assignee 二次 submit 生成新 submission → reviewer 再 approve。

**关键点(本次发现)**:
- **submit 允许 `in_progress` 或 `revision_requested` 作为入口态**(service.go 两个 submit 都改过);之前只允许 in_progress,revision_requested 无法二次提交
- **approval 按 submission 计数,不按 task 累加**:打回后新 submission 的同意数从 0 重算(`CountApprovalsForSubmission` 只看当前 submission_id),语义上"新版本 = 重新审",合理
- 前端 `latestSubmission = submissions[0]` —— 后端 `ORDER BY id DESC` 返回,"最新"在第 0 项,不是 length-1

**验证**:`task_submissions` 2 行(两次提交版本),`task_reviews` 2 行(submission=旧 decision=request_changes / submission=新 decision=approved)。

#### E4 ✅ 取消任务

open / in_progress / revision_requested 下点 "取消任务" → status=cancelled,closed_at=now。终态之后不能取消。

**验证**:Redis `task.cancelled` 事件;`tasks.closed_at` 落盘;再 cancel 一次应被 `ErrTaskStateTransition` 拒。

---

### 阶段 F:方案 A(任务变更)

#### F1 ✅ 改执行人

**权限**:task 创建人 或 channel owner。
**状态闸**:非终态可改(`approved/rejected/cancelled` 拒)。
**副作用**:清空 assignee 且当前 in_progress → 自动回 open 态。

**代码**:`task/service.UpdateAssignee`,HTTP `PATCH /v2/tasks/:id/assignee { assignee_principal_id }`。

**验证**:`tasks.assignee_principal_id` 变化,Redis `task.assignee_changed` 事件带 `new_assignee_principal_id`。

#### F2 ✅ 改审批人

**权限**:同 F1。
**状态闸**:**只在 open / in_progress 允许**(submitted / revision_requested 拒,避免和在飞审批流冲突)。
**语义**:`task_reviewers` 表全量替换,`task_reviews` 历史保留但当前判定只按新 reviewer 列表。`required_approvals` 自动 clamp 到 [0, len]。

**代码**:`task/service.UpdateReviewers`,HTTP `PATCH /v2/tasks/:id/reviewers { reviewer_principal_ids, required_approvals }`。

**验证**:`task_reviewers` 行数 = 新传入列表 len;`tasks.required_approvals` clamp 到合法范围;Redis `task.reviewers_changed` 事件带 `new_reviewer_count / new_required_approvals`。

---

### 阶段 G:MCP + Claude Desktop

#### G1 ✅ OAuth DCR + consent + 自动创建 agent

从 Claude Desktop 端添加 Custom Connector → 触发 DCR → 跳转 consent 页 → 用户同意 → 后端 `oauthAgentBootstrapperImpl.CreateUserAgent` 查重后创建或复用 agent。

**验证**:
- `oauth_clients` +1(每次 DCR 新建 client 记录)
- `agents` 新增 1 行,display_name="Eyri He 的 Claude"(若同名已存在,查重命中复用)
- `oauth_access_tokens` live +1

**已知限制**:Claude Desktop / Claude Web 自报 `client_name` 都是 "Claude",会查重共用同一个 agent(见已知问题)。

#### G2 ✅ MCP list_my_tasks 能看到被指派任务

在 Claude 里:"列一下我的任务"。MCP tool `list_my_tasks` 调用,返回该 agent 为 assignee 的任务。

**验证**:Synapse 代派任务给 Claude(assignee_principal_id=13)后,Claude Desktop 能读到;代派语义同时生效:`created_by=发起人 / created_via=Synapse / reviewer fallback=发起人 / required=1`。

**代码**:`mcp/tool_task.go:handleListMyTasks` → `TaskService.ListByAssigneePrincipal`。

#### G3 ✅ MCP post_message / create_task

从 Claude 端主动在 channel 发消息 / 派任务。

**验证**:
- post_message:`channel_messages` 新增 `author_principal_id=<Claude pid>` ✅
- create_task:`created_by=<Claude pid> / created_via=0 / required_approvals=0`(**MCP 路径走手动分支**,Initiator=0 不走代派,符合 MVP 设计)

**发现(小 UX 瑕疵,不阻塞)**:Claude 第一次调 create_task 带了 `required_approvals=1`,后端正确拒 `ErrRequiredApprovalsRange`;Claude 读错误后重试传 0 成功。可考虑把 `internal/mcp/tool_task.go` 的 create_task tool description 和 agentsys 那边对齐(说明"留空走后端默认"),让首次就传对。

#### G4 ✅ 删除 agent 级联清理

UI `/org/agents` 页点删除按钮 → `AgentService.Delete` → `repo.Delete` 事务清理:
- `oauth_access_tokens` / `oauth_refresh_tokens`(按 principal_id)全部 revoke
- `channel_members` 里相关行删除
- `task_reviewers` 里相关行删除
- 活跃 `tasks.assignee_principal_id` 清零 + status 保持(非终态不动;open 仍 open)
- 最后删 agents 行

**验证**:
- `agents` id=9 消失,`oauth_access_tokens` live=0,`channel_members` pid=13 行=0,`task_reviewers` pid=13 行=0
- `tasks` id=8 (之前 assignee=13) `assignee_principal_id` 变 0,status 仍 open ✅
- Claude Desktop 再调 MCP → 401 "Authentication required to use this tool" ✅(截图验证)

---

### 阶段 H:边界 & 隔离

#### H1 ✅ 归档 channel 后发消息被拒
归档后 `message_service.postCore` 里 `c.Status == 'archived'` 检查 → ErrChannelArchived 403。UI 也同步隐藏 composer。

#### H2 ✅ 归档项目后不能建 channel
`channel_service.Create` 里 `p.ArchivedAt != nil` → ErrProjectArchived。**UI 级联**:ChannelsPage 对已归档项目隐藏"新建 channel"按钮。

**额外修复**:原来 `projectService.Archive` 只改 project 自身,下属 open channel 仍可发消息,语义裂开。现改为事务内级联 UPDATE(见已知问题日志)。前端 ChannelsPage 加"显示已归档"toggle 一并控制项目和 channel 可见性。

#### H3 ⏳ 跨 org 隔离(需要第二个 org,可选)
创建第二个 org + channel,在其中 @ Synapse,确认 Synapse 回答内容**完全不包含第一个 org 的 channel / task 信息**。

静态断言(已在单测):`scoped_test.go:TestScopedServices_NoCrossOrgParams` —— ScopedServices 的 public API 物理上不接受 orgID/channelID 参数,跨 org 不可能。

---

## 已知问题与修复日志

按时间倒序(最新在上)。

### 归档 project 不级联下属 channel + ChannelsPage 看不到归档(2026-04-24)
- **症状**:归档项目后下属 channel 仍 open 可继续发消息;ChannelsPage 也看不到已归档项目/channel,无法检查历史
- **根因**:`projectService.Archive` 只 UPDATE project 自己,不 touch channels;ChannelsPage 写死过滤 `!archived_at && status==='open'`
- **修复**:
  1. 后端 repo 加 `ArchiveOpenChannelsByProject(ctx, projectID, now) (int64, error)` 原子 UPDATE;`Archive` 改用事务,先归档 project 再级联 open channel,日志输出 `cascaded_channel_count`
  2. 前端 ChannelsPage 加"显示已归档"toggle,默认关;打开后一并显示 archived 项目 + archived channel;归档项目的"新建 channel"按钮隐藏
- **状态**:✅ 已修 + 已部署,p2+c2 级联归档验证同秒同事务

### submit 不允许 revision_requested 入口 + 前端 latestSubmission 取错位(2026-04-24)
- **症状 1**:E3 中 reviewer 打回后,assignee 再次 submit 报 "state transition not allowed"
- **根因 1**:`Submit` / `SubmitByPrincipal` 的 guard 只允许 `in_progress`,没覆盖 `revision_requested`
- **修复 1**:guard 放宽到 `{in_progress, revision_requested}`,两处对齐;二次 submit 刷新 submitted_at 并把 status 回写为 submitted
- **症状 2**:二次 submit 后 reviewer approve 报 "reviewer already decided"
- **根因 2**:后端 `ListSubmissions` 返回 `ORDER BY id DESC`(最新在前),但前端 `TaskDetailPage.latestSubmission` 取 `submissions[length-1]`(拿到最旧那条),approve 发到旧 submission 上撞 `uk_task_reviews_submission_reviewer` 唯一键
- **修复 2**:前端改 `submissions[0]`,顺带注释说明后端 DESC 约定
- **状态**:✅ 已修 + 已部署,E3 闭环通过

### `/org/*` 路由未选组织也展示内容 + channel 子 tab 无刷新按钮(2026-04-24)
- **症状 1**:未选组织直接访问 `/org/tasks/5` 等深链,页面仍渲染(后端按 principal 校验兜底,但 UI 闪烁 + 可能露出缓存状态)
- **修复 1**:新增 `RequireOrg` 守卫组件,套在所有 `/org/*` 路由外层;首次 mount 触发 fetchOrgs,无 currentOrg 时 Navigate 到 `/user`。将来新加 /org 子页自动被守卫覆盖
- **症状 2**:channel 子 tab(messages / members / kb / tasks)都没有显式"刷新"入口,只能切换 tab 或硬刷页面
- **修复 2**:4 个 tab 各加 ghost 图标按钮;MessagesTab 的刷新 icon 在 loading 时旋转;其它 tab 用已有 fetch 回调
- **状态**:✅ 已修

### GORM `default:1` tag 让 required_approvals=0 被 DB 覆盖为 1(2026-04-24)
- **症状**:E1 测试 UI 不选 reviewer 建任务,后端 service 算出 `required=0`,但 INSERT 后 DB 存成 1,task 卡在 submitted 态
- **根因**:Task model `RequiredApprovals int` 配 `gorm:"default:1"`。GORM 对非指针 int 的 zero value + 声明 default 的字段,INSERT 时把值省略让 DB 填 default,结果 service 层的 0 无法落库
- **修复**:去掉 `default:1` tag,model 层注释明确"service 是唯一真相源";AutoMigrate 自动把 DB 端 `DEFAULT 1` 也抹掉
- **状态**:✅ 已修 + 已部署,E1 重测通过

### 代派任务语义改造 + reviewer fallback(2026-04-24)
- **症状**:D1 第一次测,Synapse 代派的 task `required_approvals=1` 但 `task_reviewers` 为空,task 卡死无人能审批
- **根因 1**:tool description 写 `required_approvals: 0 / 缺省 = 1`,LLM 主动传 1 但没给 reviewer
- **根因 2**:task 语义错位 —— `created_by` 记的是 Synapse(实际 INSERT 执行者),不是意图发起人;reviewer 也没 fallback
- **改造**:
  1. Task model 加 `created_via_principal_id`(非 0 = 代派 agent);`created_by` 改记发起人
  2. `CreateByPrincipalInput` 加 `InitiatorPrincipalID`;代派分支里自动 `created_by=Initiator / created_via=Creator`,reviewer 空 fallback = `[Initiator]`,required clamp 不报错
  3. `ScopedServices.CreateTask` 透传 `triggerAuthorPID` 作为 Initiator
  4. tool description 改成"reviewer 留空 → fallback 发起人;required 建议不传"
- **状态**:✅ 已修 + 已部署(镜像 tag `20260424141056-6822221-dirty`)+ D1 重测通过 + E2 闭环通过

### IME 中文输入法回车误发(2026-04-24)
- **症状**:中文输入法打英文字母时按回车,半句话被发出
- **根因**:`MessagesTab.handleKeyDown` 没检查 `e.nativeEvent.isComposing`
- **修复**:加 composition 守卫,IME 期间的 Enter 不拦截
- **状态**:✅ 已修,**待前端部署**

### @ 文字但没从 MentionPicker 选时 mention 不生效(2026-04-24)
- **症状**:用户打 `@Synapse` 但没从 dropdown 点选 → mentions 数组为空 → orchestrator 跳过
- **根因**:PR #4' 设计"后端只信 mentions 数组不做文本解析"是严格但不友好
- **修复**:前端 `handleSend` 前扫 body 里 `@DisplayName` 自动补齐 mentions(按 displayName 长度排序避免前缀冲突)
- **状态**:✅ 已修,**待前端部署**

### 同一 user 重复创建多个同名 Claude agent(2026-04-24)
- **症状**:Claude Desktop 每次重连 DCR 生成新 client_id,OAuth consent 无脑 create 新 agent → `agents` 表里同一用户多行"Eyri He 的 Claude"
- **根因**:`oauthAgentBootstrapperImpl.CreateUserAgent` 没查重
- **修复**:加 `FindByOwnerAndDisplayName` repo 方法 + service 暴露 `FindUserAgentByDisplayName` + bootstrap 查重命中则复用
- **状态**:✅ 已修 + 清 4 个僵尸 + 清 6 个孤儿 principal,**后端已部署**
- **已知遗留**:Claude Desktop 和 Claude Web 两个客户端自报 client_name 都是 "Claude" → 共用一个 agent。用户接受了当前设计(不是 bug,是取舍),未来若要分开需要加 `client_fingerprint` 字段

### 首次 deploy 后 LLM 报 `max_tokens not supported`(2026-04-24)
- **根因**:gpt-5 / o1 系列新模型 API 弃用 `max_tokens`,改 `max_completion_tokens`
- **修复**:`llm/azure.go` 改字段名;`handler.go` 删掉硬编码 Temperature=0.3(新模型只接受默认 1.0)
- **状态**:✅ 已修 + 已部署

### 第二轮 LLM 报 `content expected string, got null`(2026-04-24)
- **根因**:`azureChatMessage.Content` tag 是 `json:"content,omitempty"`,assistant 消息 content="" 时被 omit;OpenAI 校验缺字段=null 报错
- **连带**:tool-loop 回灌 assistant 时没带 `tool_calls`,后续 tool result 消息非法
- **修复**:去 omitempty;`llm.Message` 加 `ToolCalls` 字段;handler 回灌时带上
- **状态**:✅ 已修 + 已部署

### 后端 TaskStatus 常量和前端枚举不一致(2026-04-24)
- **症状**:前端用 `canceled / changes_requested / assigned`,后端用 `cancelled / revision_requested`(无 assigned 态)
- **修复**:前端 `types/api.ts` / `taskMeta.ts` / `TasksPage` / `TasksTab` / `TaskDetailPage` 五处对齐
- **状态**:✅ 已修 + 已部署

### required_approvals out of range(2026-04-24)
- **症状**:创建任务不选 reviewer 报错
- **根因**:旧逻辑 `required_approvals<=0 → 1`,但空 reviewer → 1 > len(reviewers)=0 → 422
- **修复**:空 reviewer 列表时 required=0;submit 代码里加 "required=0 时 submit 直接 approved + closed_at=now" 分支
- **状态**:✅ 已修 + 已部署

### Task 详情页空白(2026-04-24)
- **症状**:点任何 task 跳转后页面空
- **根因**:后端返回 `submissions=null`(Go nil slice 序列化);前端 `.map(null)` 崩
- **修复**:前端加 `?? []` 兜底
- **状态**:✅ 已修 + 已部署

### Synapse 被 @ 但不响应(MVP 预期行为)(2026-04-24)
- **症状**:你 @ Claude(不是 Synapse)让 Synapse 代转,没反应
- **根因**:不是 bug,是 orchestrator 只响应 `mentions` 含 `principal_id=7`。"@ 非 Synapse 但希望它代办"路径当前不做(原规划在 PR #7',已下线)
- **解法**:明确 @ Synapse + 请求转达,例如 `@Synapse 帮我告诉 @Claude ...`

### Claude Desktop 没感知到新任务(MVP 预期行为)
- **症状**:UI 派任务给 Claude,Claude Desktop 不知道
- **根因**:MCP 不推送(原规划在 PR #7';2026-04-24 实测后下线,因为 Claude Desktop 每 turn 重建 transport,推送窗口过短,效果 ≈ pull),Claude 只能在用户下次对话时调 `list_my_tasks` 自然看到
- **临时改进**:MCP tool description 明确告诉 LLM "不要用缓存,每次涉及任务都重新调 list_my_tasks"(已修 + 已部署)
- **长期**:等生态出现持久 GET 的 client(Cursor/Codex/自建 agent)或做 Synapse-Web 独立 WebSocket 通道时再议

### Docker 容器 purpose 中文乱码(2026-04-24)
- **症状**:mcp-smoke channel 的 purpose 显示 `PR#5 MCP ç«¯åˆ°ç«¯æµ‹è¯•`
- **根因**:旧 MCP 工具链把 UTF-8 字节当 Latin-1 再次 UTF-8 编码(双重编码)写入
- **修复**:SQL 直接 UPDATE 修正该行;新写入链路无此问题
- **状态**:✅ 已修(DB 直改)

---

## 下一步

**E2E 验收 round 1 已全通**(2026-04-24 🎉):

A(项目/Channel 骨架) → B(普通消息) → C(@Synapse 单轮) → D(Synapse 代派多轮 tool loop) → E1-E4(任务全生命周期) → F1-F2(执行人/审批人变更) → G1-G4(MCP + Claude Desktop 全链路) → H1-H2(归档边界)全部 ✅。

**剩余可选**:
- H3(跨 org 隔离):已有单测 `TestScopedServices_NoCrossOrgParams` 物理断言 ScopedServices public API 无法接受 orgID/channelID,跨 org 不可能泄漏;UI 端对应守卫 `RequireOrg` 已补。真实多 org 压测可二期做

### 本轮附带修复 / 增强

E2E 过程中识别并落地的 8 个 bug + 3 个 UX 增强(都已在"已知问题与修复日志"详列):

- 代派任务语义改造(`created_by` = 发起人 / `created_via` = 代派 agent / reviewer fallback 发起人)
- GORM `default:1` tag 让 required_approvals=0 被 DB 覆盖 → 去 default
- submit 允许 `revision_requested` 作为入口态
- 前端 `latestSubmission` 后端 DESC 下取错位 → 改 `[0]`
- 路由未选组织展示内容 → 新增 `RequireOrg` 守卫
- channel 子 tab 无刷新按钮 → 4 个 tab 加 ghost 刷新
- 归档 project 不级联下属 channel + ChannelsPage 看不到归档 → service 层事务级联 + 前端"显示已归档"toggle
- 归档 channel 中文 purpose 乱码、IME 回车误发、@ 文字无 picker mention 不生效 等前期前端修复

### 下一步:PR #11' system_event 协作时间线

用户发现:MCP/手动创建 task 后 channel 里静默,只有切 Tasks tab 才知道。决议引入 `kind=system_event` 消息卡片,覆盖 task 生命周期 7 个事件 + channel member 3 个 + kb_ref 2 个 + channel 自身 2-3 个,共计 14-15 个 hook 点。架构走 Redis Streams consumer(B 方案),service 层不直接调 message,保持解耦。

完整设计见 [`collaboration-roadmap.md` PR #11'](collaboration-roadmap.md#pr-11channel-协作时间线--system_event-消息卡片)。
