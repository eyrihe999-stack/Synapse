# Synapse 协作架构设计

> **方向**:Channel 协作 + MCP 多 agent 接入。Synapse 是一套让"人 + 个人 agent + 系统 agent"在 channel 里协作的基础设施,不是有自己 UI 的业务系统。
>
> **实施推进**见 [`collaboration-roadmap.md`](collaboration-roadmap.md)。

---

## 1. 产品形态

**Synapse 不是一个有自己 UI 的业务系统,而是一套让"人 + 每个人的本地 agent + 系统 agent"协作的基础设施**。用户在自己电脑上跑 Claude Desktop / Cursor / Codex 等 agent 客户端,通过 **MCP 协议**连到 Synapse;Synapse 在服务端暴露 `list_channels` / `claim_task` / `submit_result` / `post_message` 等一套 **channel-centric tools**。

典型使用场景:

1. Alice 在本地 Claude Desktop 里说 "我要做一个 X 功能",Claude 调 Synapse MCP 工具创建了一个 channel,自动把 **org 级顶级系统 agent** 拉进来
2. 顶级系统 agent 在 channel 里和 Alice 对话,分析需求,拆成若干 task;需要专项能力(代码审查 / 测试)时 `@` 某个专项系统 agent 把它拉进 channel
3. 某个 task 派给 Bob(@ Bob),Bob 的本地 agent 通过 MCP 看到 "有一个任务给我",拉取任务详情 / 关联的 KB 文件,让 Bob 的 Claude 做完,`submit_result` 把产物(markdown / 文本)交回 channel
4. 指定的 reviewer(人或 agent)审批;达到 N 个 approve 则 task 关闭
5. Channel 归档 → 产物晋升到 org 的 KB,后续 channel 可读

**核心命题**:用户的"agent"不是 Synapse 提供的,而是**用户自带的本地 agent**;Synapse 只负责把"多方(多人 + 多 agent)协作"这件事的结构化部分做对 —— channel、task、审批、KB 共享、事件扇出。

---

## 2. 层级与核心概念

```
Org
 └─ Project
     └─ Channel
         ├─ Members:     Principal[]   (User + 个人 Agent + 系统 Agent 混成员)
         ├─ Messages:    人 / agent 的发言、@mention、附件
         ├─ Tasks:       结构化工作单元(系统 agent 拆 / 人手工编排均可)
         │   ├─ Submissions:  产物(md / text 文件,走 OSS)
         │   └─ Reviews:      指定 reviewer 的 approve / request-changes
         └─ KB Refs:    挂载到 channel 的知识库文件(成员默认可读,归档后释放)
```

关键一等概念:

| 概念 | 说明 |
|---|---|
| **Principal** | user / agent 统一身份根。channel 成员、task 的 assignee / reviewer、message 的 author 一律 FK `principal_id`,不分叉 |
| **User** | 真人用户,通过 web JWT 或 MCP OAuth 登录 |
| **个人 Agent** | `agents.kind='user'` + `owner_user_id` 归属具体 user;一个 user 可有多个(Claude Desktop / Cursor / Codex 各绑一个) |
| **系统 Agent** | `agents.kind='system'`,org 级;分两类:**顶级系统 agent**(一个 org 一个,默认参与所有 channel)+ **专项系统 agent**(多个,按 `@mention` 或顶级 agent 主动拉入) |
| **Channel** | 一次协作的工作空间;成员 + 消息 + 任务 + KB 挂载 |
| **Task** | 结构化工作单元;有 assignee(principal,通常是 user)/ reviewers / 输出格式约束 / 状态机 / 可被人或系统 agent 创建 |
| **Task Submission** | Task 的产物,第一版只支持 `markdown` 和 `text` 两种,走 OSS 存储 |
| **KB Reference** | channel 关联的 knowledge base 源(文档 / 代码仓 / ...),channel 成员默认有读权限,channel 归档时释放该关联;**channel 产物归档时反向进 KB** |

---

## 3. 关键设计决策

### 3.1 Channel 作为协作单元(不再承载 DAG 状态机)

**决策**:`Channel` 就是"一群 principal 就一件事聊天 + 派任务 + 交结果"的容器。Channel 本身**没有状态机**(除了 `open → archived`),不内置"流程推进"逻辑。任务间是否有依赖,由**系统 agent 在对话里判断**,不由 DB state machine 推进。

**理由**:
- 真实协作里,任务边界和顺序是动态的(人随时补充需求、插入子任务、跳过不必要的步骤)
- DAG 引擎对"天然不能静态定义的流程"反而是束缚
- 顶级系统 agent 本身就是 LLM,由它来"决定下一步派给谁做什么"比固定 DAG 表达力强得多
- 少一层抽象少一组表,实现面大幅收敛

**代价**:没有"编排可复用性"的载体(模板化最佳实践),换成系统 agent 的 prompt + 组织 KB 里的历史记录共同承担"让 agent 学会流程"这件事。短期内复利感会弱一点,长期靠 KB 沉淀。

---

### 3.2 身份模型:User + 个人 Agent + 系统 Agent 统一为 Principal

