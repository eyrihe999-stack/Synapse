# Synapse 协作系统实施路线

> **方向**:Channel 协作 + MCP 多 agent 接入。
> **当前进度**:PR #1 ✅ · PR #2 ✅ · PR #3 ✅ · PR #4' ✅ · PR #5' ✅ · PR #6' ✅ · PR #9' ✅ · PR #11' ✅ · PR #12' ✅ · PR #10' / #13' 待开始
> **E2E 验收 round 1**(2026-04-24)全链路跑通:A(项目/Channel 骨架)→ B(普通消息)→ C(@Synapse 单轮)→ D(代派任务)→ E(任务生命周期 1-4)→ F(变更执行人/审批人)→ G(MCP + Claude Desktop)→ H(边界隔离)全过;详见 [`e2e-test-plan.md`](e2e-test-plan.md)。附带修复 8 个 bug + 3 个 UX 增强(代派语义 / GORM default:1 / submit 允 revision_requested / 前端 latestSubmission / RequireOrg 守卫 / 归档 project 级联 / ChannelsPage toggle / 刷新按钮)
> **对应设计**:[`collaboration-design.md`](collaboration-design.md)
> **组织方式**:按 PR 分解。每个 PR 给**目标 / 范围 / 依赖 / 验收标准 / 对应设计章节 / 注意事项**,每项带稳定 ID 便于单点追踪

---

## 总体目标

跑通一条"人 + 个人 agent + 系统 agent 在 channel 里协作"的最小闭环:

