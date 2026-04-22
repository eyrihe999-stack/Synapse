# Synapse 网关架构决策(ADR)

> **状态**:**已废弃**(2026-04-21 后续清理)。以下 ADR 描述的"OAuth 2.1 AS / MCP 代理 / token 透传 / agent 分类"等能力已全部从代码库移除,仅作为历史档案保留,用于未来重建业务层时参考设计思路。
>
> **当前实际架构**(2026-04-21 之后):Synapse 退化为"基础设施单体" —— 只有 `user`(含 Google OIDC 登录) + `organization`(orgs + org_members,无 RBAC) + `asyncjob` framework + `ingestion` 骨架。没有 OAuth AS,没有 MCP 代理,没有 agent 机制。身份由 web JWT 独立承担。
>
> **原 ADR 一句话总览**(仅供参考):Synapse 主进程只做网关,一切 MCP 业务能力由独立部署的 agent 进程提供;授权由 OAuth 证身份 + 业务模块按需做简单归属判断。

---

## 1. 背景与动机

原架构把"向量检索 / 数据摄取 / workflow 编排"等能力直接做成 Synapse 主进程内的 MCP tools,暴露在 `/api/v2/retrieval/mcp` 端点。代价:

- **主进程业务堆叠**:tools 数量随产品演进线性增长,主进程变成一个"什么都干"的单体
- **OAuth scope 粒度错位**:尝试用 scope 细化"能读 / 能写 / 能调哪个 tool",但 consent 页授权的对象是 MCP client(Claude Desktop),它不是调这些 tool 的最终决策者(决策者是背后的 agent 实现),scope 层缺少"可决策的主体"
- **权限模型双头**:主进程 MCP tools 走一套 OAuth scope + 资源检查,第三方 agent 走另一套 org RBAC,审计 / 撤销 / 变更路径不统一

目标架构把**主进程瘦身为网关**,所有业务能力下沉到 agent,授权模型统一。

---

## 2. Synapse 主进程的职责

### 2.1 做什么

| 能力 | 端点 / 模块 |
|---|---|
| OAuth 2.1 AS(身份发行) | `internal/oauth` + `/oauth/*` + `.well-known/*` |
| Org / User / Member 管理 + RBAC | `internal/organization` + `internal/user` |
| 数据摄取(文档 / 代码 / 未来图片等) | `internal/document` + `internal/code` + REST API |
| 向量库 + 元数据存储 | PostgreSQL(pgvector)+ MySQL |
| Agent 注册表 + 发布审核 | `internal/agent`(agents / agent_publishes 表) |
| Agent MCP 代理 | `/api/v2/agents/:owner_uid/:agent_slug/mcp`(WS hub + HTTP fallback) |

### 2.2 不做什么

- **不提供任何 MCP tools**:`/api/v2/retrieval/mcp` 与 legacy `/api/v2/orgs/:slug/retrieval/mcp` 下线
- **不做 workflow 编排 tool**:如需编排,由 orchestrator 类 agent 提供
- **不为具体业务能力定义 OAuth scope**:scope 层只保留一个 `agent:invoke`

---

## 3. Agent 分类与边界

**系统 agent 和第三方 agent 在技术栈上完全一致** —— 都是独立进程、统一走 MCP proxy、统一用 token 透传回调 Synapse REST API。差别只在**开发归属**与**运营动作**:

| 维度 | 系统 agent | 第三方 agent |
|---|---|---|
| 开发者 | Synapse 官方 | 用户 / 外部开发者 |
| `agents.owner_uid` | 保留值(如 `0`) | 真实用户 uid |
| 发布机制 | 白名单自动注入所有 org,免审核 | 发布到 org 需 admin 审核 |
| 信任加固 | 默认可信 | 审核 + 运行时审计 + token 短 TTL |
| MCP 入口 | `/api/v2/agents/0/:slug/mcp` | `/api/v2/agents/:owner_uid/:slug/mcp` |
| 部署位置 | Synapse 团队独立部署 | 开发者自建 |
| 回调 Synapse 的身份链 | 一致,token 透传 | 一致,token 透传 |

**典型系统 agent**(规划,非定稿):
- `kb_retrieval_agent`:向量检索
- `kb_ingest_agent`:文档 / URL 摄取
- `workflow_agent`:多轮任务编排(替代原 workflow MCP tool)

---

## 4. 授权模型

### 4.1 OAuth(身份认证)

**职责**:证明"这个请求是哪个 user 代表哪个 org 发起的"。

- Access token JWT 的 `sub=user_id`,自定义 claim `org=org_id`(在 authorize 时用户选定并固化)
- 单一 scope:`agent:invoke` —— 粗粒度声明"持此 token 可调 agent 功能"
- **OAuth 层不决定**能调哪个 agent、能读写哪条数据