**决策**:抽象 `Principal` 作为所有身份的根,User 和 Agent 都是 Principal 的子类型(PR #1 已落地)。

**三类 principal 的定位**:

| 类型 | `principals.kind` | `agents.kind` | `agents.owner_user_id` | 典型例子 |
|---|---|---|---|---|
| 人类用户 | `user` | — | — | Alice(人) |
| 个人 agent(本地 MCP 客户端代表) | `agent` | `user` | `<alice_user_id>` | Alice 的 Claude Desktop / Alice 的 Cursor |
| 系统 agent(Synapse 内置) | `agent` | `system` | NULL | **全局顶级编排 agent(Synapse 内嵌,`agents.org_id=NULL`,所有 org 共享)**、org 专项 reviewer agent(per-org 建,`agents.org_id=<org>`)|

**个人 agent 的多设备支持**:

一个 user 可以绑多个个人 agent(同时在 Claude Desktop / Cursor / Codex 上接入 Synapse)。每个本地 agent 对应一行 `agents` 表,`owner_user_id` 相同,`agent_id`(外露的 slug)不同。

**任务分派的 assignee 规则**(见 §3.5):
- Task 的 `assignee_principal_id` **默认指向 `kind='user'` 的 principal**(Alice 本人)
- 同一 user 下**任一**在线个人 agent 都可以 `claim_task`(查询时按 `agent.owner_user_id` 反查 user)
- 允许显式派给具体个人 agent(只有 Alice 的 Claude Desktop 能领),但不是默认模式

### 3.2.1 从 PR #1 延续的数据模型

```
principals (id, kind, display_name, status, ...)
  ├── users   (principal_id UNIQUE FK, id, email, password_hash, ...)
  └── agents  (principal_id UNIQUE FK, id, org_id, kind, owner_user_id NULLABLE,
               agent_id, api_key, display_name, enabled, ...)
```

**`agents` 表新增 / 调整的字段**(PR #4' 落地):

- `owner_user_id BIGINT UNSIGNED NULLABLE REFERENCES users(id)` —— 表达"这个 agent 归某个 user 所有"
  - `agents.kind='user'` 时必填(个人 agent)
  - `agents.kind='system'` 时 NULL(系统 agent 不归属具体人)
- `auto_include_in_new_channels BOOLEAN NOT NULL DEFAULT FALSE` —— 标识"新建 channel 时默认加进来"
  - 典型值:全局内嵌顶级 agent 设 TRUE,org 的专项系统 agent / 个人 agent 默认 FALSE
- **`org_id = 0` 作为 "全局内嵌 agent" sentinel**(保持 NOT NULL,不改类型)
  - `org_id=0` 表示全局内嵌(唯一用途:Synapse 顶级系统 agent,跨 org 共享同一身份)
  - `org_id > 0` 表示 org-scoped(个人 agent / 专项系统 agent 都是)
  - 为什么不用 NULL:`orgs.id` 是 autoincrement 从 1 开始,**0 永远不是合法 org**,可作 sentinel;保留 NOT NULL 让代码侧 `uint64` 零改动(和 PR #1 `PrincipalID uint64 default:0` 同套路)
  - 索引:现有 `idx_agent_org_kind` `idx_agent_org_created` 对 `org_id=0` 仍有效(0 是普通值,索引不感知语义)
  - 无迁移改动(类型不变),PR #4' 只 seed 一行 `org_id=0` 的顶级 agent

**其他字段保留不变**,兼容 PR #1 的迁移结果。

### 3.2.2 Agent 代人行动的权限归属

Agent 用自己的权限,不是触发它的 user 的权限。审计记录 `actor_principal_id=<agent>, triggered_by_principal_id=<user>`。避免 prompt injection 借 user 权限越权。

**对个人 agent 的具体影响**:Alice 的 Claude Desktop 在 MCP 连接时用的是"agent apikey + owner 回填 user_id"双凭证,Synapse 记录两个字段用于审计。被 `@` 或自主行动时的权限判定都基于 agent 本身。

---

### 3.3 系统 Agent 分层:顶级 + 专项 + @mention 驱动

**决策**:顶级系统 agent **全局唯一、内嵌在 Synapse**,所有 org 共享同一个 principal;每个 org 另有若干**专项系统 agent**。

#### 3.3.1 顶级系统 agent(全局内嵌)

- **内嵌在 Synapse 进程**:身份是一行**全局 agents 记录**(`org_id=0` sentinel, `kind='system'`, `agent_id='synapse-top-orchestrator'`, `auto_include_in_new_channels=TRUE`),由 Synapse 启动迁移时种入,**不是 per-org 建**
- 所有 org 的所有 channel 看到的**都是同一个 principal**(同一 `principal_id`)
- 行为(prompt / 工具白名单 / 决策逻辑)由 **Synapse 代码固化**,**不是 per-org 可配**(产品级统一人格)
- 所有新建 channel 自动把它加进 `channel_members`(靠 `auto_include_in_new_channels=TRUE` 的机制)

**关键:数据访问必须按当前 operating org 做隔离** ⚠️

顶级 agent 的身份是全局的,但**每次具体动作的作用域一定绑定到某个具体 org** —— 通过"当前所在 channel"反查出 operating org:

```
顶级 agent 被 @ 的一条消息,落在 org A 的 channel #42
       │
       ▼
operating_org_id = channel.org_id (= A)
       │
       ▼
所有后续动作的 scope 都固定在 org A:
  - 读 channel 内容 → 只能读 channel #42 的消息
  - 调 search_kb → 只搜 channel #42 挂载的 KB(必然是 org A 的 KB)
  - 调 create_task → 只在 channel #42 里建,assignee 必须是 channel 成员(天然 org A 内)
  - 审计事件 → actor_principal_id = <top-orchestrator>, operating_org_id = A
  - LLM 调用费用 → 归在 org A 头上(计费字段带 operating_org_id)
```

**严格不变量**:
- 顶级 agent **不能跨 operating org 读数据** —— 实现上由 tool 的 org 判定兜底:每个 tool 都接受 `operating_org_id`(或 `channel_id` 间接传),service 层 `WHERE org_id = ?` 硬过滤
- 顶级 agent 在 org A 的对话 **不会出现在 LLM context** 中被带到 org B 的对话 —— LLM runtime 里 session 按 `(principal_id, operating_org_id, channel_id)` 三元组隔离,**不同 operating org 的对话不共享 context 缓存**
- `channel_members` / `channel_messages` 的"看得见"语义已经是 channel 内 + org 内;顶级 agent 以 member 身份进入后,**不享受任何"跨 channel / 跨 org"的特权**(它读 channel #42 的历史 ≠ 它能读 org A 其他 channel 的历史,除非那个 channel 也把它加为 member)

**为什么不给它 org-wide 超级权限**:
- prompt injection 一旦得手,全 org 数据泄露风险放大 100 倍
- 用户不信任"一个能穿透所有 channel 的 agent";让它和普通成员一样受 channel 边界约束,审计直观
- 如果真要 "org 级视野"(比如跨 channel 总结),走显式 tool(比如 `summarize_channels_for_org`),每次调用都审计 + 用户授权

#### 3.3.2 专项系统 agent(per-org)

- `agents.kind='system'`,`agents.org_id=<具体 org>`,`auto_include_in_new_channels=FALSE`
- 每个 org 可有多个,按产品 / 团队 / 职能划分(`reviewer-go` / `security-scanner` / `summarizer` / ...)
- 只在被显式加入 channel 时出现(顶级 agent `@` 拉进 / 人手工加)
- 权限由 `channel_members` 决定,不享受跨 channel 权限
- Org admin 可 CRUD(**这是它和顶级 agent 的关键差异**:专项 agent 是 org 自己的业务数据,顶级 agent 是 Synapse 产品的一部分)

#### 3.3.3 在 channel 里如何"出现"

所有 agent(个人 / 顶级 / 专项)**统一走 `channel_members`**,不给任何类型开特殊通道。`auto_include_in_new_channels=TRUE` 的 agent 在 channel 创建时自动加入:

```
channel 创建 hook:
  SELECT principal_id FROM agents
    WHERE auto_include_in_new_channels = TRUE
      AND (org_id = 0                        -- 全局 sentinel(顶级 agent)
           OR org_id = <new_channel.org_id>) -- 本 org 的(专项 agent 若配置了)
  → 全部插入 channel_members
```

从 channel 视角,顶级 agent 和其他成员没区别。

**@mention 驱动的派发模型**:

Channel 里的消息支持 `@<principal_display_name>` 或 `@<agent_id>`(消息体解析后落 `message_mentions` 子表,FK `principal_id`):

- `@ <顶级系统 agent>` —— 默认就在 channel 里,通常不用显式 `@`(它监听全部消息);显式 `@` 可以提高其响应优先级
- `@ <专项系统 agent>` —— 如果该 agent 不在 channel,顶级 agent 收到该消息后**自动把它加进 channel**;随后专项 agent 开始响应
- `@ <user(Alice)>` —— 通知推到 Alice 的所有在线个人 agent(Claude Desktop / Cursor 等);推送方式见 §3.6
- `@ <某个具体个人 agent>`(少见)—— 仅推给那一个 agent

**任务派发的两种方式**:

1. **系统 agent 自动编排**:顶级 agent 分析需求 → 调 MCP tool `create_task` 把 task 创建进 channel,assignee 指向某个 principal(通常是 user)
2. **人手工编排**:人用前端(或本地 agent 的 `create_task` tool)直接建 task,assignee / reviewers / output_spec 都自己填

两种方式产生的 task 数据结构完全相同,只在 `created_by_principal_id` 字段上区分来源。

---

### 3.4 版本弱关联(PR #2 已落地)

channel 直属 project;version 是 channel 的 label/milestone,**多对多**。见 §3.8 数据模型。

---

### 3.5 Task 模型:结构化 + 多方编排 + 显式审批

**决策**:task 是比 message 更结构化的一等实体,有明确的字段 schema、输出格式约束、审批状态机。channel 里可同时存在多个 task,并行推进;task 之间如果有依赖,靠**系统 agent 在对话里**表达和协调,DB 不强制表达依赖关系(第一版)。

#### 3.5.1 数据模型

```sql
-- Task 主体
CREATE TABLE tasks (
  id                          BIGINT UNSIGNED PRIMARY KEY,
  org_id                      BIGINT UNSIGNED NOT NULL REFERENCES orgs,
  channel_id                  BIGINT UNSIGNED NOT NULL REFERENCES channels,
  title                       VARCHAR(256) NOT NULL,
  description                 TEXT,                                 -- markdown
  created_by_principal_id     BIGINT UNSIGNED NOT NULL REFERENCES principals,  -- 人 or 系统 agent
  assignee_principal_id       BIGINT UNSIGNED REFERENCES principals,           -- 通常是 user principal
  status                      VARCHAR(24) NOT NULL,                 -- draft / open / in_progress /
                                                                    --   submitted / approved /
                                                                    --   revision_requested / rejected /
                                                                    --   cancelled
  output_spec_kind            VARCHAR(16) NOT NULL,                 -- 'markdown' | 'text'
                                                                    -- 第一版只两种,后续加 code / mixed
  output_spec                 JSON,                                 -- 约束详情:max_size / required_frontmatter 等
  required_approvals          INT NOT NULL DEFAULT 1,               -- 需要多少个 approve 才算过
  due_at                      DATETIME,
  submitted_at                DATETIME,
  closed_at                   DATETIME,
  created_at                  DATETIME NOT NULL,
  updated_at                  DATETIME NOT NULL
);
CREATE INDEX ON tasks(channel_id, status);
CREATE INDEX ON tasks(assignee_principal_id, status);

-- Reviewer:显式指定谁能审批(可多个;任一 N 个 approve 即通过,N = tasks.required_approvals)
CREATE TABLE task_reviewers (
  task_id       BIGINT UNSIGNED NOT NULL REFERENCES tasks ON DELETE CASCADE,
  principal_id  BIGINT UNSIGNED NOT NULL REFERENCES principals,
  PRIMARY KEY (task_id, principal_id)
);
CREATE INDEX ON task_reviewers(principal_id);  -- "我被指派 review 哪些 task"

-- Submission:task 的产物(一个 task 可多次提交 —— 打回重做即新提交)
CREATE TABLE task_submissions (
  id                        BIGINT UNSIGNED PRIMARY KEY,
  task_id                   BIGINT UNSIGNED NOT NULL REFERENCES tasks ON DELETE CASCADE,
  submitter_principal_id    BIGINT UNSIGNED NOT NULL REFERENCES principals,
  content_kind              VARCHAR(16) NOT NULL,                   -- 对齐 tasks.output_spec_kind
  oss_key                   VARCHAR(512) NOT NULL,                  -- 产物走 OSS
  byte_size                 BIGINT NOT NULL,
  inline_summary            VARCHAR(512),                           -- 列表展示用短描述
  created_at                DATETIME NOT NULL
);
CREATE INDEX ON task_submissions(task_id, created_at);

-- Review:reviewer 的决策(一个 submission 有多个 review 记录)
CREATE TABLE task_reviews (
  id                        BIGINT UNSIGNED PRIMARY KEY,
  task_id                   BIGINT UNSIGNED NOT NULL REFERENCES tasks ON DELETE CASCADE,
  submission_id             BIGINT UNSIGNED NOT NULL REFERENCES task_submissions,
  reviewer_principal_id     BIGINT UNSIGNED NOT NULL REFERENCES principals,
  decision                  VARCHAR(24) NOT NULL,                   -- approved / request_changes / rejected
  comment                   TEXT,
  created_at                DATETIME NOT NULL,
  UNIQUE (submission_id, reviewer_principal_id)                     -- 同一 reviewer 对同一 submission 只能一次决策
);
CREATE INDEX ON task_reviews(task_id);
```

#### 3.5.2 状态机

```
               ┌──────────────────────────────┐
               │                              │ request_changes
               ▼                              │
 draft ──(创建)──▶ open ──(认领)──▶ in_progress ──(submit)──▶ submitted ──(N 个 approve)──▶ approved (closed)
           │                                                      │
           │                                                      └──(reject)──▶ rejected (closed)
           │
           └──(cancel)──▶ cancelled (closed)
```

- **draft**:只有创建者可见,用于"系统 agent 先起草,人确认后再 open"的场景
- **open**:可被认领(assignee 未填时);assignee 已填时直接 → in_progress
- **in_progress**:assignee 在做;允许改 description(会记审计但不回滚状态)
- **submitted**:assignee `submit_result` 之后;等待 reviewers 决策
- **approved**:达到 `required_approvals` 个 approve;close 时间戳;允许后续查看不允许改
- **revision_requested**:任一 reviewer `request_changes` → 回到 `in_progress`(相同 submission 废弃,重做产生新 submission)
- **rejected**:任一 reviewer `reject` 立即终结;close
- **cancelled**:创建者 / assignee / channel owner 主动取消

#### 3.5.3 审批策略

**简化策略**:

- `task.required_approvals` 指明需要多少个 approve(默认 1)
- `task_reviewers` 子表列出**允许审批的 principal 白名单**;不在白名单里的 approve 不计数
- 任一 reviewer `reject` → 立即 rejected(不用等齐)
- 任一 reviewer `request_changes` → 立即回到 `in_progress`(不等其他 reviewer)
- 计数满足 `required_approvals` → approved

**未被采纳的复杂策略**(可开放问题讨论):
- "必须全员 approve"(过于刚性)
- "角色级 reviewer"(需先做职能角色表,见 §6)
- "reviewer 权重"(MVP 过度设计)

#### 3.5.4 产物约束:第一版只 md / text

**决策**:`output_spec_kind` 第一版只支持:

- `markdown`:`.md` 文件,UTF-8,单文件 ≤ 1MB
- `text`:`.txt` 文件,UTF-8,单文件 ≤ 1MB

提交时 MCP tool `submit_result` 接收(payload 上限控制在 body 里;大于阈值要走 OSS direct upload + 返回 key,后续版本再加)。第一版直接 body 传输。

**为什么先不支持代码块 / 多文件**:
- 代码块涉及"怎么和 Git / code review 集成"的大话题(见 §4 开放问题)
- 多文件(`mixed` kind)需要子表 `task_submission_files`,schema 复杂度明显升高

后续扩展保留位:`output_spec JSON` 字段里可以放 `allowed_extensions` / `required_frontmatter` / `max_size` 等细粒度约束,当前版本只用 `max_size` 和 `content_kind` 粗约束。

#### 3.5.5 多方创建入口

两种入口必须都提供(用户明确要求):

1. **HTTP API**(前端):`POST /api/v2/channels/:id/tasks`
2. **MCP Tool**:`create_task(channel_id, title, description, assignee, reviewers, output_spec_kind, required_approvals)`

两个入口底层调同一个 `TaskService.Create`;`created_by_principal_id` 字段区分来源(人走前端 = user principal,个人 agent 调 MCP = agent principal,系统 agent 拆任务 = 系统 agent principal)。

---

### 3.6 MCP Server 设计

Synapse 作为 MCP server,每个人本地的 Claude Desktop / Cursor / Codex 作为 MCP client 连上。

#### 3.6.1 Transport 选型

**决策**:仅实现 **Streamable HTTP transport**(MCP 2025-11-25 规范)。

**事实依据**(2026-04 三家客户端支持现状调研):

| 客户端 | stdio | SSE(legacy) | Streamable HTTP | OAuth |
|---|---|---|---|---|
| Claude Desktop | ✅(本地 subprocess) | ❌ 已 deprecated | ✅(remote 默认) | ✅ |
| Cursor | ✅ | ✅(兼容)| ✅ | ✅ / bearer token |
| Codex CLI | ✅ | — | ✅ | bearer token |

- Streamable HTTP 是所有三家 2026 的默认 remote 形态,SSE 已被 Claude Desktop 废弃
- stdio 仅适用本地 subprocess(Synapse 是服务端,不作为 subprocess 运行)
- **Synapse 不做 stdio,不做 SSE(老 transport),只做 Streamable HTTP**

#### 3.6.2 认证

**决策**:两种并存,按客户端能力路由。

1. **OAuth 2.1**(Claude Desktop 官方 remote connector 走这条路):
   - Synapse 内置 OAuth AS,**只管 MCP 连接的凭证发放**,只有一个 scope `mcp:invoke`(不做 tool 级 scope 细化)
   - 授权对象是 `(user, client, agent_id)` 三元组:Alice 授权 Claude Desktop 作为她的 `claude-desktop` agent 接入

2. **PAT(Personal Access Token / bearer token)**(Cursor / Codex CLI / 其他 CLI 走这条):
   - 用户在 Synapse 网页端生成 token,token 绑定 `(user, agent_id)`,放进客户端 config 文件
   - Cursor `~/.cursor/mcp.json` 里 `headers: { Authorization: "Bearer xxx" }`
   - Codex `~/.codex/config.toml` 里 `bearer_token_env_var = "SYNAPSE_MCP_TOKEN"`

两种方式最终都**落到同一个 agent principal**(`agents.kind='user'` + `owner_user_id` + 具体 `agent_id`),权限检查统一走 principal。

#### 3.6.3 暴露的 Tools

按功能分类(✅=已落地,⏳=暂未实现):

**Channel 相关(7 个)**:
- ✅ `list_channels` —— 列我参与的 channel
- ✅ `get_channel(channel_id, message_limit?)` —— channel 基本信息 + 近期 messages + mentions
- ✅ `post_message(channel_id, body, mentions?, reply_to_message_id?)` —— 发消息 / 引用回复
- ✅ `list_channel_members(channel_id)` —— 列成员 + display_name + kind(@ / 派任务前翻译)
- ✅ `add_reaction(message_id, emoji)` / `remove_reaction(message_id, emoji)` —— 12 个白名单 emoji(PR #12')
- ✅ `list_my_mentions(since_message_id?, limit?)` —— 跨 channel 列我被 @ 的消息(PR #14')

**Task 相关(6 个,全 ✅)**:
- `list_my_tasks(role?, status?)` —— 派给我 / 我作为 reviewer 的 tasks
- `get_task(task_id)` —— 完整 task 详情(含 reviewers / submissions / reviews)
- `create_task(channel_id, title, ...)` —— 创建 task
- `claim_task(task_id)` —— `open` → `in_progress` + 绑 assignee
- `submit_result(task_id, content_kind, content)` —— 提交产物
- `review_task(task_id, submission_id, decision, comment?)` —— 审批

**KB 相关(3 个,PR #14' 后 ✅,search 暂缓)**:
- ✅ `list_channel_kb_refs(channel_id)` —— 列 channel 挂的 KB 关联(source / document)
- ✅ `list_kb_documents(channel_id, query?, limit?, before_id?)` —— 列 channel 经由 source 可见的 KB 文档(PR #14';走 LIKE on title/file_name + keyset 分页,不走向量检索)
- ✅ `get_kb_document(channel_id, document_id)` —— 拉文档元数据 + chunks 拼接的全文(PR #14';权限按 channel 可见集校验)
- ⏳ `search_kb(channel_id, query)` —— 向量近邻检索;需独立 retrieval 模块,留待

**Channel 共享文档相关(6 个,全 ✅,PR #14' 落地)**:
- `list_channel_documents(channel_id)` —— 列 channel 共享文档(含锁状态)
- `get_channel_document(channel_id, document_id, include_content?)` —— 元数据 + 锁;含选填内容
- `create_channel_document(channel_id, title, content_kind)` —— agent 起新文档
- `acquire_channel_document_lock(channel_id, document_id)` —— 抢锁(LockHeld 时返当前持锁人,非错误)
- `save_channel_document(channel_id, document_id, content, edit_summary?)` —— 保存新版,持锁前提,同 hash 幂等
- `release_channel_document_lock(channel_id, document_id)` —— 主动释放
  - **没暴露**:heartbeat / force_release / version_list / get_version_content / soft_delete —— 治理动作走 Web

**身份(1 个)**:
- ✅ `whoami` —— 当前 caller 身份(agent + owner user)

**聚合(1 个,PR #14')**:
- ✅ `get_my_dashboard(task_limit?, mention_limit?, channel_limit?)` —— 一次拉 (assignee 待做 task) + (reviewer 待审 task) + (最近 mentions) + (我参与的 channels);user 问"我有什么要处理"的入口

#### 3.6.4 推送(不做,纯 pull)

**决策**:**不做**服务端推送。所有"新消息 / 新任务 / 被 @ / 状态变化"的感知都走客户端 pull —— 使用现有 tools(`list_my_tasks` / `get_channel` / ...)。

典型用法:用户每次和 agent 对话,LLM 自然会调相关 tool 拉最新状态;长期空闲不通知。

> 曾试做过基于 MCP `notifications/*` 的服务端推送(PR #7' 实施完后下线),实测主流 client(Claude Desktop 为主)**每次对话重建 MCP transport**,推送窗口极短,与纯 pull 无差异。未来若出现持久 GET 连接的 client 场景(Synapse-Web 独立通道 / 自建后台 agent / 长 polling 工具等),**按届时需求重新设计**。

---

### 3.7 事件总线(PR #3 已落地)

Redis Streams + consumer group,是 Synapse 所有跨模块异步事件的底座:

| Stream 名 | 发布者 | 消费者 |
|---|---|---|
| `synapse:asyncjob:events` | asyncjob.service | 未来归档 KB 晋升 reaper 之类 |
| `synapse:channel:events` | `MessageService` / `ChannelService` / `KBRefService` / `MemberService` / `DocumentService` | `eventcard.Writer`(转 system_event 卡片)+ `agentsys` 顶级 agent runtime |
| `synapse:task:events` | `TaskService` | `eventcard.Writer` + 未来通知系统(Inbox) |

**三条规律**:
1. 发布在 DB 事务**之后** best-effort;XADD 失败只 warn,不回滚 DB
2. DB 状态是真相源;消费端失败可以靠下次启动 reaper 扫状态差兜底
3. 消费端必须幂等;at-least-once 投递

其余细节(MAXLEN / consumer 名 / 幂等键等)见 PR #3 代码实现和 `internal/common/eventbus/eventbus.go` 头注释。

---

### 3.8 Channel / Project / Version 基础模型(PR #2 已落地)

> 本节表述对齐 PR #2 实施结果,后续对 channel 语义的所有扩展(见 §3.9 KB 挂载、§3.10 归档)都基于这 5 张表。

**5 张表**(字段摘要,完整见 `internal/channel/model`):

- `projects(id, org_id, name, description, created_by, archived_at)`,`(org_id, name_active)` UNIQUE(归档后释放名字,生成列技巧)
- `versions(id, project_id, name, status, target_date)`
- `channels(id, org_id, project_id, name, purpose, status, created_by, archived_at)`
- `channel_versions(channel_id, version_id)` —— 多对多弱关联
- `channel_members(channel_id, principal_id, role in ('owner','member','observer'), joined_at)`

**PR #4' 扩充的**:

1. **Messages 表**(新增,原本放在 Phase 2,现在提前):
   ```sql
   CREATE TABLE channel_messages (
     id                   BIGINT UNSIGNED PRIMARY KEY,
     channel_id           BIGINT UNSIGNED NOT NULL REFERENCES channels,
     author_principal_id  BIGINT UNSIGNED NOT NULL REFERENCES principals,
     body                 TEXT NOT NULL,                     -- markdown
     kind                 VARCHAR(16) NOT NULL DEFAULT 'text',  -- 'text' / 'system_event'
     created_at           DATETIME NOT NULL,
     INDEX idx_channel_created (channel_id, created_at)
   );

   CREATE TABLE channel_message_mentions (
     message_id     BIGINT UNSIGNED NOT NULL REFERENCES channel_messages ON DELETE CASCADE,
     principal_id   BIGINT UNSIGNED NOT NULL REFERENCES principals,
     PRIMARY KEY (message_id, principal_id)
   );
   ```

2. **KB 挂载表**(新增,替代 PR #2 原 `channel_versions` 承担的"关联 KB"职责):
   ```sql
   CREATE TABLE channel_kb_refs (
     channel_id        BIGINT UNSIGNED NOT NULL REFERENCES channels ON DELETE CASCADE,
     kb_source_id      BIGINT UNSIGNED REFERENCES knowledge_sources,   -- 整库挂载
     kb_document_id    BIGINT UNSIGNED REFERENCES documents,            -- 单文档挂载
     added_by          BIGINT UNSIGNED NOT NULL REFERENCES principals,
     added_at          DATETIME NOT NULL,
     -- 二选一:source 挂载 vs document 挂载
     CHECK ((kb_source_id IS NULL) <> (kb_document_id IS NULL))
   );
   CREATE INDEX ON channel_kb_refs(channel_id);
   ```
   `channel_members` 自动获得"可通过 channel 读挂载的 KB"权限,走 §3.9 的权限模型;channel archive 时自动失效。

**保留不改的**:`channels` / `channel_members` / `projects` / `versions` / `channel_versions` —— PR #2 验证过的基础表。

---

### 3.9 KB 权限:channel 生命周期绑定

**决策**:channel 成员对 channel 挂载的 KB 文件**默认有读权限**,生命周期**严格绑定 channel 状态**:

| Channel 状态 | 成员对 `channel_kb_refs` 关联资源的访问 |
|---|---|
| `open` | 全部成员可读;`owner` 可添加 / 删除挂载 |
| `archived` | 关联关系**立即失效**(读权限也失效);要继续看,通过 KB 本身的独立权限访问 |

**实现**:`knowledge_sources.CanReadByPrincipal(...)` 这类方法除了原有权限表,还加一条 "是否在 open 状态的 channel 里 + 该 channel 挂了这个 source"。写一个 view 或辅助 JOIN。

**个人 agent 的读权限**:Alice 的个人 agent 是独立 principal;它能读哪些 KB = 它作为 channel member 挂载的 KB + agents 表上的直接授权(PR #4' 里不做直接授权,完全靠 channel 挂载)。

---

### 3.10 Channel 归档 + KB 回流

**决策**:channel 归档时,**已 approved 的 task 的产物**晋升到 org KB(不搬字节,元数据指向同一个 OSS key)。

**流程**(触发于 `POST /api/v2/channels/:id/archive`):

1. 校验归档权限(channel owner / org admin)
2. 对 channel 下每个 `status='approved'` 的 task:
   - 取其最新 `task_submission`,拿到 `oss_key` / `byte_size` / `content_kind`
   - 在 `documents` 表新建一行,`oss_key` 指向同一 key;元数据带 task title / channel / org 等
   - 触发 ingestion pipeline 的"跳 fetch"路径(复用现有 pipeline,读 OSS → chunk → embed → 入 pgvector)
3. 未 approved 的 task(draft / cancelled / rejected)**不晋升** —— 不污染 KB
4. 更新 `channels.status='archived'` + `channels.archived_at=now()`
5. `channel_kb_refs` 关联自动失效(应用层 query 时按 channel 状态过滤)
6. 发 `synapse:channel:events` 事件 `channel.archived`,MCP 下发 notification

**幂等**:重复 archive 已归档 channel 返 200 但不重复晋升 —— 靠 `documents` 表上 `(oss_key)` 的唯一检查(或 `content_hash`)。

**晋升对象**(PR #13' 实现):channel 归档时,**共享文档**(§3.11)和**已 approved 的 task submission** 都晋升到 org KB,采用同一套"OSS 共享 + ingestion 跳 fetch"逻辑。每个 submission 就是"这一轮提交",不再有独立的 artifact 状态机和版本链。

---

### 3.11 Channel 共享文档区 + 独占编辑锁(PR #9')

**问题**:task_submission 是单人交付物 + reviewer 审批,但很多协作场景(共写 PRD、会议纪要、运维手册、故障复盘时间线)需要**多人共同对一份产物负责**,channel 缺乏这层载体。

**决策**:在 channel 内引入"共享文档"原语,跟 messages / kb_refs / task_submissions 平级,用**独占编辑锁 + 版本历史**保护并发。

**数据模型**(三张新表):

```sql
channel_documents              -- 文档元数据 + 最新版指针
  id, channel_id, org_id, title, content_kind ('md' | 'text')
  current_oss_key, current_version (sha256), current_byte_size
  created_by_principal_id, updated_by_principal_id
  created_at, updated_at, deleted_at  -- 软删,channel 内列表过滤

channel_document_versions      -- append-only 历史
  id, document_id, version (sha256), oss_key, byte_size
  edited_by_principal_id, edit_summary
  UNIQUE (document_id, version)  -- 同 hash 重复 save 幂等

channel_document_locks         -- 一文档一锁,PK 互斥
  document_id, locked_by_principal_id
  locked_at, expires_at  -- TTL = 10min,心跳每 60s 续
```

**协作语义**:

- channel members 都能 create / read / 抢锁 / 编辑;**默认对等,不分作者**
- 删除:**创建者本人** 或 **channel owner**(MVP 简化)
- 强制解锁:**channel owner 任何时候** / **普通成员仅在锁过期后**
- channel archived 后所有写返 `ChannelArchived`,读仍可

**锁实现**(MySQL,无事务):
1. `INSERT IGNORE INTO channel_document_locks VALUES (...)` —— RowsAffected=1 表示无锁抢到
2. RowsAffected=0 → 已有锁。`UPDATE ... WHERE expires_at < NOW() OR locked_by = caller` —— RowsAffected=1 表示过期/同人续锁
3. 都失败 → SELECT 返当前持锁人(409 + 渲染等待)

**幂等设计**:
- 同 sha256 重复 save → `UNIQUE (document_id, version)` 撞 → service 返已有 version,不重写 OSS,`Created=false`
- 关页面 / 网络断 → 心跳停 → 10min 后锁过期,任意成员可再抢
- 重复释放、重复软删:返 nil(无副作用)

**事件**(走 `synapse:channel:events`,自动写 system_event 卡片):

| event_type | 触发 |
|---|---|
| `channel_document.created` | Create 成功 |
| `channel_document.locked` | AcquireLock 抢成功(续锁不发) |
| `channel_document.unlocked` | ReleaseLock / ForceReleaseLock 真删一行 |
| `channel_document.updated` | SaveVersion `Created=true`,带 version hash + byte_size |
| `channel_document.deleted` | SoftDelete 真改 |

**为什么独占锁而不是 OT/CRDT**:

- 工程量差 5–10 倍;MVP 场景(轻办公协作)独占锁体验已够(Confluence、SharePoint、飞书表格早期都是同模型)
- 锁可视化(`channel_document.locked` 卡片)让其他人立刻知道"X 在改",社交协议比技术协议有效
- 后续要做实时多人光标 / 段落级锁可平滑升级:`channel_document_locks` 加 `section_id` 字段,锁粒度细化

**HTTP API**(全部前缀 `/api/v2/channels/:id/documents`):

```
POST    /                                         创建空白文档
GET     /                                         列 channel 下未删文档(公共空间视图)
GET     /:doc_id                                  读元数据 + 当前锁状态
GET     /:doc_id/content                          读最新版字节
DELETE  /:doc_id                                  软删(创建者/owner)
POST    /:doc_id/lock                             抢锁(409 + 持锁人 if held)
POST    /:doc_id/lock/heartbeat                   续锁(60s 一次)
DELETE  /:doc_id/lock                             主动释放
POST    /:doc_id/lock/force                       强制解锁(owner / expired)
POST    /:doc_id/versions                         保存新版(必持锁;同 hash 幂等)
GET     /:doc_id/versions                         历史版本列表
GET     /:doc_id/versions/:version_id/content     读历史版字节(diff/回滚预览)
```

**与归档晋升的关系**:见 §3.10 末段说明。Phase 1(本 PR)只做共享文档自身,归档时晋升到 KB 留给 PR #13'。

**MVP 不做的**(显式留给后续):

- 实时多人光标 / OT / CRDT
- 评论 / 行内批注 / reactions
- 图片 / PDF / 富文本 / 表格 / 清单
- 文档间链接 / @文档 引用
- 文档全文搜索(归档晋升 KB 后通过 KB 搜)
- 文档 export / 跨 channel 复制
- MCP 工具(v1 只 Web 用;agent 走 task submission 通路)

---

## 4. 容易被忽略但会踩的设计点

| 问题 | 建议 |
|---|---|
| **顶级系统 agent 的"人格"配置** | 由 Synapse **代码固化**,非 per-org 可配(见 §3.3.1);专项 agent 可由 org admin 自配(§3.3.2)|
| **顶级 agent 的 LLM 费用归属** | 按 operating_org_id 计入,每次调用落审计 `(principal_id=<top>, operating_org_id=<A>, tokens, cost)`;rate limit 也按 operating_org 而不是 agent 总量(避免一个 org 烧尽全局额度) |
| **顶级 agent 的跨 org 串扰防护** | LLM session / context cache 按 `(principal_id, operating_org_id, channel_id)` 三元组隔离;严格不共享;单测要覆盖"org A 的对话不出现在 org B 的 prompt 里" |
| **多 agent 同职能并存的协作** | 同 channel 里两个 reviewer agent,谁先回应?—— 第一版按"谁先抢任务谁做"(FIFO 争抢,和 @mention 无关);避免并发审批要靠 `task_reviews` 的 UNIQUE 约束拦(同 reviewer 同 submission 只一票) |
| **个人 agent 的 context 长度** | 本地 agent 的 context window 有限,不可能把完整 channel 历史 / KB 全塞进去。Synapse 的 `get_channel` / `search_kb` 设计要支持分页 + 相关性摘要,默认不返全量 |
| **MCP 连接的在线判定** | 纯 pull 模式下无"在线"概念 —— 对话时能拉到数据就叫"在",长期空闲不感知。未来若重做服务端推送(见 §3.6.4 历史参考),再定义在线追踪机制 |
| **Agent 成本治理** | 个人 agent 调 tool 的频率和总量需要 org-level rate limit(LLM 烧钱);第一版硬编码阈值 + 429,后续做可配额度 |
| **任务 vs 消息的边界** | 简单需求能不能不建 task?—— 可以。`@ 某 agent 帮我做一下 X` 这种轻量请求就留在 message 里,由对方 agent 自行决定是否用 `create_task` 升级。两种粒度长期并存 |
| **历史追溯 / 审计** | 每个 principal 的动作(发消息、建 task、submit、review)都落审计事件;@mention 要可搜索 |

---

## 5. 优先级与实施路线

### 5.1 已完成

- **PR #1 Principal 抽象** ✅ —— `principals` 表 + `users.principal_id` + `agents.principal_id`
- **PR #2 Channel 骨架** ✅ —— projects / versions / channels / channel_members / channel_versions
- **PR #3 eventbus + asyncjob 完成事件 + 幂等键** ✅ —— `internal/common/eventbus` 通用封装 + `async_jobs.idempotency_key`
- **PR #4' Channel messages + Task 数据模型 + agents 扩展** ✅ —— channel_messages / mentions / kb_refs + tasks / reviewers / submissions / reviews + `agents.owner_user_id` / `auto_include_in_new_channels` / 顶级 agent seed
- **PR #5' OAuth AS + MCP Server** ✅ —— `internal/oauth`(DCR + PKCE + consent)+ `internal/mcp`(Streamable HTTP + 10 个 tool);单 scope `mcp:invoke`,Claude Desktop / Cursor / Codex 通吃
- **PR #6' 顶级系统 agent runtime** ✅ —— `internal/agentsys`(LLM + ScopedServices 跨 org 隔离 + audit_events / llm_usage),`@` 触发响应
- **PR #11' Channel 协作时间线 system_event 卡片** ✅ —— `internal/channel/eventcard` consumer,task / member / kb_ref / channel / document 事件转 `kind=system_event` 消息
- **PR #12' Channel 消息 emoji reactions** ✅ —— `channel_message_reactions` + 12 个白名单 emoji,前端 ReactionBar + MCP `add_reaction` / `remove_reaction`
- **PR #9' Channel 共享文档区 + 独占编辑锁** ✅ —— `channel_documents` / `channel_document_versions` / `channel_document_locks` + 11 个 HTTP 端点 + 5 种 `channel_document.*` 事件
- **PR #14' MCP pull 链路补全** ✅(2026-04-25)—— 新增 10 个 MCP tool 把 KB 内容 / 共享文档 / mention / 聚合 dashboard 接进 agent;tool 总数 12 → 22。详见 §3.6.3

### 5.2 待开始

- **PR #10' 审批闭环 + reviewer 通知 + Inbox**
  - `GET /api/v2/users/me/inbox`:reviewer 待审 / 被 @ 未读 / 我的 task 状态变化
  - 已读状态表 `user_inbox_reads`
  - 邮件通知默认关,纯 pull 兜底
- **PR #13' Channel archive → KB 晋升**
  - 共享文档(未删)+ approved task 的最新 submission 双路晋升
  - `documents` 元数据指针指向源 OSS key + ingestion 跳 fetch
  - 异步 asyncjob,失败可重跑
  - 详见 §3.10

### 5.3 已下线 / 不做

- **PR #7' MCP Notifications** —— 2026-04-24 实施完成 + 端到端验收通过后整体下线,代码已回滚。Claude Desktop 每次对话重建 MCP transport,GET SSE 只在 turn 期间(0.7-3.6s)打开,推送窗口极短,增量价值 ≈ 0(详见 §3.6.4)。未来若有持久 GET 连接的 client 场景再按届时需求重新设计
- 复杂审批策略(角色级 / 权重)
- 代码 / 多文件产物格式(第一版只 md / text)
- Channel reopen

---

## 6. 待定 / 开放问题

- **顶级系统 agent 的 bootstrap**:org 创建时由代码 hook 建,还是 DB migration 给每个 org 跑一个 INSERT?倾向 hook(服务端可观察 org.created 事件),但这意味着 org 模块要发出这个事件
- **代码类产物的集成**:未来支持 `content_kind='code'` 时,要不要直接落 Git?还是先走 OSS,后续再 sync 到 Git?这是一个独立的产品决策
- **跨 org 协作**:一个 channel 能不能同时有多个 org 的成员?—— 第一版不支持,channel 严格在一个 org 内
- **专项系统 agent 的 marketplace**:不同 org 能不能共享一组系统 agent 定义(reviewer-go / reviewer-python / ...)?搁置,直到产品需求明确
- **Task 之间的显式依赖**:第一版不做 task_dependencies;如果后续发现系统 agent 的对话式协调不够,再加。加法比减法便宜
- **职能角色 / team tag**:`pm / dev / reviewer / designer` 这种标签,用于 channel UI 展示或 reviewer 匹配。不做,直到产品需求明确
- **个人 agent 的 "被动代言"**:Alice 离线时 `@ Alice` 能不能让她的个人 agent 代为响应("我家主人不在,我帮她先答一下")?涉及"agent 代理 user 说话"的 UX 和信任边界,第一版不做,agent 只响应派给"自己"(agent principal)或"owner user"(会转交给 owner 本人)的任务