1. 一个 org 下有 user + 个人 agent + 系统 agent 三类 **principal**(PR #1 身份根已落)
2. 在 project 下拉起 **channel**,成员 = 若干人 + 若干 agent(PR #2 骨架已落)
3. Channel 里**发消息**、`@mention`,顶级系统 agent 监听对话驱动协作(新 PR)
4. 系统 agent 或人手工创建 **task**;task 有结构化 schema、reviewer、输出格式约束(新 PR)
5. 个人 agent 通过 **MCP** 连到 Synapse,拉任务 / 提交产物 / 发消息 —— Claude Desktop / Cursor / Codex 本地运行(新 PR)
6. Task 经审批 → channel 归档 → 产物晋升 KB(新 PR)

**不在第一批范围内**(留到后续 PR 或开放问题):
- OAuth AS(只给 Claude Desktop remote connector 用,先走 PAT)
- 多文件 / 代码产物(第一版只 md + text)
- 跨 org 协作(channel 严格 org 内)
- 复杂审批策略(角色级 / 权重)
- Task 之间的显式依赖(DB 不强制表达)
- 成本治理运行时

---

## PR 依赖图

```
  已落地
  ┌────────────────────────────────────────────────────────────────────────┐
  │ PR #1 Principal ✅  PR #2 Channel ✅  PR #3 eventbus ✅                  │
  │ PR #4' Messages+Tasks ✅                                                │
  │ PR #5' OAuth AS + MCP Server(Streamable HTTP + DCR + PAT + bearer mw) ✅│
  └────────────────────────────────────────────────────────────────────────┘
                           │
                           ▼
                 PR #6' 顶级系统 agent runtime ✅
                 (LLM + operating_org 隔离)
                           │
                           ▼
              PR #11' Channel 协作时间线 ✅
              (system_event 卡片 consumer)
                           │
                           ▼
              PR #12' Channel 消息 reactions ✅
                           │
                           ▼
              PR #9' Channel 共享文档区 + 编辑锁 ✅
              (channel_documents + 独占锁 + 版本)
                           │
                           ▼
           PR #10' 审批闭环 + reviewer 通知 + Inbox
                           │
                           ▼
              PR #13' Channel archive → KB 晋升
              (共享文档 + approved submissions 同晋升)
```

---

## PR #1:Principal 迁移 ✅ 已完成(2026-04-23)

### 目标
新建 `principals` 作为身份根,把现有 `users` 降级为子表,为未来的 `agents` 子表占位。零停机(可接受短时间维护窗口)。

### 完成状态
本地 docker 部署验证通过,所有业务目标达成。生产环境实施时参考下方"实施后记"小节里踩过的坑。

### 对应设计
[§3.5 Agent 作为 Principal,和 User 并列](collaboration-design.md#35-agent-作为-principal和-user-并列)(含 3.5.1~3.5.6 全部小节)

### 关键前提(和摸底后的设计调整)

摸底 `internal/agents` 现状后发现两个和 design doc 原稿的偏差,已在 §3.5 修正:

1. **现有仓库已有 `agent_registry` 表**(`internal/agents`),事实上承担 "org-scoped agent 实例" 角色。**PR #1 不新建 agents 表,而是重命名 `agent_registry` → `agents` 并接入 principal**。"跨 org 共享的全局 agent 定义"搁置(见 §3.5.3)
2. **系统 ID 是 `uint64 autoIncrement`,不是 UUID**。`principals.id` 走**独立 autoincrement**,不复用 `users.id`;`users.id` 保留作 JWT `sub`,新增 `users.principal_id` 作 FK(见 §3.5.5)

### 范围

- **新表**:
  - `principals`(BIGINT UNSIGNED 独立 autoincrement)
  - RBAC 角色不新建表 —— 复用现有 `org_roles + org_members.role_id`(见 design §3.5.4)
- **改表**:
  - `users` 加 `principal_id BIGINT UNSIGNED` 列(先 nullable 回填,再 NOT NULL + FK + UNIQUE)
  - `agent_registry` **RENAME TO** `agents`,加 `principal_id BIGINT UNSIGNED` 列(同样的 nullable→NOT NULL 分两步)
- **保留不动**(避免扩大 blast radius):
  - `org_members.user_id` 不改 —— 现有 FK 语义本来就是 user,不阻塞后续工作(未来单独 PR 改 principal-based)
  - `users.display_name / avatar_url / status` / `agents.display_name / enabled` 冗余列先不删,过渡期双写容忍
  - 现有 `/api/v2/orgs/:slug/agents/*` API 不改
- **代码改动**:
  - 新增 `internal/principal`(只含 `model/` + `migration.go` + 轻量 `repository/`,**不提供 HTTP API**)
  - `internal/user/model.User` 加 `PrincipalID uint64` 字段
  - `internal/agents/model.Agent` 改 TableName 从 `agent_registry` → `agents`,加 `PrincipalID uint64` 字段
  - `cmd/synapse/main.go` 在 `user.RunMigrations` **之前**接入 `principal.RunMigrations`
  - JWT claim / auth 中间件**完全不改**(`users.id` 语义和值都不变)

### 迁移脚本要点(写进各模块自己的 `migration.go`)

`internal/principal/migration.go`:
1. `AutoMigrate(&Principal{})` → 创建 principals 身份根表

`internal/user/migration.go`(追加逻辑):
1. `AutoMigrate(&User{})` 会自动加 `principal_id` 列(nullable)
2. 幂等回填:`SELECT id, display_name, avatar_url, status FROM users WHERE principal_id IS NULL` → 逐行 `INSERT INTO principals (...) ` + `UPDATE users SET principal_id = <new_pid> WHERE id = <uid>`
3. 所有行回填完后,修改 `users.principal_id` 为 NOT NULL + UNIQUE + FK(通过 `db.Exec` 手动 DDL,AutoMigrate 改不了这种约束)

`internal/agents/migration.go`(追加逻辑):
1. 一次性 RENAME:`IF EXISTS (agent_registry) AND NOT EXISTS (agents) THEN RENAME TABLE agent_registry TO agents`
2. `AutoMigrate(&Agent{})` 用 TableName `agents`,会自动加 `principal_id` 列(nullable)
3. 幂等回填:`SELECT id, display_name, enabled FROM agents WHERE principal_id IS NULL` → 逐行 `INSERT INTO principals (kind='agent', display_name, status=CASE enabled WHEN 1 THEN 1 ELSE 2 END, ...)` + `UPDATE agents SET principal_id = <new_pid>`
4. 所有行回填完后,修改 `agents.principal_id` 为 NOT NULL + UNIQUE + FK

### 验收标准(2026-04-23 本地 docker 环境验证)
- [x] `go build ./...` 通过 + `go vet ./...` 通过
- [x] 启动一次后,`principals` 表里 user 行数 = `users` 行数(4=4);agent 行数 = `agents` 行数(1=1)
- [x] `users.principal_id` / `agents.principal_id` 全部非 0,和 `principals.id` 对应正确(`orphan_users=0`、`orphan_agents=0`、kind 匹配零错)
- [x] 再次启动(幂等性):`docker restart` 后不新增 principal 行,不报错
- [x] 登录流程(`/api/v1/auth/login`)路由 + handler 正常(返回 validation 错误);JWT 中间件未失效(`/api/v1/users/me` 无 token 返 401)
- [x] agents CRUD(`/api/v2/orgs/:slug/agents`)路由 + auth 中间件正常(无 token 返 401)
- [x] SQL 验证:`SELECT u.id, u.email, u.principal_id, p.kind, p.display_name FROM users JOIN principals ON u.principal_id=p.id` 返回合理数据,display_name 上移正确
- [x] Agent status 映射:`enabled=1 → principals.status=1`(StatusActive),backfill 规则生效
- [x] `agent_registry` 表被重命名为 `agents`,`SHOW TABLES LIKE 'agent%'` 只剩一张

> **注**:验收时实际 API 路径是 `/api/v1/auth/login`(不是原文里的 `/api/v2/auth/`),路由正确是一码事,路径描述笔误已记。

### 注意事项
- **风险最高,先做**:越晚做,后续 PR 累积的跨表引用越多,迁移面越大
- **RENAME TABLE 是一次性操作**:DDL 要有 "`IF EXISTS(agent_registry) AND NOT EXISTS(agents)`" 保护,二次启动不 panic
- **回填走 Go 代码 for loop**:MySQL 没有 RETURNING,用 `db.Exec(INSERT...)` 拿 `LastInsertId()` 再 `UPDATE`;每个 user/agent 一个小事务,失败不影响已处理的
- **不删冗余列**:`users.display_name` 等过渡期保留,避免灰度期间空写挂;清理放到后续专门 PR
- **FK 约束**:用 `db.Exec` 手动加(`ALTER TABLE ... ADD CONSTRAINT ...`),带 `IF NOT EXISTS` 语义的幂等包装
- **日志字段**:按 memory `feedback_logging_style.md`,迁移日志用 `*Ctx` 带 trace_id

### 实施后记(踩过的坑 + 中途修正)

1. **摸底后发现的设计偏差**(已写进"关键前提"小节):ID 类型是 uint64 不是 UUID;`agent_registry` 表已存在就是 agent 实例表,"全局 registry" 概念现实中没用到 —— design doc §3.5.3 / §3.5.5 / §3.5.6 对应修正
2. **早期草案的 `principal_roles` 表被移除**:最初照抄一份"`(principal_id, org_id, role)`"表,摸底后发现和现有 `org_roles + org_members.role_id` 重复,且把"RBAC 角色"(owner/admin/member)和"职能角色"(pm/dev/reviewer)两种不同概念混在一起。移除方案见 design §3.5.4;code 层 `PrincipalRole` struct 和 AutoMigrate 都清除,并加了 `dropLegacyPrincipalRoles` 函数幂等清掉老镜像残留的空表
3. **BeforeCreate hook 是业务写路径零改动的关键**:User / Agent 的 `BeforeCreate` GORM hook 自动建 principal 并回填 `PrincipalID`,所以现有 `CreateUser` / agent Create 一行代码不用改。Hook 里对 `PrincipalID != 0` 的调用方留了逃生门(测试 / 手动导入场景可用),当前代码路径全部走自动创建
4. **响应 DTO 里的 `principal_id` 字段**:User / Agent 结构体序列化时会带出 `principal_id`,前端不消费即可;若需隐藏可改 struct tag 为 `json:"-"`(暂未改,留给真有前端洁癖时再处理)
5. **docker 镜像要同步重建**:修改 `internal/principal/model` 后必须重新 build 镜像,否则运行的还是老二进制。本次验收时发现 `principal_roles` 老表还在就是镜像没刷新导致 —— 新镜像自动 drop,不手动 SQL 也行

---

## PR #2:Channel envelope ✅ 已完成(2026-04-23)

### 目标
落地 project / version / channel / channel_members / channel_versions 五张基础表,提供基础 CRUD API。

### 完成状态
本地 docker 部署 + 端到端 HTTP + DB 验证通过;29 项场景测试见下方"验收标准"。

### 对应设计
[§3.9 Channel / Project / Version 基础模型](collaboration-design.md#39-channel--project--version-基础模型)

### 依赖
- **PR #1**(`channel_members.principal_id` / `projects.created_by` FK 需要 `principals` 就位)

### 范围
- **新模块**:`internal/channel`(推荐)或拆成 `internal/project` + `internal/channel`(按现有模块粒度习惯)
- **新表**:`projects` / `versions` / `channels` / `channel_versions` / `channel_members`
- **HTTP API**:
  - `POST /api/v2/projects` / `GET /api/v2/projects` / `POST /api/v2/projects/:id/archive`
  - `POST /api/v2/projects/:id/versions` / `GET /api/v2/projects/:id/versions`
  - `POST /api/v2/channels` / `GET /api/v2/channels/:id` / `POST /api/v2/channels/:id/archive`
  - `POST /api/v2/channels/:id/members` / `DELETE /api/v2/channels/:id/members/:principal_id`
  - `POST /api/v2/channels/:id/versions/:version_id` / `DELETE /api/v2/channels/:id/versions/:version_id`

### 验收标准(2026-04-23 本地 docker + 真实 HTTP + DB 验证)

**核心场景**(全部验证通过):

- [x] **可创建 project,名字 org 内唯一** —— T1 创建 `pr2-test-project`(200);T2 同名重复返 `409270021`
- [x] **名字归档后释放** —— T23 archive project 后,T25 用同名重建成功;DB 里 `name_active`:archived 行是 NULL,新行有值(生成列 + UNIQUE 索引生效)
- [x] **可在 project 下创建 channel,默认 `status='open'`** —— T6 响应含 `"status":"open"` + 创建者自动加为 owner(T7 确认)
- [x] **可往 channel 加成员,role 为 owner / member / observer** —— T8 加 member;T9 加 agent 为 observer(跨类型 principal 工作);owner 由 channel 创建时自动加
- [x] **可把 channel 关联到多个 version** —— T11 attach + T13 verify;T12 重复 attach 幂等
- [x] **Archive channel 后 `status` / `archived_at` 正确** —— T21 archive + T22 查询响应:`"status":"archived","archived_at":"2026-04-23T04:10:24.902Z"`
- [x] **非 org 成员不能建 project** —— T3 在不存在的 org_id=999 建 project 返 `403270010 forbidden`
- [x] **非 channel owner 不能改 members** —— T19 user1(已退出 owner)尝试加成员返 403;T20 非 owner 不能 archive channel

**额外业务规则**(roadmap 没列但实现了):

- [x] **Cross-org principal 拒绝加入** —— T10 重做:把不在任何 org 的 user4 加到 channel1(org1),返 `400270053 principal not in channel org`
- [x] **重复加成员撞 PK 翻译成 409** —— T14 重复加 user2 → `409270052`
- [x] **非法 role 拒绝** —— T15 role=superadmin → `400270050`
- [x] **最后一个 owner 不能移除** —— T16 `400270051 cannot remove or demote last owner`
- [x] **升另一个为 owner 后原 owner 可移除** —— T17 user2 升 owner + T18 移除 user1 成功
- [x] **archived project 不允许建 version** —— T26 `400270022 project archived`
- [x] **archived channel 不允许加成员** —— T27 `400270041 channel archived`
- [x] **archive 幂等** —— T28 重复 archive 已归档 channel 返 200
- [x] **attach version 幂等** —— T11 + T12 重复关联同 version 都返 200

**基础设施**:

- [x] `go build ./...` 通过 + `go vet ./...` 通过
- [x] 迁移日志干净:`channel: added projects.name_active generated column` + `created uk_projects_org_name_active unique index` + `migrations completed`
- [x] 所有 5 张表 schema 符合设计(generated column + composite PKs + 复合索引),见 `SHOW CREATE TABLE` 输出
- [x] 路由全部注册(未带 token 返 401 而非 404)

### 注意事项
- `owner` 不能退成员(至少保留一个 owner);转让 owner 留到 Phase 2
- Archive channel **此时只改状态**,KB 晋升留给 PR #13'
- 现在没有 project membership 表;v1 判权用 org membership 即可

### 实施后记

1. **MySQL 没有 partial unique index**:design doc 原写的 `CREATE UNIQUE INDEX ... WHERE archived_at IS NULL` 是 PG 语法。用 `name_active GENERATED ALWAYS AS (IF(archived_at IS NULL, name, NULL)) STORED` + UNIQUE (org_id, name_active) 绕过 —— MySQL 允许多 NULL 在 UNIQUE 索引共存,于是归档行不占名字。见 `migration.go` 的 `ensureProjectNameActiveColumn` / `ensureProjectNameActiveUnique`
2. **跨类型归属查询抽成 `PrincipalOrgResolver`**:user 的 org 归属在 `org_members`、agent 的在 `agents.org_id`,用单一 interface 封装分叉逻辑,未来 `org_members` 统一走 principal-based 后可以简化实现(不改 API)
3. **创建 channel 时事务性把 creator 升为 owner**:避免"channel 创建成功但 channel_members 空"的中间状态。详见 `channel_service.Create` 的 `WithTx`
4. **`repository.LookupUserPrincipalID` 是跨模块查询但留在 channel repo**:service 拿到 JWT 里的 `user_id` 后需要反查 `principal_id`,不想引入 user 模块依赖;直接查 `users` 表一列,轻量、FK 不变,可以接受
5. **所有权限校验在 service 层,handler 只管参数 + 错误翻译**:对齐 organization 模块风格,但不使用 `OrgContextMiddleware` / `permCtxMW` 这套 —— channel 的权限语义是"org 成员 + channel 角色",不走 RBAC 表驱动

---

## PR #3:asyncjob 微调 ✅ 已完成(2026-04-23)

### 目标
搭通用事件总线基础设施:**eventbus 封装** + **完成事件发布** + **幂等键**。后续 consumer(顶级 agent runtime / eventcard writer / 归档 reaper 等)统一在此之上接线。

### 完成状态
本地 docker 部署验证通过,集成测试 4/4 pass。

### 对应设计
[§3.7.6 asyncjob 需要的微调](collaboration-design.md#376-asyncjob-需要的微调)

### 依赖
无(可和 #1、#2 并行)

### 范围

#### 1. eventbus 封装(新包 `internal/common/eventbus`)
独立小包,不放在 `database/` 下(Streams 是消息通道,不是 KV),但复用 `RedisDatabase.GetClient()` 拿底层 `*redis.Client`,不重连。

Publisher 接口:
```go
type Publisher interface {
    // Publish 发布一条事件到指定 stream。fields 会作为 XADD 的 key-value 对写入。
    // 内部带 MAXLEN ~ N 近似裁剪,N 从 config.redis.eventbus.max_len 读取(默认 100000)。
    // 返回 Redis 分配的 stream ID("1234-0");调用方一般无需关心。
    Publish(ctx context.Context, stream string, fields map[string]any) (string, error)
}
```

ConsumerGroup 接口:
```go
type ConsumerGroup interface {
    // EnsureGroup 幂等创建 consumer group(XGROUP CREATE MKSTREAM ... $);已存在则忽略 BUSYGROUP。
    EnsureGroup(ctx context.Context, stream, group string) error

    // Consume 启动阻塞消费循环。先消费自己 PEL 里未 ACK 的历史事件(XREADGROUP ... 0-0),
    // 再切到实时新事件(XREADGROUP ... >)。handler 返回 nil 才 XACK;返回 error 事件留在 PEL。
    // ctx 取消即优雅停机。
    Consume(ctx context.Context, stream, group, consumer string, handler HandlerFunc) error
}

type HandlerFunc func(ctx context.Context, msg Message) error

type Message struct {
    ID     string              // Redis stream ID,用于 XACK
    Fields map[string]string   // XADD 写入的 key-value
}
```

**实现要点**:
- `XREADGROUP` 带 `BLOCK 5000 COUNT 10`(5s 阻塞超时 + 每次最多拉 10 条),避免忙轮询
- Consumer 名由调用方传入(`hostname-pid` 级别),让 PEL 能和具体进程实例绑定,重启后认领自己的未 ACK 事件
- 冷启动流程:先用 `0-0` 消费自己 PEL 里所有未 ACK → 返回空再切 `>` 消费新事件
- Handler 返回 error 不 XACK 就够了,**不做**自动重试 / 死信队列 —— retry 语义留在 consumer 业务层;PEL 长期堆积由运维告警 + `XPENDING` 人工介入
- 不在本包做 idempotency 去重(上层 consumer 自己 `UPDATE ... WHERE status=running` 保证幂等)

#### 2. 完成事件发布
Job 进入终态(succeeded/failed/cancelled)时,`asyncjob.service.markTerminal` 在 `UPDATE async_jobs` 事务提交**之后**调用 `eventbus.Publish(ctx, "synapse:asyncjob:events", fields)`,fields 含 `job_id / org_id / kind / status / result / error / idempotency_key`。

- **为何不走 MySQL:**MySQL 无 `LISTEN/NOTIFY`;Postgres 在本项目只给 pgvector 用且 `Host 可空`,不能当事件通道(详见 design §3.7.4 决策 d)
- **为何不走进程内 channel:**单进程假设会卡住未来多副本部署;Phase 2 的 channel 消息 / inbox(roadmap L344)迟早需要持久化事件底座,一次建好复用
- **Publish 失败不回滚 DB**:只记 error log,DB 的 `async_jobs.status` 是唯一真相,启动时 reaper 扫状态差补推

#### 3. 幂等键
- `Job.Payload` 里约定顶层 `idempotency_key` 字段(nullable),repository 查找时 `(org_id, kind, idempotency_key)` 唯一;已存在终态 job 时短路返回,不重新入队
- 用于 consumer 侧"同一逻辑请求 re-drive 不重复执行"的去重;同时作为 Streams payload 里的关联键

#### 4. Redis key registry 更新
`internal/common/database/redis.go` 文件头的 Key Registry 注释在现有 KV 表**之后**追加一张**独立的 Streams 小表**(不动原表,避免列结构改动波及已有 14 条 KV 登记)。

Streams 表列约定(和 KV 表不同):`Stream Key / MAXLEN / 发布者 / 消费 group / 字段集`。

本 PR 登记一条:
- `synapse:asyncjob:events` — MAXLEN ~ 100000;发布 `internal/asyncjob/service.markTerminal`;字段 `job_id / org_id / kind / status / result / error / idempotency_key`

### 验收标准(2026-04-23 本地 docker + 集成测试验证)
- [x] `eventbus.Publisher` / `ConsumerGroup` 接口 + 集成测试走真实 docker Redis(`//go:build integration`,4/4 pass):`TestPublishConsumeAck` / `TestEnsureGroupIdempotent` / `TestPELReplayOnRestart` / `TestPublishMaxLenTrim`
- [x] `EnsureGroup` 幂等:重复调用 3 次无错(BUSYGROUP 静默)
- [x] **Consumer 崩溃重启重放**:Publish 3 条 → handler 强制返 error 不 XACK → 关闭 consumer → 同名重开 → `0-0` 阶段拿到 3 条并 ACK,`XPENDING` 清零
- [x] **handler 成功 XACK** 后,PEL 里该条消失(`XPENDING.Count=0`)
- [x] Stream 按 `MAXLEN ~` 近似裁剪(O(1)):发 500 条,MAXLEN=50 下 XLEN 远小于 500
- [x] `redis.go` Key Registry 在原 KV 表之后追加独立的 Streams 小表(KV 表未被改动),两个 stream key 登记齐全(MAXLEN / 发布者 / 消费 group / 字段集四列俱全)
- [x] 迁移脚本:`idempotency_key` 列 + `idem_active` 生成列 + `uk_async_jobs_org_kind_idem_active` UNIQUE 三步自动落地,**二次启动幂等**(所有 "added/created" 日志不再出现,只 "running → completed")
- [x] `async_jobs` 表 `SHOW CREATE TABLE` 验证:`idempotency_key VARCHAR(128) NOT NULL DEFAULT ''` + `idem_active GENERATED ALWAYS AS (IF(idempotency_key='',NULL,idempotency_key)) STORED` + UNIQUE 索引俱全
- [x] `go build ./...` + `go vet ./...` + `docker compose build synapse` 全过
- [x] 启动日志:`eventbus publisher ready` 带 asyncjob_stream / max_len=100000 字段
- [x] 现有 `docupload` runner 不受影响(启动加载 + `/api/v2/async-jobs/:id` 路由挂载正常)
- [ ] **未跑**:`docupload` → XADD 端到端事件观察(需要上传真实文档,后续端到端覆盖)
- [ ] **未跑**:同 `(org_id, kind, idempotency_key)` 重复入队 → 返回已有 job,不重新执行(逻辑上由 `FindByIdempotencyKey` 短路实现,等首个调用方填 key 时端到端覆盖)
- [ ] **未跑**:Redis 宕机时 `Publish` 失败不阻塞 asyncjob 状态更新事务(代码路径显式 `publishCompletion` 失败只 warn,需真停 Redis 端到端覆盖,延后)

### 注意事项
- **不加 retry**:retry 语义留在 consumer 业务层;eventbus 也不做 handler 自动重试,error 不 XACK 留 PEL 就够
- **事件投递语义是 at-least-once**:consumer 必须幂等 —— 用条件 UPDATE(`WHERE status='running'`)等手段,`RowsAffected=0` 当已被推过跳过
- **兜底 reaper**:`XADD` 之前进程崩(事件根本没进 Redis)是窄缝但存在;消费方启动时扫 DB 状态做对账,一次性,不持续轮询
- **Redis 不可用降级**:`XADD` 失败不阻塞 `UPDATE async_jobs` 事务,记 error log,reaper 会在下次启动时补推(DB 是唯一真相来源)
- **Stream 裁剪策略**:`XADD MAXLEN ~ 100000` 每次 XADD 时做近似裁剪,保留最近约 10 万条够做审计 + 冷启动重放
- **Consumer 名稳定性**:同一进程实例重启后用相同 consumer 名(如 `hostname-pid` 不行,要 `hostname` 或配置里的 replica id),否则 PEL 里未 ACK 事件没人认领,要等 `XAUTOCLAIM` 超时转移 —— 当前简单起见固定用 `hostname`,够用
- **PEL 长期堆积监控**:handler 反复返回 error 会让 PEL 里事件一直不消失;生产要配 `XPENDING` / `XINFO GROUPS` 监控,PEL 长度超阈值告警(运维侧,不在代码范围)

### 实施后记(踩过的坑 + 中途调整)

1. **`go-redis v8.11.5` 的 `MaxLenApprox` 已 deprecated**:第一版 publisher 用 `args.MaxLenApprox = maxLen`,lint 提示 "use MaxLen+Approx, remove in v9"。改成 `args.MaxLen = maxLen; args.Approx = true`,XADD 命令语义不变仍是 `MAXLEN ~ N`
2. **`go-redis v8` 的 `Block=0` 是永远阻塞,不是非阻塞**:最初想在 PEL 重放阶段用 `Block: 0` 立刻返空,结果阻塞;实测要用短超时(100ms),或用负数(依赖行为,不保险)。PEL drain 用 `replayBlockTimeout = 100 * time.Millisecond`,实时消费用 `liveBlockTimeout = 5 * time.Second`
3. **`FindActive` 和幂等键不互斥**:ScheduleInput 同时支持两条防重路径 —— 有 `IdempotencyKey` 时精确短路(含终态复用),**跳过** `FindActive`;没填时走传统 `FindActive(user_id, kind)` 防"UI 手抖"。系统派单可用前者,人发起的请求(飞书 sync 等)走后者
4. **`MarkFinished` 失败时不 publish**:finalize 函数里 `MarkFinished` 返 error → 直接 return,不走 XADD。避免"DB 说还 running,stream 说 succeeded"的裂脑;reaper 下次启动会对账
5. **沿用 PR #2 的生成列套路搞 UNIQUE**:MySQL 没有 partial unique index,用 `idem_active GENERATED ALWAYS AS (IF(idempotency_key='',NULL,idempotency_key)) STORED` + `UNIQUE (org_id, kind, idem_active)` —— 多 NULL 可并存,于是没填幂等键的任务不受约束,填了的三元组唯一。和 `projects.name_active` 同一模板
6. **NewService 破坏性扩参**:`NewService(cfg, repo, runners, log)` → `NewService(cfg, repo, runners, publisher, log)`,main.go 对应改;没做可选参数 / builder 模式 —— 单一消费方,直接改更简洁。publisher 允许 nil(单测或尚未接事件总线场景)
7. **集成测试用真实 docker Redis 不引入 miniredis**:沿项目"本地 docker 起全套"习惯(用户确认);文件用 `//go:build integration` tag,默认 `go test` 不跑,CI 里 `go test -tags=integration` 启用;用 DB 编号 15 避开业务数据
8. **Streams 在 Key Registry 里独立小表**:原 KV 表有 TTL,Stream 有 MAXLEN 而无 TTL,结构不兼容。在原 KV 表之后追加独立 Streams 小表(四列:Stream Key / MAXLEN / 发布者 / 消费 group / 字段集),不动原表 —— 14 条 KV 登记零风险

---

## PR #4':Channel messages + Task 数据模型 + agents 扩展 ✅ 已完成(2026-04-23)

### 目标
落地新方向下的**数据骨架**:消息、任务、agent 归属 —— 先跑 HTTP 入口,不涉及 MCP。

### 完成状态
本地 docker 部署 + DB schema 验证通过,全部 API 路由挂载正常;顶级系统 agent seed 成功(principal_id=7),auto-include hook SQL 条件验证返回正确。端到端 HTTP 场景测试留给下一轮真实用户登录覆盖。

### 对应设计
[§3.2.1 agents 扩展字段](collaboration-design.md#321-从-pr-1-延续的数据模型) + [§3.3 系统 Agent 分层](collaboration-design.md#33-系统-agent-分层顶级--专项--mention-驱动) + [§3.5 Task 模型](collaboration-design.md#35-task-模型结构化--多方编排--显式审批) + [§3.8 Channel 基础模型扩展](collaboration-design.md#38-channel--project--version-基础模型pr-2-已落地)

### 依赖
- PR #1 / #2 / #3 ✅

### 范围

**agents 表扩展**:
- `owner_user_id BIGINT UNSIGNED NULLABLE REFERENCES users(id)` —— `kind='user'` 必填
- `auto_include_in_new_channels BOOLEAN NOT NULL DEFAULT FALSE`
- **`org_id = 0` 作为全局 agent sentinel**(不改类型,保持 NOT NULL uint64):`orgs.id` 从 1 开始 autoincrement,0 永远不是合法 org;代码侧 `uint64` 零改动(和 PR #1 `PrincipalID default:0` 套路一致)
- **Seed 全局顶级系统 agent**:在 `internal/agents/migration.go` 里加幂等 INSERT —— `org_id=0, kind='system', agent_id='synapse-top-orchestrator', auto_include_in_new_channels=TRUE, display_name='Synapse', apikey=<随机>, owner_user_id=NULL` + 对应 principal(`kind='agent'`);二次启动按 `agent_id='synapse-top-orchestrator'` 已存在就跳过
- 迁移顺序:AutoMigrate 加 `owner_user_id` + `auto_include_in_new_channels` 两列 → seed 顶级 agent(事务内 INSERT principal + INSERT agent)

**新表**(5 张):
- `channel_messages`(id / channel_id / author_principal_id / body / kind / created_at)
- `channel_message_mentions`(message_id, principal_id)
- `channel_kb_refs`(channel_id, kb_source_id 或 kb_document_id, added_by)
- `tasks`(见 design §3.5.1)
- `task_reviewers` / `task_submissions` / `task_reviews`

**HTTP API**:
- Messages:`POST /api/v2/channels/:id/messages` / `GET /api/v2/channels/:id/messages?cursor=`
- KB 挂载:`POST /api/v2/channels/:id/kb-refs` / `DELETE /api/v2/channels/:id/kb-refs/:id`
- Tasks:`POST /api/v2/channels/:id/tasks` / `GET /api/v2/tasks/:id` / `PATCH /api/v2/tasks/:id` / `POST /api/v2/tasks/:id/claim` / `POST /api/v2/tasks/:id/submit` / `POST /api/v2/tasks/:id/review`

**事件发布**(发到 eventbus,消费者由 PR #11' system_event 卡片 + PR #6' 顶级 agent runtime 使用):
- `XADD synapse:channel:events` on new message / mention / archive
- `XADD synapse:task:events` on create / submit / review / status change

### 验收标准(2026-04-23 本地 docker 验证)
- [x] `agents` 表新列存在:`owner_user_id`(BIGINT UNSIGNED NULLABLE)、`auto_include_in_new_channels`(TINYINT(1) NOT NULL DEFAULT 0);存量行 org_id / 旧字段不受影响
- [x] 顶级 agent seed 成功:`agent_id='synapse-top-orchestrator', org_id=0, kind=system, auto_include_in_new_channels=1, owner_user_id=NULL`,并自动建对应 principal(kind='agent')
- [x] Seed 幂等:二次 restart 不重新 INSERT(按 agent_id UNIQUE 检查)
- [x] 新表 7 张(channel_messages / channel_message_mentions / channel_kb_refs / tasks / task_reviewers / task_submissions / task_reviews)AutoMigrate 成功
- [x] Auto-include SQL 正确:`agents WHERE auto_include=TRUE AND enabled=TRUE AND (org_id=0 OR org_id=<new>)` 返回顶级 agent principal_id
- [x] 所有 HTTP 路由挂载:`/api/v2/channels/:id/messages` / `/api/v2/channels/:id/kb-refs` / `/api/v2/tasks/*` / `/api/v2/users/me/tasks` 无 token 返 401(路由存在)
- [x] `go build ./...` + `go vet ./...` + `docker compose build synapse` 全过
- [x] migration 二次 restart 幂等:`channel` / `task` / `agents` 三个模块都只输出 "running → completed",无重复 DDL
- [ ] **手动端到端**:用户登录建新 channel → `channel_members` 含顶级 agent(principal_id=7)—— 需要真实 user JWT 登录,留给产品测试轮次覆盖
- [ ] **手动端到端**:POST 一条 `@xxx` 消息 → `channel_message_mentions` 正确落行 + `redis-cli XREAD synapse:channel:events` 能看到 `message.posted` 事件
- [ ] **手动端到端**:完整 task 状态机跑一遍(create → claim → submit → review 多次直到 approved)+ task.* 事件流入 `synapse:task:events`

### 注意事项
- **不实现 MCP**:纯 REST,前端可调;个人 agent 的接入留给 PR #5'
- **无 WS**:第一版前端想实时看要自己轮询;服务端推送(MCP notifications)2026-04-24 评估后下线,见 design §3.6.4
- **Task 之间无依赖字段**:第一版不建 task_dependencies
- **Inbox 不做**:查"我的任务"直接 `GET /api/v2/users/me/tasks?status=...`

### 实施后记(踩过的坑 + 中途调整)

1. **`agents.org_id` 没改 NULLABLE,用 0 作 sentinel**:原计划改成 nullable 以表达"全局 agent",实测发现 `.OrgID` 在 `internal/agents/*` 里出现十几处,改 `*uint64` 会到处牵连。换成 **`org_id=0` 作 sentinel**(orgs.id autoincrement 从 1 开始,0 天然不合法),代码零改动 —— 和 PR #1 `PrincipalID default:0` 套路一致。design §3.2.1 / §3.3.1 / §3.3.3 同步到 sentinel 表述
2. **系统 agent 的"顶级"定义对齐**:中途澄清顶级 agent 是 **Synapse 内嵌全局唯一**(不是 per-org 一个)。`agents.org_id=0` + seed 一行全局记录,新 org 不需要 hook,channel 创建 hook 查 `(org_id=0 OR org_id=<new>)` 自动包含全局 agent。不引入 `org_agents` 关联表,结构最简
3. **Channel 创建 auto-include hook 失败只 warn 不阻断**:顶级 agent 加入失败不应让 channel 创建失败 —— channel 本身建好比"顶级 agent 缺席"更重要;agent 缺席后续可以手动加 member 补救
4. **消息体不做 @ parsing,由前端/MCP client 传 mentions 数组**:服务端只信任 `mentions: [principal_id]` 数组,不解析 body 里的 `@xxx` 文本。少一个模块,且前端做 @autocomplete 时天然拿到 principal_id,parse 留给最近信息源
5. **Task OSS key 不依赖 submission id**:`synapse/{org}/tasks/{task}/{timestamp}-{random}.{ext}` —— 事务外 PutObject,事务内 INSERT submission 行。失败回滚时 OSS 可能留孤儿对象,best-effort 补 DeleteObject 清理;彻底不清也无妨(bucket lifecycle rule 兜底)。避免 "事务内 hold OSS IO" 的常见坑
6. **`isUniqueViolation` 抽象重复**:channel / task / user 三个模块都有各自的 mysql_err.go 或本地 isUniqueViolation 实现。没单独抽 common helper(三个实现都几行);未来有第 4 个地方用到时再抽
7. **任务 assignee 权限简化**:原设计 Q1 讨论 "assignee 指向 user,任一该 user 的 agent 都能 claim/submit"。**PR #4' 第一版**简化为 "assignee principal_id 本身必须匹配 submitter principal_id",不支持 "agent 代 owner user 提交"。PR #5' MCP Server 接入时 agent principal 会作为 submitter,那时再扩权限检查到 "assignee 或 assignee 的 owner_user 的个人 agent"
8. **`required_approvals` 范围校验放在 service 层**:必须 ≤ `len(reviewers)`,否则任务永远 approve 不了;校验在 `CreateInput` 到达 service 时立即返 `ErrRequiredApprovalsRange`,比 DB 级 CHECK 更早

---

## PR #5':OAuth AS + MCP Server ✅ 已完成(2026-04-23)

### 目标
把 PR #4' 的能力以 MCP tools 的形式暴露给 Claude Desktop / Cursor / Codex;**同时把 OAuth 2.1 AS**(DCR + PKCE + consent flow)一并落地,一次到位支持三种客户端接入方式(Claude Desktop OAuth / Cursor / Codex bearer)。

### 完成状态
本地 docker 部署 + ngrok 公网隧道 + Claude Desktop 实测通过,**完整 OAuth flow + 10 个 MCP tool 全链路跑通**(建 channel → 发消息 → 建 task → claim → submit → review → approved,事件流 4 条 `task.*` + 1 条 `message.posted` 齐全)。

### 对应设计
[§3.6 MCP Server 设计](collaboration-design.md#36-mcp-server-设计)

### 依赖
- PR #4'(tools 底层 service 层) ✅

### 范围(最终落地)

**新模块 `internal/oauth`**
- 5 张表:`oauth_clients` / `oauth_authorization_codes` / `oauth_access_tokens` / `oauth_refresh_tokens` / `user_pats`
- 标准 OAuth 2.1 端点:`/oauth/register` (RFC 7591 DCR) / `/oauth/authorize` / `/oauth/login` / `/oauth/authorize/consent` / `/oauth/token` / `/oauth/revoke`
- Well-known metadata:`/.well-known/oauth-authorization-server`(RFC 8414) + `/.well-known/oauth-protected-resource` + 子路径通配(RFC 9728 per-resource 形式)
- **SSR 同意页**:server-rendered HTML,password 登录 + consent 两步;OAuth session 走 Redis(10 分钟 TTL,独立于 web JWT)
- **Consent 成功自动建 agent**:`agents.kind='user' + owner_user_id=<user>`,通过 `agents.BootstrapUserAgent` 扩展方法落地
- **PAT 管理**:`POST/GET/DELETE /api/v2/users/me/pats`(给 Cursor / Codex / curl 的 bearer token 路径)
- **OAuth client 管理**:`POST/GET /api/v2/oauth/clients` + `POST /api/v2/oauth/clients/:id/disable`(手动配 client 的路径;DCR 也走同一 service)
- **统一 Bearer 认证中间件** `internal/oauth/middleware/bearer_auth.go`:解 OAuth access_token 或 PAT,SHA-256 hash 查 DB,把 `(user_id, agent_principal_id, oauth_client_id)` 写进 `gin.Context` + `request.Context`
- **DCR per-IP 限速**:复用 `rdb.SlidingWindowAdd`,默认 60s / 10 次

**新模块 `internal/mcp`**
- 基于 `github.com/mark3labs/mcp-go@v0.49.0` 的 `StreamableHTTPServer`,挂在 `/api/v2/mcp/*`
- **10 个 tool 全实现**:
  - Channel:`list_channels` / `get_channel` / `post_message`
  - Task:`list_my_tasks` / `get_task` / `create_task` / `claim_task` / `submit_result` / `review_task`
  - KB:`list_channel_kb_refs`(`search_kb` / `get_kb_document` 待后续,需接 pgvector)
- Facade 模式:`internal/mcp/{channel,task,kb}_adapter.go` 包装 service 接口,`Server.Deps` 依赖抽象
- 认证:从 `request.Context` 读 `AuthFromContext`(BearerAuth 中间件注入的身份),所有 tool 以 **`AgentPrincipalID` 作为操作者**,不信任请求体里的 principal_id(防越权)
- Service 层扩展 `*ByPrincipal` 变体方法:`channel.ChannelService.ListByPrincipal` / `channel.MessageService.{Post,List}AsPrincipal` / `channel.KBRefService.ListForPrincipal` / `task.TaskService.{Create,Get,Claim,Submit,Review}ByPrincipal` + `ListByAssigneePrincipal` —— HTTP 路径(user JWT)和 MCP 路径(agent principal)共享核心逻辑,只在入口差一次反查

**配置**:`config.OAuth`(issuer / TTL / DCR 限速)+ 两个 yaml 都加

### 验收标准(2026-04-23 docker + ngrok + Claude Desktop 验证)

OAuth 基础:
- [x] 5 张新表 AutoMigrate 成功,二次启动幂等
- [x] `.well-known/oauth-authorization-server` 返 RFC 8414 metadata;issuer 跟随 config 可配(ngrok URL)
- [x] `.well-known/oauth-protected-resource` + 子路径通配(`/api/v2/mcp`)都返 200
- [x] DCR(`POST /oauth/register`)匿名注册成功返 `client_id + client_secret`;per-IP 限速阈值 10 次/分钟生效
- [x] `authorize` 错 client_id / redirect_uri → 致命错误页(不跳转,防 open redirect);错 PKCE / response_type → RFC 6749 redirect 带 error
- [x] `authorize` 未登录 → SSR 登录页;登录成功 → SSR consent 页
- [x] `authorize/consent` 同意 → 自动建 `agents.kind='user'` + 对应 principal,授权码落 `oauth_authorization_codes`
- [x] `token` endpoint PKCE S256 校验生效,无效 verifier → `invalid_grant`
- [x] `token` endpoint authorization_code 重放检测 → 连带吊销该 client 的所有 tokens(RFC 6749 §10.5)
- [x] `refresh_token` grant 做 token rotation,旧 refresh token 被标 revoked + rotated_to
- [x] `revoke` endpoint 接 RFC 7009 错误处理(无效 token 返 200)

MCP 完整链路(Claude Desktop 实测):
- [x] Custom Connector UI 输入 URL → DCR 自动注册 → 浏览器跳转 authorize → user 输入 email+password → consent → 302 回 Claude 换 token
- [x] Claude 自动拿到 access_token,后续调用走 Bearer header
- [x] `list_channels` 按 principal 成员关系过滤(新建 agent 没加入任何 channel → 空列表;加入后列表含正确条目)
- [x] `get_channel` 返 channel 基本信息 + 最近消息 + mentions
- [x] `post_message` 以 Claude agent principal 为 author,正确落 `channel_messages` 表 + 发 `synapse:channel:events` XADD
- [x] 完整 task 状态机:`create_task` → `claim_task` → `submit_result` → `review_task` → `approved`,OSS key 正确(`synapse/{org}/tasks/{task}/{ts}-{rnd}.md`)
- [x] 状态机跃迁事件 4 条落 `synapse:task:events`:`task.created` / `task.claimed` / `task.submitted` / `task.reviewed`
- [x] 状态机拦截生效:open 状态直接 submit_result → `invalid state transition` 错误(Claude 的错误展示 + 引导先 claim)
- [x] reviewer 白名单生效:非白名单 principal 调 review_task 返 403
- [x] `go build ./...` + `go vet ./...` + docker build + 启动 + migration 幂等全过

### 注意事项
- **Streamable HTTP only**:不做 stdio(Synapse 是服务端)、不做老 SSE transport(2026 已 deprecated)
- **Cookie `SameSite=Lax`**:OAuth consent flow 的跨站 redirect 要求
- **PAT 和 OAuth access_token 同一中间件处理**:前缀不同(`syn_pat_` vs `syn_at_`)但 hash 后分别查两张表
- **`agent_id` 是 FK 语义**:`oauth_access_tokens.agent_id` 存的是 `agents.principal_id`(不是 `agents.id`),命名和历史一致;中间件解出后直接当 principal 用

### 实施后记(踩过的坑 + 中途调整)

1. **Claude Desktop 的 Custom Connector UI 只支持 OAuth,不支持 bearer token 直连**(搜索社区 issue 确认)。为此把 OAuth AS 和 MCP server 合到同一个 PR 一步到位;Cursor / Codex 仍可用 PAT 路径
2. **Claude Desktop 是云端服务,访问 localhost 不通**:本地 dev 必须用 ngrok(或 Cloudflare Tunnel)暴露到公网 URL,并同步改 `oauth.issuer` 为公网 URL 重启 —— 否则 `.well-known` 返回的 endpoint URL 仍是 localhost,OAuth flow 会在 token exchange 步骤断掉
3. **`.well-known/oauth-protected-resource` 子路径通配**:Claude Desktop 按 RFC 9728 探测 `/.well-known/oauth-protected-resource/api/v2/mcp`(metadata 路径 + resource path),我们原本只有精确 `/.well-known/oauth-protected-resource`。补了 `/*path` catch-all 路由,两者返同一 body
4. **mcp-go `server.NewStreamableHTTPServer` + gin**:用 `gin.WrapH(mcpServer.HTTPHandler())` 挂到 `Any` 路由。为了同时处理 `/api/v2/mcp`(精确)和 `/api/v2/mcp/...`(sub-path),注册两条:`Any("")` + `Any("/*any")`
5. **认证中间件**:`BearerAuth` 注入身份到**两处** —— `gin.Context`(给 gin chain handler 用)+ `request.Context`(给 mcp-go tool handler 用,通过 `c.Request = c.Request.WithContext(newCtx)` 覆盖)。MCP tool 从纯 `context.Context` 读 `AuthFromContext`,不依赖 gin
6. **`*ByPrincipal` 变体方法**:HTTP 和 MCP 两条路径本来要么重复代码要么复用 —— 重构成 "核心用 principal,HTTP 入口反查一次" 的模式。`core()` 函数里做实际业务,HTTP / MCP 两种入口分别调。改动面不小但收益明显,两条路径测试覆盖同一逻辑
7. **Submit 权限校验 MVP 简化**:原设计 Q1 讨论 "submitter ∈ {assignee 本人, assignee 的 owner user 的任意个人 agent}";本 PR 第一版只实现 "submitter principal == assignee principal"。后续 PR #6' 顶级 agent runtime 接入时再扩权限模型(让 agent 代 owner user 提交)
8. **OAuth consent 时 agent 归属 org**:MCP OAuth flow 里建 `kind='user'` agent 需要 `org_id`,但 OAuth 没直接暴露 org 选择。MVP 取该 user 的第一个 org(`orgService.ListOrgsByUser(userID)[0]`);多 org 场景未来 consent 页加 org 选择器
9. **`oauth.issuer` 配置不同步导致的隐蔽 bug**:改 ngrok URL 后如果不重启 synapse,`.well-known` 返回的还是老 issuer,Claude Desktop 会在 token exchange 时访问老 URL → 超时。记:`oauth.issuer` 属"启动时读一次"配置,改后必须 restart
10. **OAuth 全局 agent seed 对存量 channel 无效**:`auto_include_in_new_channels=TRUE` 的顶级 agent 只会被 hook 到**新建** channel,存量 PR #2 时代的 channel(如 mcp-smoke 之前的 PRD-feature-x)没有这个 agent。实施时发现的非 bug 语义 —— 如果要让顶级 agent 加入老 channel,需要单独的 backfill 或手动加

### 本次 PR 没做、但已留接口的能力
- **`search_kb` / `get_kb_document`** MCP tool:需接入 pgvector 检索 + documents 表权限;留到 PR #13'(归档 KB)或单独 PR 做
- **OAuth client 的 admin 超级禁用**:普通用户只能禁用自己建的 client;DCR 匿名注册的 client(registered_by_user_id=0)未来需要 admin 接口禁用
- **Multi-org agent 选择**:consent 页当前硬编码 "取第一个 org",UI 层面加选择器属 Phase 2

---

## PR #6':顶级系统 Agent Runtime ✅ 已完成(2026-04-24)

### 目标
内嵌在 Synapse 的**全局唯一**顶级系统 agent(PR #4' 已 seed 行)上线:消费 channel 事件,按 operating_org 做严格隔离地调 LLM,**只在被 `@` 时回应**,产出回复或创建 task。

### 对应设计
[§3.3 系统 Agent 分层](collaboration-design.md#33-系统-agent-分层顶级--专项--mention-驱动)(重点读 §3.3.1 的数据隔离不变量)

### 依赖
- PR #4' ✅(seed 行已种 + 消息 / 任务数据层 + `mentioned_principal_ids` 已进事件 CSV)
- PR #3 ✅ eventbus

### 实施前锁定(2026-04-24)

在规划阶段拍定的关键决策,写在前面避免实施时回头再议:

| 项 | 决定 |
|---|---|
| 模型 | `gpt-5.4`(Azure 部署) |
| LLM provider | **只保留 Azure**,不建 fake —— dev/prod 环境行为一致,真 key 走 `config.local.yaml` |
| Config 节 | 新增独立 `llm.azure.*`(不复用 `embedding.azure`,因为 LLM 和 embedding 经常是不同 Azure 资源) |
| 响应条件 | **只在 `mentioned_principal_ids` 含 top-orchestrator 时回**。不做"没别的 agent 在线就兜底"—— Synapse 走纯 pull 模型(无在线追踪),不沿用此前 MCP 推送方案 |
| 跨 org 隔离 | 不靠 prompt,靠 `scoped.ScopedServices`:构造时绑定 `(orgID, channelID, actorPrincipalID)`,public API **不接受**这三个参数,物理上传不进来别的 org(静态断言) |
| `agents.org_id=0` sentinel | 保留;本 PR 在 `internal/agents/repository` 新增 helper `ListAutoIncludeVisibleToOrg(orgID)` 封装 `OR org_id=0`;禁止裸查 agents 表。长期(出现第二个全局 agent 时)单独 PR 切 `NULLABLE` |
| Tool-loop 上限 | 硬编码 `MaxToolRounds=5` / `RecentMessageWindow=20`,不进 config |
| Tool 执行失败 | 回 channel 一条"执行工具 X 时出错:{err}" + audit `action=error` + ACK(不静默) |
| Rate limit | `llm.daily_budget_per_org_usd`;超限 → 回 channel "今日预算已用完" + ACK(**不是 HTTP 429**,runtime 不走 HTTP) |
| audit_events | 本 PR 新建,全量写,不加开关 |
| Prompt 管理 | 硬编码在 `internal/agentsys/prompts/top_orchestrator.md`;任何改动走 PR review |
| MCP 回连 | **禁止**顶级 agent 通过 MCP 连回 Synapse;直接调 service 层 Go 接口 |

### 范围

**Config 改动** —— `config/config.go` 加:
```go
type LLMConfig struct {
    Provider             string         `yaml:"provider"`  // 必须是 "azure"
    Azure                AzureLLMConfig `yaml:"azure"`
    DailyBudgetPerOrgUSD float64        `yaml:"daily_budget_per_org_usd"` // 0 = 不限
    RequestTimeoutSec    int            `yaml:"request_timeout_sec"`      // 默认 60
}
type AzureLLMConfig struct {
    Endpoint, Deployment, APIKey, APIVersion string
}
```
`Validate()` 强制 azure + endpoint/deployment/api_key 非空;`config.dev.yaml` 留空串模板(真值走 `config.local.yaml`)。

**新模块 `internal/common/llm`** —— 仿 `internal/common/embedding` 骨架:
- `llm.go` —— `Chat` 接口 + `Request / Response / Message / ToolDef / ToolCall / Usage` 类型
- `azure.go` —— Azure v1 兼容 provider(POST `{endpoint}chat/completions`,`api-key` header)
- `pricing.go` —— gpt-5.4 的 input/output $/1K tokens 写死;换模型时一起改
- `factory.go` —— `NewFromConfig(cfg LLMConfig) (Chat, error)`,只分支 azure
- **不建 `fake.go`**;测试用 mock `Chat` interface

**新模块 `internal/agentsys/`** —— 目录:
```
internal/agentsys/
  migration.go                 audit_events + llm_usage DDL
  model/{audit.go, usage.go}   GORM 结构体
  repository/{audit.go, usage.go}
  scoped/scoped.go             ★ ScopedServices(隔离核心)
  tools/tools.go               LLM tool schema + dispatcher(JSON → scoped 调用)
  runtime/{orchestrator.go, handler.go, const.go}
  prompts/top_orchestrator.md  硬编码 system prompt
  hook.go                      BuildAutoIncludeHook(agentRepo) channel.AutoIncludeFn
```

**新增表**(在 `internal/agentsys/migration.go`):
- `audit_events(id, actor_principal_id, operating_org_id, channel_id, action, target_id, detail_json, created_at)` —— 本 PR 先给 top-orchestrator 用,设计上可共用
- `llm_usage(id, operating_org_id, actor_principal_id, model, prompt_tokens, completion_tokens, cost_usd, channel_id, created_at)`

**Channel 创建 hook** —— 依赖注入,不让 channel 包反向依赖 agentsys:
- `internal/agentsys/hook.go` 导出 `BuildAutoIncludeHook(agentRepo)` 返回 `channel.AutoIncludeFn`
- `ChannelService.Create()` 事务最后调 `c.autoIncludeFn(ctx, tx, newChannel)` —— 加 helper `ListAutoIncludeVisibleToOrg(orgID)` 封装 `WHERE auto_include_in_new_channels=TRUE AND (org_id=0 OR org_id=?)`
- 存量 channel 不 backfill(沿用 PR #5' §513 的决策)

**Runtime 主循环**(`internal/agentsys/runtime/orchestrator.go`):
- 常驻 goroutine + eventbus consumer group `top-orchestrator`(全局一个,不 per-org)
- 启动时查一次 `SELECT principal_id FROM agents WHERE agent_id='synapse-top-orchestrator'` 缓存在结构体
- 消费 `synapse:channel:events` `message.posted`:
  1. 过滤非 `message.posted` 事件(直接 ACK)
  2. 解析 `mentioned_principal_ids` CSV,**不含 top-orchestrator 的 principal_id 就 ACK 跳过**
  3. Rate limit 检查(按 `operating_org_id` 查 `llm_usage` 当天 SUM(cost_usd))→ 超限回"预算用完" + ACK
  4. `scoped := scoped.New(ctx, orgID=事件.org_id, channelID=事件.channel_id, topOrchPID, ...)` → 进 handler
- Handler tool-loop(单条处理上限 `MaxToolRounds=5`):
  1. 取 `scoped.ListRecentMessages(ctx, RecentMessageWindow=20)` + 当前消息 → 组 prompt
  2. 调 `llm.Completions(..., tools=toolSchemaList)`
  3. 有 `tool_calls` → dispatcher 调 `scoped.*` → 结果喂回 → 再跑一轮
  4. 有 `content` → `scoped.PostMessage(content, mentions)` → 退出
  5. 每步写 `audit_events`;LLM 成功一次写 `llm_usage`
  6. LLM 超时/报错 → `scoped.PostMessage("我暂时回不上来")` + ACK(不重试烧钱)
  7. Tool 执行失败 → `scoped.PostMessage("执行工具 X 时出错:{err}")` + audit `error` + ACK
  8. 真 panic → 不 ACK 留 PEL,下次重启重放

**Scoped 服务(隔离核心)** —— `internal/agentsys/scoped/scoped.go`:
```go
type ScopedServices struct {
    orgID, channelID, actorPrincipalID uint64  // 构造后不可变、无 getter
    messages channel.MessageService
    tasks    task.TaskService
    // ...
}
// 所有方法硬塞 s.orgID / s.channelID / s.actorPrincipalID,调用方传不进来别的值
func (s *ScopedServices) PostMessage(ctx, body string, mentions []uint64) (...)
func (s *ScopedServices) CreateTask(ctx, in CreateTaskArgs) (...)  // in 不含 ChannelID/CreatorPID
func (s *ScopedServices) ListRecentMessages(ctx, limit int) (...)
func (s *ScopedServices) ListChannelKBRefs(ctx) (...)
```
Code review 红线:`internal/agentsys` 代码里**不得出现** `s.orgID` / `s.channelID` 的裸引用(只在构造和内部 service 调用时转发);LLM tool schema **不得包含** `org_id` / `channel_id` 参数。

**Prompt 初稿**(`internal/agentsys/prompts/top_orchestrator.md`) —— 规划阶段草稿见 2026-04-24 讨论记录,核心五条:只在被 `@` 时回应、简短聚焦、不知道就说不知道、只属于当前 channel(不假设别的 channel 存在)、敏感操作转人工。**Prompt 是行为规范,不是安全栅栏** —— 跨 org 安全由 `ScopedServices` 保障,prompt 拿掉第 4 条也不会漏数据。

**计费 / rate limit**:
- 每次 LLM 调用落 `llm_usage(operating_org_id, actor_principal_id, model, prompt_tokens, completion_tokens, cost_usd, channel_id, created_at)`
- `UsageRepo.OverBudget(ctx, orgID, dailyCap)` 查当天 SUM(cost_usd) 比较
- 超限 org 之间互不影响(不共用全局额度)

### 实施顺序(依赖有向)

1. `config.LLM` + `Validate()`
2. `internal/common/llm`(Chat + Azure + pricing + factory)
3. `internal/agentsys/migration.go`(audit_events + llm_usage)
4. `internal/agentsys/model` + `internal/agentsys/repository`(audit / usage)
5. `internal/agents/repository` 加 `ListAutoIncludeVisibleToOrg` + `const.go` 注释
6. `internal/agentsys/scoped/ScopedServices`
7. `internal/agentsys/tools`(schema + dispatcher)
8. `internal/agentsys/prompts/top_orchestrator.md`
9. `internal/agentsys/runtime`(orchestrator + handler + const)
10. `internal/agentsys/hook.go`(`BuildAutoIncludeHook`)
11. `internal/channel/service` 接 `autoIncludeFn`
12. `cmd/synapse/main.go` 装配 + 常驻 goroutine
13. 单测 + 集成测
14. 更新本 roadmap 进度行 + 写"实施后记"

### 验收标准
- [x] Synapse 首次启动后,`agents WHERE agent_id='synapse-top-orchestrator'` 有且只有 1 行,`org_id=0`(PR #4' 已覆盖,本 PR 不重复做)
- [x] 二次启动不重复 seed(PR #4' 已覆盖)
- [ ] 任何 org 新建 channel 后,`channel_members` 含顶级 agent(hook 生效)
- [ ] 被 `@` 时:org A 的 channel 里 `@synapse 我的 org 里有多少 channel` → agent 回答限定在 org A(不会泄露其他 org 数据)
- [ ] 未被 `@` 时:agent 不回应(ACK 跳过,不写 audit/usage)
- [ ] 并发:org A 和 org B 各起一条对话 → LLM 调用的 context 互不掺杂(静态断言:ScopedServices 的 public API 在类型层面无法传错 org/channel)
- [ ] 审计表:每次响应都能查到 `(actor=top-orchestrator, operating_org_id=?, channel_id=?, action ∈ {post_message, create_task, error, skip_budget})`
- [ ] 某 org 跑满 rate limit → channel 回"今日预算已用完" + 其他 org 不受影响
- [ ] Tool 执行失败 → channel 收到 "执行工具 X 时出错" + audit `action=error`

### 注意事项
- **顶级 agent 的 operating_org 不是 agent 属性,是"每次调用的上下文"**;`scoped.ScopedServices` 构造后不可变,无 getter,禁止把 `orgID` 作为 runtime 级全局变量
- **不给它"查全局 orgs 列表"之类的 tool**;tool schema 不含任何 `org_id` / `channel_id` 参数 —— 作用域由 Go 侧 scoped 绑死
- **`agents.org_id=0` sentinel 的护栏**:所有查 agents 表"对 org X 可见"的地方必须走 `repository.ListAutoIncludeVisibleToOrg` / `ListVisibleToOrg` helper,禁止裸写 `WHERE org_id = ?`(会漏掉全局 agent)。未来出现第二个全局 agent 时单独开 PR 切 `NULLABLE`
- **Prompt 管理**:硬编码在 `internal/agentsys/prompts/top_orchestrator.md`;任何改动随 PR review
- **禁止通过 MCP 反向连回 Synapse**:顶级 agent 是进程内 goroutine,不开 MCP 连接,不拿 PAT;直接调 service 层 Go 接口
- **没有 LLM session 缓存**:每次事件重新组 context(最近 20 条消息 + 当前消息),不做 per-`(org, channel)` 长连接 session —— 加不加留给性能观察后再说

### 实施后记(2026-04-24)

按"实施前锁定"表格和"实施顺序"14 步依法办事,整体没打折,**实际落地时有 6 处偏差**,记下以免将来自己或旁人看代码疑惑:

1. **auto-include hook 不是新建,PR #4' 已做了**:原计划 Step 10/11 要在 `internal/agentsys/hook.go` 导出 `BuildAutoIncludeHook(agentRepo) channel.AutoIncludeFn`,让 `channel.service.Create` 走依赖注入调 hook。摸代码时发现 PR #4' 已经在 `channel.service.channelService.autoIncludeAgents`(`channel_service.go:251`)+ `channel.repository.LookupAutoIncludeAgentPrincipals`(`kb_ref.go:65`)里落地了等效逻辑 —— SQL 正确写的是 `WHERE auto_include_in_new_channels=TRUE AND enabled=TRUE AND (org_id=0 OR org_id=?)`,创建失败只 warn 不阻塞 channel。按"不为了美学重构"原则跳过 Step 10/11,不搞 hook 注入。副作用:channel 模块保留了对 agents 表的直读(注释里写了"跨模块查 agents 表(不引入 agents 模块依赖)"),耦合小瑕疵但可控。
2. **Step 5 的 `agents.ListAutoIncludeVisibleToOrg` helper 实际未被调用**:原本想让 PR #6' 的 hook 通过它统一经过 agents 模块查表,跳过 Step 10/11 后这个 helper 就没有真实调用方。保留它 + `GlobalAgentOrgID` 注释里的查询护栏说明 —— 作为"未来任何要从 agents 模块查全局+org 可见 agent 的代码应走的正确入口"。grep 不到用它会被未来维护者当"死码"删,在注释里提前打预防针。
3. **`LLMConfig.Validate()` 没单独做**:实施前锁定表格里说"factory 强制 provider/endpoint/deployment/api_key 非空"。实际校验放在 `llm.New` / `newAzure` 的构造时(wrap `ErrLLMInvalid`),不另开 `config.Validate`。理由是 embedding 包也是这个模式(factory 验 config),保持仓库习惯一致;config 层新加 `LLMConfig.Validate()` 反而是孤例。
4. **`llm.ToolDef` 的 ArgumentsJSON 字段命名打架**:最初定义 `Message.Content` 想塞 tool 历史回放;实际 OpenAI 协议里 assistant 的 tool_calls 字段独立于 content。当前 handler 在一次事件处理内完成所有 tool-loop,不需要把 assistant 的 tool_calls 历史回灌,所以 `azureChatMessage.ToolCalls` 只在序列化时用、不从上层 `llm.Message` 暴露 —— 有意的简化,留注释在 `azure.go` 里。未来如果要做跨事件断点续聊(不太可能),再扩 `llm.Message.ToolCalls`。
5. **`llm_usage.CostUSD` 用 float64 不是 decimal**:业务只是"日汇总 vs 预算上限"比较(美元级),不涉及账务。引入 shopspring/decimal 会多一条依赖,得不偿失。仓库里全局也没用 decimal 库。
6. **`LLMError` 场景仍会 ACK**:实现决策是"LLM 超时/报错 → channel 回'暂时回不上来' + ACK"。保护钱包比保护一次响应机会重要。真 crash(panic)才不 ACK 让下次重启重放 —— 这是 eventbus 语义兜底,不是我们代码里的显式路径。

#### 未覆盖的事(本 PR 没做,也不在范围内)
- **集成测试**:`handleEvent` 端到端(DB + Redis + 真消息)没写。理由:CI 还没有标准化 Redis/MySQL testcontainer 的模式;本 PR 的风险面集中在"跨 org 隔离"由类型系统已经把住,集成侧的"连得上 / 落得了库"放到第一次真上线后 smoke 测即可。单测覆盖了 scoped 静态断言、LLM factory 校验、tool schema 无 scope 参数、pricing、mention CSV 解析、IsErrorResult 等 —— 最容易被重构 / 回归意外破坏的点。
- **rate limit 超限的自动恢复**:超限后当天不再响应,零点自然解锁(`time.Date(..., 0, 0, 0, 0, time.UTC)` 比较)。没有"管理员手动提额"API,有需要再加。
- **llm session 缓存 / streaming 响应**:按决策表,第一版不做。长回复(>2048 tokens)会被 `MaxOutputTokens` 截断,用户看到"被切断"的回复 —— 观察一段时间决定是否做 streaming。

---

## PR #9':Channel 共享文档区 + 独占编辑锁 ✅ 已完成(2026-04-25)

> 重新定义于 2026-04-25。原"Channel archive → KB 晋升"挪到 [PR #13'](#pr-13channel-archive--kb-晋升)
> —— 拆分原因:协作过程缺"共同对一份产物负责"的载体,先把共享文档做好,归档晋升才有意义。

### 目标
Channel 内引入"共享文档"原语,让 channel 成员能共建 PRD / 会议纪要 / 运维手册等产出,
配独占编辑锁 + 版本历史保护并发,跟 messages / kb_refs / task_submissions 平级。

### 对应设计
[§3.11 Channel 共享文档区 + 独占编辑锁](collaboration-design.md#311-channel-共享文档区--独占编辑锁pr-9)

### 依赖
- PR #2(channels / channel_members)
- PR #11'(system_event 卡片)—— 锁 / 保存事件复用此机制广播

### 范围
- 新增 3 张 MySQL 表:`channel_documents` / `channel_document_versions` / `channel_document_locks`
- 新增 `internal/channel/service/document_service.go`(权限 / 锁 TTL / 版本写入 / 归档过滤)
- 新增 `internal/channel/repository/document.go`(MySQL `INSERT IGNORE + 条件 UPDATE` 两步原子抢锁)
- 11 个 HTTP 端点(/api/v2/channels/:id/documents/...)—— CRUD + lock × 4 + version × 3
- 新增 5 种 channel system_event:`channel_document.{created,locked,unlocked,updated,deleted}`
- 复用现有 `ossupload.Client` / 事件总线 / eventcard writer

### 验收标准
- [x] A 抢锁 → B 抢锁 409 + 看到 A 持锁 + 过期时间
- [x] A save 新版 → B save 返 `ErrChannelDocumentLockNotHeld`
- [x] A 同 sha256 重复 save → 返已有版本,Created=false,OSS 不重写
- [x] 锁 1ms TTL → sleep 20ms → B 可抢
- [x] channel owner 强制解锁 / 普通成员锁过期才能强制
- [x] channel archive 后所有写返 `ErrChannelArchived`,读 OK
- [x] 非 channel member 读写均返 `ErrForbidden`
- [x] 软删后 List 不返,Find 仍可拿到带 DeletedAt
- [x] 并发抢锁:同时 2 个 goroutine 抢同文档,只有一个赢

### 注意事项
- **悲观独占锁不上 OT/CRDT**:工程量差 5–10 倍,MVP 不上;后续要做实时多人光标可平滑升级到 section_id 锁粒度
- **锁不绑设备**:同一 user 跨终端冲突时,后到的设备需 force unlock;MVP 不优化此场景
- **保存不自动解锁**:用户可能连续编辑多版;显式 release 才解锁。前端关页面靠 unbeforeunload 触发 DELETE 兜底
- **MCP 工具暂不暴露**:v1 只 Web 用,agent 走 task_submission 通路;后续 PR 再补 by-principal 变体
- **OSS key 约定**:`<prefix>/<orgID>/channel-docs/<docID>/<sha256>.{md|txt}` —— 同 hash 同 doc 自然 dedup

---

## PR #14':MCP pull 链路补全 ✅ 已完成(2026-04-25)

> 把"用户主动 pull"路径上明显缺的 4 块一起补完(KB 内容 / 共享文档 / mention / dashboard 聚合);
> 不做服务端推送类的实时性。落地后 MCP tool 数量 12 → 22。

### 目标
让 agent 在一次对话里能完整覆盖 pull 模式的核心场景:
1. 看 KB 文档全文(原 KB 只能 list,看不到 chunk 内容)
2. 参与 channel 共享文档协作(原只 Web 用)
3. 看跨 channel 被 @ 的消息(原只能进每个 channel 翻)
4. 一站式入口 "我有什么要处理"(原要调 4 个 list_* tool)

### 对应设计
- [§3.6.3 暴露的 Tools](collaboration-design.md#363-暴露的-tools)(已扩到 22 个)
- 上下文讨论见 conversation 转录(2026-04-25 优先级排序:KB 死链 + 共享文档 MCP 化最痛,先做)

### 依赖
- PR #5'(MCP server + bearer auth + 10 个 tool 基础)
- PR #9'(channel 共享文档 service)
- PR #4'(channel_message_mentions 表 + KB ACL 过滤已落)
- 已有 document repository(GetByID / ListByOrg 含 KnowledgeSourceIDs M3 ACL)

### 范围

**底层 repository(3 个新方法)**:
- `document.repository.ListChunksByDocOrdered(orgID, docID)` —— 按 chunk_idx ASC 拉文档全部 chunks(显式 SELECT 不读 vector 列,避开 GORM SELECT *)
- `channel.repository.ListMentionsByPrincipal(principalID, sinceMessageID, limit)` —— JOIN channel_message_mentions + channel_messages,跨 channel 列被 @
- `channel.repository.ListKBSourceIDsForChannel / ListKBDocumentIDsForChannel` —— channel 可见 KB 集合(source / 直接挂 doc 分两路)

**Service 层(channel.service.documentService 重构 + 加 ByPrincipal)**:
- helper 抽 `resolveChannelMemberByPrincipal` / `resolveOpenChannelMemberByPrincipal`,旧 by-user helper 改成 `lookup → byPrincipal`
- 新增 7 个 ByPrincipal 方法:`Create / List / Get / GetContent / AcquireLock / SaveVersion / ReleaseLock`
- 新增 `SaveVersionByPrincipalInput`(差别仅 `ActorPrincipalID` vs `ActorUserID`)
- `MessageService.ListMyMentionsByPrincipal` + `MentionItem` 类型

**MCP 层(facade / adapter / tool 全套)**:
- `facades.go`:扩 KBFacade(+ListKBDocuments/+GetKBDocument/+`KBDocumentContent` 类型)+ ChannelFacade(+ListMyMentions)+ 新 DocumentFacade
- `kb_adapter.go`:加 ChannelRepo / DocumentRepo 字段;实现可见集 = (source 集 ∪ 直接挂 doc 集);权限失败统一 ErrForbidden(不暴露 doc 是否存在)
- `channel_adapter.go`:+ListMyMentionsForPrincipal(透传)
- 新 `document_adapter.go`:7 个方法直接透传到 channel.service.DocumentService
- `tool_kb.go` 扩 2 个 tool;新 `tool_document.go`(6 个)/`tool_mention.go`(1 个)/`tool_dashboard.go`(1 个)
- `server.go`:新 register 三段(Document / Mention / Dashboard)

**Wire(cmd/synapse/main.go)**:
- 加 `mcpDocumentRepo := docrepo.New(pgDB)`(repo 无状态,新建独立实例 OK)
- mcp.Deps 加 `DocumentSvc: &mcp.DocumentAdapter{...}` 和 `KBSvc.ChannelRepo / DocumentRepo`

### 落地的 10 个新 tool
- KB:`list_kb_documents` / `get_kb_document`(都不接 vector,走 LIKE / chunk 拼接)
- 共享文档:`list_channel_documents` / `get_channel_document`(可附 content) / `create_channel_document` / `acquire_channel_document_lock` / `save_channel_document` / `release_channel_document_lock`
- Inbox:`list_my_mentions(since_message_id?, limit?)`
- 聚合:`get_my_dashboard(task_limit?, mention_limit?, channel_limit?)`

### 验收标准
- [x] `go build ./...` + `go vet ./...` 全绿
- [x] 新 service 集成测试 3 个 PASS:
  - `TestChannelDocumentByPrincipal_HappyPath` —— A create → list → acquire → save → 同 hash 幂等 → release → B acquire 成功
  - `TestChannelDocumentByPrincipal_NonMemberForbidden` —— 非成员 list/get/acquire/save 全 ErrForbidden;principalID=0 也拒
  - `TestChannelDocumentByPrincipal_ArchivedReadOnly` —— 归档 channel 读 OK,create/acquire/save 全 ErrChannelArchived
- [x] 已有所有 ChannelDocument / channel.repo / document.repo 集成测无回归

### 注意事项
- **不做向量搜索(`search_kb`)**:需独立 retrieval 模块,工程量过大,留待。当前 `list_kb_documents` 走 `LOWER(title/file_name) LIKE` 兜 80% 场景;真要语义检索就开新 PR
- **`get_kb_document` chunks 拼接是损失版**:按 chunk_idx ASC 用 `\n\n` 连接,丢失部分原版面(代码块边界、表格);LLM 阅读够用,无损还原留给 retrieval
- **共享文档 MCP 只 6 个核心动作**:不暴露 heartbeat / force_release / version_list / get_version_content / soft_delete —— 心跳由 MCP 调用本身的短回合代偿(锁 10min TTL 已够单次保存),治理动作走 Web
- **KBAdapter 直接吃 repository,跳过 service 层**:channel.service 没"按 channel 看 KB"业务,document.service 当前只 upload;直接组合 repo 是最直接的做法,只发生在 MCP 这一层,不污染 service
- **可见集只支持 source 级挂载列出**:直接挂 doc 的(`channel_kb_refs.kb_document_id`)在 `list_kb_documents` 不合并(避开 N+1 doc 元数据拉取),只在 `get_kb_document` 校验权限时纳入
- **dashboard 顺序调 4 次,不并发**:errgroup 复杂度对当前轻量查询不值得;失败整体返错(无 partial result),LLM 易理解
- **`list_my_mentions` 不做"已读"**:语义留给未来 PR #10' Inbox;当前用 `since_message_id` 做"自上次最大 id 之后"增量

### 实施后记
- 重构 documentService helper 时一开始想 surgical 给每方法复制粘贴,但很快发现 7 个方法逐个改不如重构 helper(`resolveOpenChannelMemberByPrincipal`)+ 旧 by-user 改成 wrap,代码复用度高
- `list_kb_documents` 用 source ACL 过滤直接打到 `document.repository.ListByOrg.KnowledgeSourceIDs`(M3 已落地的 ACL 入口),发现 source 集为空时要短路返 nil(否则会退化成"全 org 文档")
- `get_kb_document` 权限失败一律返 ErrForbidden 不返 ErrDocumentNotFound,防侧信道泄露 doc 存在性
- chunk 表 SELECT * 会触发 vector 列拉取,显式 `Select("id, doc_id, ...")` 列字段避开

---

## PR #13':Channel archive → KB 晋升

> 从原 PR #9' 拆分而来(2026-04-25)。共享文档(PR #9')和 task_submission 都晋升,采用同套
> "OSS 共享 + ingestion 跳 fetch"逻辑。等 PR #9' 落地后启动。

### 目标
Channel 归档时,把所有 **共享文档**(`channel_documents` 未删)+ 所有 **approved task** 的最新
`task_submission` 一并晋升到 org KB;channel_kb_refs 关联失效。

### 对应设计
[§3.10 Channel 归档 + KB 回流](collaboration-design.md#310-channel-归档--kb-回流)

### 依赖
- PR #4'(tasks + submissions)
- PR #9'(channel_documents)

### 范围
- `POST /api/v2/channels/:id/archive` 同步:改 channel 状态 + 发 `channel.archived` 事件 + 调度 asyncjob
- 新增 asyncjob kind `channel_archive_promote`,runner 内:
  - 列 channel 下 `status='approved'` 的 tasks → 取最新 submission → 晋升
  - 列 channel 下未删 `channel_documents` → 取 current 版指针 → 晋升
  - 每条晋升:`documents` 新行 `(org_id, source_type='channel-promote', source_id='submission:<id>'|'channel-doc:<id>')`,oss_key 指向源 key
  - 触发 ingestion 跳 fetch 路径(复用 upload fetcher 模式)
- 决议中:KnowledgeSource 归属 —— **每 channel 一个** vs 每 org 共享;需要再讨论

### 验收标准
- [ ] 3 个 approved task + 2 个共享文档的 channel 归档 → 5 个 documents 行,oss_key 对应
- [ ] Rejected / cancelled task 不晋升;软删的共享文档不晋升
- [ ] 重复 archive 已归档 channel 返 200 不重复晋升(`source_id` UNIQUE)
- [ ] Archive 后,`list_channel_kb_refs` 返空;共享文档 API 仍可读不可写
- [ ] asyncjob 失败可重跑;单条晋升失败不阻断其他

### 注意事项
- **不搬字节**:OSS 里一份,documents.oss_key = 源 key
- **异步晋升**:embed 慢,Archive 不能挂 HTTP;走 asyncjob,完成后发 `channel.archive_promoted` 事件
- **历史可见性**:归档后 channel 本身的 messages / tasks / submissions / 共享文档都保留可读
- **跨 channel 同 hash dedup 暂不做**:首版只按 source_id 幂等;未来加 content_hash 再优化

---

## PR #10':审批闭环 + Reviewer 通知 + Inbox

### 目标
让 reviewer 知道有东西要 review;提供跨 channel 的"我的待办"视图。

### 对应设计
[§4 Inbox / 通知](collaboration-design.md#4-容易被忽略但会踩的设计点)

### 依赖
- PR #4'(task_reviewers / task_reviews)

### 范围
- `GET /api/v2/users/me/inbox`:返 "我是 reviewer 且 task submitted 等审" + "我被 @ 未读" + "我的 task 状态变化"
- 邮件通知(复用现有 email sender):可选,默认关
- 纯 pull:reviewer 自己查 inbox / 前端定期轮询,或等 Synapse-Web 独立 WebSocket 通道(非本 PR 范围)

### 验收标准
- [ ] Alice 作为 Bob task 的 reviewer,Bob submit 后 Alice inbox 出现一条
- [ ] Alice review 完,该条消失
- [ ] `@Alice 帮忙 review` 未读条数能减(需要定义"已读")

### 注意事项
- **已读状态怎么存**:新一张 `user_inbox_reads(user_id, kind, ref_id, read_at)`;pragmatic,不过度设计
- **邮件默认关**:MVP 不骚扰用户,靠 pull inbox 兜底

---

## PR #11':Channel 协作时间线 · system_event 消息卡片 ✅ 已完成(2026-04-24)

### 验收记录

一次性 10 条用例逐条 web 操作 + DB 核验,14 种 event_type 全部出卡片:

| 用例 | 覆盖 |
|---|---|
| 1 UI 新建 task | `task.created` ✅ |
| 2 认领 | `task.claimed` ✅ |
| 3 提交(无审批直跳 approved) | `task.submitted` ✅ |
| 4 新建再取消 | `task.created` + `task.cancelled` ✅ |
| 5 改 assignee | `task.assignee_changed` ✅ |
| 6 改 reviewers | `task.reviewers_changed` ✅ |
| 7 claim/submit/review approve | `task.claimed` + `task.submitted` + `task.reviewed(approved)` ✅ |
| 8 移除/加回/改角色 | `channel.member_removed` + `member_added` + `member_role_changed` ✅ |
| 9 解挂/重挂 KB | `channel.kb_detached` + `kb_attached` ✅ |
| 10 新建 channel 再归档 | `channel.archived`(写入时刻比 `archived_at` 晚 12ms,证实 PostSystemEvent 绕过 archived guard 生效)✅ |

额外观察:
- `source_event_id` UNIQUE 幂等工作;多次部署不生重复卡片
- `channel_messages.kind='system_event'` 的 body JSON 按 `{event_type, actor_principal_id, detail}` 约定正确
- Synapse 代派场景:虽然本轮没专门测,但之前 E2E round 1 里已经看到 LLM 的自然语言回复 + task_event 卡片并存
- 未测的小变体(可后续补):`task.reviewed(request_changes)` / `task.reviewed(rejected)` 的黄/红色样式、归档 project 级联发出的 `channel.archived` 卡片(底层逻辑已验,前端样式同单个归档)

---

### 目标

把 **task 生命周期 / channel 成员变更 / 知识库挂载变更** 等结构化事件以 `kind=system_event` 消息卡片形式落在 channel 里,让 channel 成为所有成员可见的**协作时间线** —— 不用翻 Tasks / Members / KB tab 就能知道最近发生了什么。用户报告:"Claude 通过 MCP 创建任务后 channel 是静默的,只有切到 Tasks tab 才看到"。

### 对应设计
**新增** `collaboration-design.md §3.7 协作时间线`(本 PR 一并写)。现行设计未覆盖。

### 依赖
- PR #3 ✅ eventbus(已有 Redis Streams 基础)
- PR #4' ✅(`channel_messages.kind` 字段已存在;task 事件已 publish 到 `synapse:task:events`)
- PR #2 ✅(channel/member/kb_ref 基础设施)
- **本 PR 需要补**:channel/member 和 channel/kb_ref 两个 service 目前**不 publish 事件**,要补接入 `synapse:channel:events`(复用现有 stream,按 `event_type` 区分)

### 架构选择:事件总线 consumer(B 方案)

service 层保持纯粹**不直接调** message service,而是**publish 事件到 Redis Streams**;新增一个 `channel-event-card-writer` consumer group 订阅这些事件,转成 `kind=system_event` 消息写回 channel。

优势:
- task / member / kb_ref service 互不依赖(避免循环 import)
- 故障面小 —— consumer 挂了不影响主业务,重启自动 replay PEL
- 对齐 memory `project_event_bus_choice.md`("事件总线走 Redis Streams + consumer group")

### 覆盖的 15 个事件

| 事件源 | event_type | 触发点 |
|---|---|---|
| task(7) | `task.created` | `TaskService.Create / CreateByPrincipal` 成功后 |
| | `task.claimed` | `Claim*` 成功后 |
| | `task.submitted` | `Submit*` 成功后 |
| | `task.reviewed` | `Review*` 成功后(detail 带 decision) |
| | `task.cancelled` | `Cancel` 成功后 |
| | `task.assignee_changed` | `UpdateAssignee` 成功后 |
| | `task.reviewers_changed` | `UpdateReviewers` 成功后 |
| member(3) | `channel.member_added` | `MemberService.Add` 成功后 |
| | `channel.member_removed` | `MemberService.Remove` 成功后 |
| | `channel.member_role_changed` | `MemberService.UpdateRole` 成功后 |
| kb_ref(2) | `channel.kb_attached` | `KBRefService.Add` 成功后 |
| | `channel.kb_detached` | `KBRefService.Remove` 成功后 |
| channel(1) | `channel.archived` | `ChannelService.Archive`(级联归档发一条到对应 channel) |

> 原计划的 `channel.purpose_changed` 暂不做:channel_service 没有 `UpdatePurpose` 方法(purpose 只在 Create 时设置),没有 handler 触发点。后续若加"改 purpose"功能再一起加事件。

总计 **14 个事件**(task 7 + member 3 + kb_ref 2 + channel 1 + archive 1)。

### 范围

**后端 —— 事件发布补齐**

- `synapse:channel:events`(已有,目前只装 `message.posted`):增加上述 member / kb_ref / channel_archived / channel_purpose_changed 事件,公用字段 `event_type / org_id / channel_id / actor_principal_id / detail(JSON)`
- `synapse:task:events`(已有):保持不动,已 publish task 全部 7 个事件
- Member/KB service 加 publisher 依赖(和 channel/message service 注入模式一致)

**后端 —— 新 consumer `channel-event-card-writer`**

- 位置:`internal/channel/eventcard/` (或类似路径)
- 启动:在 `cmd/synapse/main.go` 启动,和 agentsys orchestrator 并列跑
- 订阅:`synapse:channel:events` + `synapse:task:events` 两个 stream,同一 consumer group `channel-event-card-writer`
- 处理逻辑:
  1. 过滤:跳过 `event_type=message.posted`(不可能把消息事件再转成消息,死循环)
  2. 组装 body JSON(见下)
  3. 查 channel 是否已归档 → 归档则 ACK 丢弃(归档 channel 不写新消息,保持语义一致)
  4. 幂等:`channel_messages` 加 `source_event_id VARCHAR(64) NULL UNIQUE`,写入前用此键去重;重放不会产生重复卡片
  5. 调 `MessageService.PostAsPrincipal(channelID, actorPID, bodyJSON, nil, 0)`,kind=`system_event`
  6. ACK
- 失败重试:gorm 异常 / publisher 挂 → 不 ACK,PEL 里重放;消费持续失败超阈值写 log,不阻塞其他事件

**body JSON 约定**

```json
{
  "event_type": "task.created",
  "actor": { "principal_id": 1, "display_name": "Eyri He", "kind": "user" },
  "detail": {
    "task_id": 9,
    "task_title": "测试 G3",
    "assignee": { "principal_id": 3, "display_name": "郝哥-瓜摊老板" }
  }
}
```

冗余 `display_name`:前端不需要再查 principal directory;历史原貌(即使后续改名)保持。

**DB 改动**
- `channel_messages` 加 `source_event_id VARCHAR(64)` + `UNIQUE INDEX uk_channel_messages_source_event`
- `channel_messages.kind` 新增合法值 `system_event`(VARCHAR 列,无 schema 变更)

**前端**
- `types/api.ts` `ChannelMessageResponse.kind` 枚举加 `'system_event'`
- `pages/channel/MessagesTab.tsx` 渲染分支:`kind === 'system_event'` 走 `<SystemEventCard>` 组件,不走气泡
- 新组件 `components/channel/SystemEventCard.tsx`:按 event_type 渲染图标 + 文案 + 点击跳转
  - 图标映射:`task.created→🆕` / `task.claimed→✋` / `task.submitted→📝` / `task.reviewed→✅/↩/🛑`(按 decision)/ `task.cancelled→🚫` / `task.assignee_changed→🔀` / `task.reviewers_changed→👥` / `channel.member_added→➕` / `channel.member_removed→➖` / `channel.member_role_changed→🎭` / `channel.kb_attached→📎` / `channel.kb_detached→📤` / `channel.archived→📦` / `channel.purpose_changed→✏️`
  - 未知 event_type 降级显示"未知事件类型:{event_type}"

**Synapse 代派双消息共存**
- Synapse 代派场景 LLM 调完 create_task 后自己 post_message 回自然语言("已派给郝哥...")—— 本 PR**不改**这行为
- 会出现:一条 `system_event` 卡片 + 一条自然语言气泡,互补(结构化事件 vs 对话回复)
- 未来若要合并,在 Synapse prompt 或 tool description 上调整,不在本 PR 范围

### 验收标准

- [ ] MCP create_task / claim_task / submit_result / review_task → channel 出现对应 system_event 卡片,author = 执行操作的 agent
- [ ] UI 手动创建/认领/提交/审批 task → 卡片出现,author = 操作 user
- [ ] UI 加/删/改角色成员 → 对应卡片出现
- [ ] UI 挂载/解除 KB source → 对应卡片出现
- [ ] 归档 channel → channel 里最后一条是 `channel.archived` 卡片;之后不再有新卡片(post 被 archived guard 挡)
- [ ] Consumer 崩溃重启 → PEL 里的事件被 replay,且不产生重复卡片(source_event_id UNIQUE)
- [ ] 前端卡片未知 event_type → 不崩,降级显示
- [ ] 已删除 agent 的事件(事件发生时 agent 仍在):display_name 冗余保留,卡片仍正确展示

### 注意事项

- **事件风暴**:第一版不合批,每事件一卡。若实战太吵(短时多次改 reviewer)再考虑 100ms 内合并同 event_type 同 actor
- **Consumer 就位顺序**:main.go 里 consumer 必须在 publisher 就绪后启动;和 agentsys orchestrator 并列跑,互不阻塞
- **作者 actor 校验**:consumer 写消息时用 `actor_principal_id` 作为 author;如果该 principal 已删除(agent 被删),fallback 到 Synapse 顶级 agent(pid=7),避免 FK 悬空
- **channel 归档后的事件**:consumer 检查 channel.archived_at → 直接 ACK 丢弃(归档后只写 "channel.archived" 那最后一条,其他后续事件若来自级联/定时任务,不再回显)
- **事件顺序**:Redis Streams 单 consumer 顺序消费,同 channel 内事件按 XADD 顺序写卡片,保证时间线无乱序
- **前端 scroll**:新 system_event 卡片也算新消息,触发 MessagesTab 自动滚到底

---

## PR #12':Channel 消息表情反应(reactions)✅ 已完成(2026-04-24)

### 验收记录

5 条用例走完,4 条通过 / 1 条"软通过":

| 用例 | 结果 |
|---|---|
| 1 UI 打一个新 emoji | ✅ pill 立即出现,无需手动刷新 |
| 2 Claude 通过 MCP 对同条消息打同一 emoji | ✅ pill 合并成 `👍 Eyri He, Eyri He 的 Claude`(多人) |
| 3 点自己打过的 pill 切换撤销 | ✅ 我的那条删掉,pill 变为只剩 Claude |
| 4 system_event 卡片也能打 reaction | ✅ message_id=37(task.created 卡片)下成功打 👍 |
| 5 非白名单 emoji(🍕) | 🟡 **客户端拒了** — Claude 读懂 tool description 的 "Allowed emojis" 主动挡住,没发到后端;后端 `ErrReactionEmojiInvalid` 守卫仍在,走 curl 可触发 |

### 本次实施里发现 + 修的 2 个 bug

- **UserProfile.PrincipalID 缺字段**:前端 `me.principal_id` 被使用(判断"这条 reaction 是不是我打的"),但后端 `UserProfile` DTO 只返 `id`(user.id),`me.principal_id` 运行时是 undefined → `Number(undefined) = NaN`,pill 判定失效。**修**:后端 `service/types.go:UserProfile` 加 `PrincipalID uint64 json:"principal_id,string"`,`profile.go:toUserProfile` 映射;前端 `types/api.ts:UserProfile` 加 `principal_id: string`。
  - **遗留**:前端 `MessagesTab.tsx:327` 用 `Number(me.id) !== m.author_principal_id`(比较的是 user.id 和 principal_id,语义错)—— 种子数据里 user.id=1 和 principal_id=1 碰巧相等没暴露,后续一并修成 `me.principal_id`。

- **fetchLatest 增量拉不更新已有消息的可变字段**:原实现 `items.filter(m => m.id > maxID)` 只 append 新 id。reaction 发生在旧消息上,id 不变只 reactions 变,所以 UI 看不到。**修**:`fetchLatest` 改成"已有 id 用新数据覆盖,新 id 再 append",同时兼顾增量滚动 + 旧消息字段同步刷新。

---

## PR #12' 原规划(设计说明)

### 目标
channel 消息和 system_event 卡片都支持打 emoji 反应。降低"收到" / "赞"这种轻反馈的噪音成本。Slack / Discord / 飞书风格。

### 对应设计
新增 `collaboration-design.md §3.8 Reactions`(本 PR 一并写)。

### 依赖
- PR #4' ✅(channel_messages 表已在)
- PR #11' ✅(kind=system_event 卡片,reactions 同样挂在它上面)

### 范围

**DB** — 新表 `channel_message_reactions`:
```
message_id      BIGINT  (FK channel_messages.id, ON DELETE CASCADE)
principal_id    BIGINT
emoji           VARCHAR(16)
created_at      TIMESTAMP
PRIMARY KEY (message_id, principal_id, emoji)
```
复合 PK:同一人对同一消息同一 emoji 只能打一次;可打多个不同 emoji。

**Emoji 预设集合**(12 个,前端硬编码):
```
👍 👎 ❤️ 🎉 🚀 👀 🙏 😂 🔥 ✅ ❌ 🤔
```
不用全 Unicode picker,也不让用户自定义(MVP 简单 + UI 紧凑)。后端按允许列表拦非法 emoji 避免垃圾数据。

**后端**:
- `channel/model/reaction.go` 新 model
- `channel/repository`:`AddReaction / RemoveReaction / ListReactionsByMessages`(批量给 List 接口用)
- `channel/service/message_service.go`:
  - `AddReaction(ctx, messageID, callerUserID, emoji) error`
  - `RemoveReaction(ctx, messageID, callerUserID, emoji) error`
  - `AddReactionByPrincipal / RemoveReactionByPrincipal`(MCP 用,agent 允许打)
  - `MessageWithMentions` 加 `Reactions []ReactionEntry` 字段;List 接口批量 fetch 填充
- Handler:`POST /api/v2/messages/:id/reactions { emoji }` / `DELETE /api/v2/messages/:id/reactions/:emoji`
- MCP tool:`add_reaction(message_id, emoji)` / `remove_reaction(message_id, emoji)`—— agent 允许打

**不做**:reaction 的 eventbus publish。reactions 不走时间线、不生 system_event 卡片、不推送。纯 DB + HTTP/MCP 读写。

**前端**:
- `types/api.ts` 加 `ReactionEntry { emoji: string, principal_ids: number[] }`;`ChannelMessageResponse.reactions?: ReactionEntry[]`
- `api/channel.ts` 加 `addReaction / removeReaction`
- 新组件 `components/channel/ReactionBar.tsx`:
  - 展示每个 emoji 一个 pill:`👍 Eyri He, 郝哥-瓜摊老板`(按 principal_ids 查 displayName,列出)
  - pill 右侧"➕"按钮 hover 展开 emoji picker(12 个候选)
  - 点 pill:当前用户是否已打 —— 已打 → remove;没打 → add
  - 点 picker 里的 emoji:add
- 嵌入:`MessagesTab` 的文本气泡下方 + `SystemEventCard` 右下角。UI 样式统一,所有消息都可打

### 验收标准
- [ ] 用户 A 打 👍 → 消息下方出现 `👍 A`
- [ ] 用户 B 打同一 👍 → pill 合并 `👍 A, B`
- [ ] A 再点 👍 → pill 变 `👍 B`(自己的反应移除)
- [ ] 同一用户对同一消息打多个不同 emoji → 并列显示多个 pill
- [ ] 非预设 emoji API 调用被拒(400)
- [ ] Claude 通过 MCP 打 reaction → 显示 `👍 Eyri He 的 Claude`
- [ ] System_event 卡片也能打 reaction(和文本气泡行为一致)
- [ ] 删消息时 reactions 级联删(FK ON DELETE CASCADE 或 service 层事务)

### 注意事项
- **Emoji 白名单**:后端 service 层校验 emoji ∈ 预设集合,否则返 `ErrReactionEmojiInvalid`。前端多一层防但不能只信前端
- **权限**:打 reaction 要求 caller 是 channel 成员(和读消息同 guard);归档 channel 不允许新增 reaction(语义一致)
- **批量 List 优化**:`ListMessages` 走 IN 批拉 reactions,避免 N+1。聚合成 `map[message_id][]ReactionEntry` 返给 handler
- **display_name 冗余?**:reaction pill 显示 displayName,直接让前端用现有 `principalDirByID` 查(和 MessagesTab 其他地方一致),后端 API 只返 `principal_id` 列表不冗余 display_name

---

## 后续 / 开放(见 design §6)

方向转向后搁置的能力:
- 代码 / 多文件产物格式(现仅 md / text)
- Task 之间显式依赖(task_dependencies 表)
- Channel reopen 语义
- 跨 org 协作
- 专项系统 agent 的 org-wide marketplace
- 职能角色 / team tag
- 个人 agent 的"被动代言"(owner 离线时代理响应)
- 系统 agent prompt / 权限的 org admin 配置界面

---

## 跨 PR 的通用要求

- **日志**:按 memory `feedback_logging_style.md`,`ctx` 必传,不手填 `trace_id` / `user_id` 字段
- **密钥 / 配置**:按 memory `feedback_local_deploy_secrets.md`,committed yaml 留空串,真值走 `config.local.yaml`
- **回复语言**:按 memory `user_language.md`,PR / 提交信息 / doc 都中文
- **测试层次**:每个 handler 至少 1 个集成测试(走 HTTP + DB);核心 service 单元测试覆盖 >80%
- **迁移脚本**:每个 PR 自带 migration,使用现有的迁移框架(见 `internal/*/migration.go` 模式)