### 4.2 业务层归属判断

不再维护 `org_roles` / `org_member_role_history` 表和权限点常量。每个业务端点只需回答两个问题:

- **是不是成员?** —— `OrgService.IsMember(ctx, orgID, userID)` 或通过 `OrgContextMiddleware` 一次性注入 `*Org`。
- **是不是 owner?** —— 直接比较 `org.OwnerUserID == userID`(用 `handler.RequireOwner` 中间件做前置守卫)。

更复杂的授权(per-agent ACL、可审计的角色变更等)留到真的需要时再引入,不做投机设计。

### 4.3 跨 org 原则

成员身份校验始终用 URL 路径里明示的目标 org,不是 token 里的 org,也不是 agent 发布所在的 org。这让"用户 X 在 org A 登录 → 调 agent M → agent M 尝试写 org B 的 KB"这类请求在 `IsMember(orgB, user_X)` 处被拒。

---

## 5. Agent 回调 Synapse 的身份链

**MVP 方案:token 透传**。

```
Claude Desktop ──[OAuth token(user=X, org=A)]──→ Synapse MCP proxy
                                                    │
                                         根据 :owner/:slug 转发
                                                    ↓
Synapse ──[WS/HTTP + 原 token 透传]──→ Agent 进程(系统 / 第三方)
                                                    │
                                         agent 业务逻辑要写 KB:
                                                    ↓
Agent ──[Authorization: Bearer <同一 token>]──→ POST /api/v2/orgs/A/documents
                                                    │
                                         BearerAuth 解 token → user_X / orgA
                                         IsMember(orgA, user_X)
```

**为什么选 token 透传而非 token exchange(RFC 8693)**:
- Synapse 的 `BearerAuth` 已原生支持 OAuth token / Web JWT 双向兼容,**无需新增端点**
- 归属链天然对齐,agent 不可能越权于调用它的用户
- 撤销即时:用户被移出 org,下一次回调即拒

**固有代价(接受,V2 再加固)**:
- Bearer token 在 agent 进程内停留期间的保存风险 —— OAuth 2.1 标准缓解是 DPoP(RFC 9449)或 mTLS binding
- 短期缓解:access token TTL 设短 + 第三方 agent 强制审核 + M10.4 审计日志记 agent_id + actor_user_id

---

## 6. 与旧架构的下线清单

| 旧实现 | 处理 |
|---|---|
| `internal/retrieval/mcpserver.RegisterOAuthRoutes` | 下线,路由不挂 |
| `/api/v2/retrieval/mcp` | 下线,`scopes_supported` 从 metadata 中移除 |
| `/api/v2/orgs/:slug/retrieval/mcp`(legacy JWT,stdio bridge 过渡) | 下线 |
| `ScopeMCP = "mcp"` 常量 | 换成 `ScopeAgentInvoke = "agent:invoke"` |
| `handler.Config.MCPResourceURL` | 废弃单一 resource URL 概念;改用 RFC 9728 path-suffixed,每个 agent 自己是独立受保护资源 |
| `oauth-protected-resource` metadata 指向 retrieval | 改为响应通配 agent 路径的 metadata |
| `internal/workflow` 作为 MCP tool 暴露的部分 | 由 workflow_agent 接管(或继续作为 REST,但不再出现在 MCP 目录) |

---

## 7. 与 roadmap 的交叉影响

- **M2.5(scope 细化)**:原规划的 `retrieval:read/write` / `workflow:run` 全部删除,只保留 `agent:invoke`;`agent:invoke:<slug>` 细粒度也先不做
- **M4.2(Doc 级 ACL)**:推迟 —— 等有真实多租户场景再按需加回
- **M4.3(agent 调用权限链)**:就是本文第 5 节的"token 透传",升级为明确的 MVP 落地方案
- **M6(Agent 注册)**:`agents` 表需加 `is_system` 语义(或用 `owner_uid = 0` 推导)+ 系统 agent 白名单自动注入逻辑
- **M10.4(审计日志)**:actor_user_id / agent_id / target_org_id 三列必填,对应本架构的身份链追溯

---

## 8. 当前挂账(非阻塞,后续再议)

- **第三方 agent 信任加固**:token 短 TTL / DPoP / 审核流程细化 / 运行时异常告警 —— 等实际有第三方 agent 接入时再设计
- **无人交互的机器摄取**:某些 agent 可能定时自动写 KB(无用户 session),需要独立的 service account 身份系统,不走 OAuth
- **RBAC 重引入**:若出现"同 org 内不同用户权力需要分级"的真实需求(目前只有 owner vs member 两档),再评估是引入 role 表还是在业务侧手工分支
- **Token exchange(RFC 8693)升级路径**:若 bearer token 保存风险变成实际问题,升级到 token exchange + audience 绑定
