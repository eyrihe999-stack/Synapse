# 生产化 Roadmap(按模块组织)

> 目标:把 Synapse 从"功能 MVP"推到能放进企业内网、跑付费客户的生产级状态。
> 组织方式:按模块分章,每个模块三段式 —— **现状 / 核心问题 / 调整方案**。每项带稳定 ID 便于单点追踪。
> 与 `kb-roadmap.md` 的关系:kb-roadmap 聚焦检索质量,本文档聚焦**工程化**(安全 / 稳定 / 观测 / 合规 / 运维),两份并行推进。
> 架构前提:本文档所有权限 / MCP / agent 相关条目,遵循 [`architecture-gateway.md`](architecture-gateway.md) 里定稿的"Synapse 纯网关 + 两层授权"模型 —— 主进程不提供 MCP tools,所有业务能力下沉 agent,OAuth 只证身份,细粒度授权走 org RBAC。

---

## 目录

- M0. [架构过渡任务](#m0-架构过渡任务) —— 对齐 ADR 的一次性迁移清单(阻塞其他演进)
- M1. [身份与登录](#m1-身份与登录) —— 注册 / 邮箱验证 / 密码 / session
- M2. [鉴权](#m2-鉴权) —— JWT + OAuth 2.1 AS + 中间件
- M3. [Org 管理](#m3-org-管理) —— 组织生命周期 + 邀请 + 转让
- M4. [权限管理](#m4-权限管理) —— RBAC + 多租户隔离 + ACL
- M5. [数据摄取](#m5-数据摄取) —— 文档 / 代码 / 图片 → chunk → embed → 索引
- M6. [Agent 注册](#m6-agent-注册) —— 注册 / 审核 / endpoint / 版本
- M7. [连接管理](#m7-连接管理) —— WS Hub + MCP proxy + SSE
- M8. [异步任务](#m8-异步任务) —— runner + retry + 超时 + 死信
- M9. [密钥与加密](#m9-密钥与加密) —— 横切:KMS envelope / 轮转
- M10. [可观测与运维](#m10-可观测与运维) —— metrics / tracing / migration / backup / CI

文档末尾 [跨模块推进节奏](#跨模块推进节奏) 汇总优先级。

---

## M0. 架构过渡任务

### 背景
`docs/architecture-gateway.md`(2026-04-21 定稿)把 Synapse 主进程重定位为**纯网关**:不再提供任何 MCP tools,一切业务能力下沉到独立部署的 agent。本节列出**为对齐新架构必须做的一次性迁移动作**,这些任务在其他模块的"调整方案"里不再重复出现,但会被它们引用为前置。

### 核心问题
- **M0-P1** `/api/v2/retrieval/mcp`(OAuth 保护)和 `/api/v2/orgs/:slug/retrieval/mcp`(legacy JWT,stdio bridge 过渡)把 retrieval 逻辑硬编码在主进程里,与"Synapse 不再提供 MCP tools"直接冲突
- **M0-P2** `ScopeMCP = "mcp"` 语义指向"可调 MCP endpoint 下所有 tools"—— 新架构里这些 tools 不存在,常量名与 metadata 广播的 `scopes_supported` 都成误导
- **M0-P3** `handler.Config.MCPResourceURL` 假设"单一受保护资源",与"每个 agent 就是独立受保护资源"的新模型不符
- **M0-P4** `agents` 表无 `is_system` 语义,系统 agent 与第三方 agent 混居,无白名单自动注入机制
- **M0-P5** MCP proxy 转发到下游 agent 时,是否原样透传 OAuth token 未验证 —— 这是 ADR §5 身份链方案能落地的前提
- **M0-P6** 系统 agent(kb_retrieval / kb_ingest / workflow 等)本体尚未开发,当前由主进程代劳的能力需要完成迁出

### 调整方案
1. ✅ **M0-T1 下线 `/api/v2/retrieval/mcp`**:删 `internal/retrieval/mcpserver.RegisterOAuthRoutes` 注册调用;保留 service 层能力供过渡期系统 agent 内部调用
   - 实际落地:`main.go` 移除注册;`mcpserver/mcpserver.go` 删 `RegisterOAuthRoutes` + HTTP 分发器(`Handle` / `handleToolsList` / `handleToolsCall` / `extractOrgID` / JSON-RPC 信封类型 / 错误码)共 ~150 行;保留 `ToolsList` / `Dispatch` / `ContentBlock` / `ExtraToolHandler` 作 in-process API 供 orchestrator/invoke + workflow/engine 用,等 T7 迁走后整包可删
2. ✅ **M0-T2 下线 `/api/v2/orgs/:slug/retrieval/mcp`(legacy JWT)**:stdio bridge 过渡期结束,`RegisterRoutes` 整个下线
   - 实际落地:同 T1 一起完成,`RegisterRoutes` 函数整个删
3. ✅ **M0-T3 Scope 常量改名**:`ScopeMCP` → `ScopeAgentInvoke = "agent:invoke"`;middleware、metadata、DCR 校验、consent 页全同步
   - 实际落地:`internal/oauth/const.go`、`internal/oauth/middleware/middleware.go`、`internal/oauth/handler/metadata.go` 全同步;dev 环境老 token 失效可接受(无生产用户)
4. ✅ **M0-T4 Resource metadata 改 RFC 9728 path-suffixed**:废弃 `MCPResourceURL` 单值,`/.well-known/oauth-protected-resource/api/v2/agents/:owner_uid/:agent_slug/mcp` 响应每个 agent 的独立 metadata
   - 实际落地:删 `handler.Config.MCPResourceURL` + `OAuthConfig.MCPResourceURL` + `OAUTH_MCP_RESOURCE_URL` env + yaml `mcp_resource_url`;`ProtectedResourceMetadata` 从 `c.Param("path")` 拿 suffix 动态拼 `resource` 绝对 URL;`oauth/middleware.AccessToken` 的 WWW-Authenticate challenge 改成用 `base + c.Request.URL.Path` 动态拼,client 拿到即能访问对应资源专属 metadata
5. ✅ **M0-T5 `agents` 表 `is_system` 支持**(打底):加字段或约定 `owner_uid=0` 为系统保留;`ListPublishedAgents` 查询 UNION 系统 agent,免审核、全局可见;`PublishService` 对系统 agent 直通 approved 状态
   - 实际落地:`Agent.IsSystem bool` 字段 + `idx_agents_is_system` 索引 + `SystemAgentOwnerUID = 0` 常量 + `(*Agent).IsSystemAgent()` 辅助方法;AutoMigrate 自动加列。**列表 UNION + 发布直通 approved 逻辑不在此步做**,推迟到 T7 真的有系统 agent 出现时统一改造,避免空转
6. 🟡 **M0-T6 MCP proxy token 透传**(核对完成 / 设计偏差已识别,落地并入 T7):核对 `internal/agent/hub` + HTTP fallback 在转发请求时,`Authorization` header 是否原样传给下游 agent;缺失则补齐(ADR §5)
   - 核对结果:**不是 token 透传**。`internal/agent/service/mcp_service.go` HTTP fallback 主动用 `ag.AuthTokenEncrypted`(agent 注册时预设 static token)替换收到的 OAuth token,注释明确"Synapse 自己的 OAuth token 不给 agent 看";WS hub `Invoke(ctx, agentID, rpcBody)` 签名无 token 参数,等价不透传
   - 偏差性质:**有意设计**。原作者方案 = "gateway 用 static token 证身份 + `X-Synapse-Org-ID` plaintext header 告知上下文",不是 ADR §5 的透传方案
   - 落地推迟原因:当前无 agent 回调 Synapse 实际链路(T7 未起),仓促改会破坏既有 static-auth 机制且无 E2E 测试保护;正确形态(严格透传 vs delegation token vs 双 token 并存)应和 T7 一起定
   - 合并指向:T7 子需求的"反向身份链"与 T6 一并重新设计
7. **M0-T7 系统 agent 本体开发**:`kb_retrieval_agent` / `kb_ingest_agent` / `workflow_agent` 作为独立服务实现,通过 MCP 协议暴露能力,回调 Synapse REST 写/读 KB;部署形态、仓库组织、版本发布走独立规划(拆子文档,本 roadmap 仅跟踪存在性)
   - **新增作用域**(吸收自 T5/T6):同步完成 T5 剩余的"列表 UNION 系统 agent + 发布直通 approved"查询改造;同步定稿 T6 的双向身份链设计("agent ← Synapse gateway 认证"和"agent → Synapse 代用户回调"两条方向的凭证机制)

### 挂账
- **M0-T7 子任务拆分**:系统 agent 本体是工程量最大项,每个 agent 单独出需求 + 技术方案;本节仅作为"要做"的锚点,具体里程碑见后续子文档
- **与 M6 的边界**:M6 聚焦 agent **注册机制**(SSRF 防护、版本化、健康检查等),对系统 agent 同样适用;M0 聚焦**架构迁移动作**,二者互补不重叠

---

## M1. 身份与登录

### 现状
- `internal/user`:Email 唯一 + 密码 bcrypt-like hash + JWT 签发 + Redis session(per-device)
- Session 支持 "per-device 限制"(默认 5 条)和"踢设备"
- refresh token 轮转 + reuse chain 检测
- **`users` 表无 `email_verified` 字段**,注册即 active
- 无密码重置 API、无邮件发送通路
- 登录接口无频率限制,爆破开放

### 核心问题
- **M1-P1** 任何人可用假邮箱注册 → 垃圾账号、发布 agent 冒充身份
- **M1-P2** 忘密码无自救,唯一路径是 DBA 改库
- **M1-P3** 登录接口可 bruteforce,弱密码用户直接失守
- **M1-P4** 无密码强度校验,允许 `123456`
- **M1-P5** 无第三方登录(Feishu/Google)到 user 的打通 —— 现在 Feishu 只进 `user_integrations`
- **M1-P6** 注册接口无限流,可被刷

### 调整方案

> **进度:M1 完整关闭(2026-04-20)**。M1.1–M1.7 全部落地,原挂账的两条尾巴("调外部 LLM 的 verified guard"和"GDPR 物理 purge job")已补齐。
> 产品决策调整:M1.6 只做 Google(飞书暂不接);M1.2 本地 fake sender 不做(Resend/SMTP 已覆盖 dev+生产)。
>
> 超出 roadmap 原计划的额外收尾(来自 M1 完成后一次模块体检):
>   - **登录审计 + 新设备邮件告警**:`login_events` 表 + `(user_id, device_id)` 首次命中发安全邮件
>   - **已登录改密 / 改邮箱**:`POST /users/me/password` + `POST /users/me/email` + 旧邮箱"邮箱已变更"告警
>   - **踢设备全局生效**:所有业务路由统一走 `JWTAuthWithSession` / `BearerAuth+session check`,消除"KickSession 只对 `/users/*` 生效"的 P0 漏洞
>   - **敏感 link 日志脱敏**:reset/verify link 只打 token 前缀,避免日志读者拿 token 重置他人账号

1. ✅ **M1.1 邮箱验证**:加 `users.email_verified_at`;注册流程 → 发 signed token 邮件 → 点击激活;未验证账号禁止创建 org / 发布 agent / 调用外部 LLM
   - 实际落地:本地注册强制消费 6 位 `email_code` 即等价邮箱所有权证明,注册成功直接 `status=active` + 写 `email_verified_at=now()`,跳过独立激活链接(见 `service.go:Register`)
   - OAuth 新建账号兜底:IdP 返 `email_verified=false` 时落 `pending_verify` + 发激活链接(见 `oauth_login.go` / `email_verification.go`)
   - 跨模块 guard:`organization.CreateOrg` / `agent.PublishService.Submit` / `agent.chatService.prepareChat` / `workflow.Handler.Run` 前置 `IsUserVerified` 检查
2. ✅ **M1.2 邮件发送抽象**:`pkg/mailer` 接口 + SMTP / 阿里云邮件推送 / Postmark 三实现;本地用 fake(写文件)
   - 实际:`internal/common/email` 提供 Sender 接口 + Resend(HTTP API) + SMTP 两实现,provider="" 自动 no-op,dev 通过日志拿激活/重置 link(见 `internal/common/email/sender.go`)
   - 产品决策:阿里云邮件推送 + 本地 fake(写文件) 不做
3. ✅ **M1.3 密码重置**:`/password-reset/request` 发邮件 →`/password-reset/confirm` 凭一次性 token(Redis,15min TTL)改密
   - 见 `password_reset.go`;confirm 成功同时 `LogoutAll` 踢全设备
4. ✅ **M1.4 登录 / 注册限流**:per-email 连续失败 10 次锁 15min;per-IP 注册 QPS 上限(滑动窗口,Redis)
   - 见 `login_guard.go`;阈值走 `config.UserConfig`
5. ✅ **M1.5 密码策略**:最短 10 位,禁用 top-10k 常用密码(离线 bloom filter 打包)
   - 见 `internal/common/pwdpolicy`;bloom filter 打进二进制,启动失败直接 fatal
6. ✅ **M1.6 第三方登录**:支持 Feishu / Google OAuth 登录落到 `users`(用 provider+subject 做外部身份,可同 email 合并)
   - 实际落地:Google OIDC(`internal/common/oidcclient` + `oauth_login.go` + `oauth_login_handler.go`),走 identity 表,email_verified 支持自动合并
   - 产品决策:飞书 OAuth 登录**不做**(原 M1-P5 的飞书场景继续走 `user_integrations` 集成,不作为登录源)
7. ✅ **M1.7 账号生命周期**:`users.status` 枚举扩展:`pending_verify / active / banned / deleted`;deleted 保留 pseudo 数据(GDPR 可彻底抹除)
   - 实际落地:4 态状态机(`model/models.go`)+ `DELETE /api/v1/users/me` 自助注销 pseudo 化 + 踢全 session + 删 identity(`account_lifecycle.go`)
   - **GDPR 物理 purge 暂不实现**:硬删 users 行涉及跨模块 FK 级联清理(orgs/agents/documents/audit 等),策略复杂度超当前阶段,等系统成熟统一规划;pseudo 化后的壳数据占用极小,短期合规场景用"deleted=不可访问"等价替代
   - 过期 `pending_verify` 清理:独立 `cmd/synapse-cleanup` CLI + `UserConfig.PendingVerifyExpireDays`(默认 7 天),扫 `status=pending_verify AND created_at < now()-阈值` 批量 **pseudo 化**(不是硬删),释放原 email 防被攻击者长期占位;生产按 cron 每日跑

### 挂账(不属 M1 原范畴,记在此以便后续规划)
- **GDPR 物理 purge 完整方案**:跨模块级联清理(agents/orgs/documents/publish/audit 等引用 user 的表)+ 统一的"物理抹除"编排;等跨模块 FK / 所有权转移 / 审计归档三件事有共识后再启动
- **管理员侧生命周期**:全局 admin 体系(封禁/解封/强制踢 session)未做,依赖先设计"全局管理员"角色(`users.is_admin` 字段 or ops token);M3 Org owner 范围不覆盖跨 org 管理
- **2FA / TOTP**:独立大工程,含备份码 + 登录链路改造,未启动

---

## M2. 鉴权

### 现状
- Web 客户端:JWT(HS256,ttl 短)+ refresh token(Redis)+ device_id 绑定
- Agent CLI:OAuth 2.1 AS(`internal/oauth`),PKCE 强制,public client only,RFC 9728 resource metadata
- Token 以 SHA256 hash 存库,不落明文
- 中间件:`JWTAuth` / `JWTAuthWithSession` / `BearerAuth`(OAuth 优先,fallback web JWT)
- OAuth access token 的 `typ=oauth-access+jwt`,防止跨流通用
- OAuth scope 粗粒度(按 client 白名单),无 per-resource 细粒度

### 核心问题
- **M2-P1** `/oauth/authorize` 表单无 CSRF token,可跨站伪造 consent
- **M2-P2** `/oauth/register`(DCR)无限流,可刷大量 ghost client
- **M2-P3** JWT 签名 secret 单 key 无版本,轮转 = 全量踢人
- **M2-P4** IP 限流不认 `X-Forwarded-For`,反代后失效
- **M2-P5** scope 模型简陋,无法做"此 token 只能调 agent X"级别的细粒度授权
- **M2-P6** OAuth state 用 JWT secret 签,JWT 泄露 = state 伪造成为可能(域未隔离)

### 调整方案
1. ✅ **M2.1 CSRF**:`/authorize` 渲染时下发 double-submit cookie,表单 POST 校验
   - 实际落地:`internal/oauth/handler/csrf.go` HMAC + 随机 nonce + exp 签名,GET 渲染时 Issue 并注入 hidden field,POST Verify 通过即 Clear(一次性 token 防重放);密钥从 `CookieSecret` HKDF 派生 "oauth-csrf-v1" 子密钥,避免与 flow cookie scope 混淆;SameSite=Strict + HttpOnly,校验失败渲染错误页不 302(防 redirect_uri 为攻击者)
2. ✅ **M2.2 DCR 限流**:per-IP + per-UA 复合限流;可选 captcha 开关
   - 实际落地:`internal/common/middleware/dcr_rate_limit.go` in-memory 固定窗口(1 分钟),per-IP 默认 5 / per-UA 默认 20(UA 用 SHA-256 hash key),任一维度触顶 429;阈值走 `OAuthConfig.DCR`;多实例部署合计阈值 = 单实例 × 副本数,写操作量小精确度足够;captcha 挂账,触顶先拒绝
3. ❌ ~~**M2.3 JWT key 版本化**~~ **(不做)**:`kid` header + key ring + 热轮转。产品决策:当前单 key 全量踢人的"轮转成本"在可接受范围内(Synapse 没有 7x24 无感知轮转需求),复杂度不值当。泄露场景按"停服 + 重新签发所有 token"处理即可
4. ✅ **M2.4 可信代理列表**:config 配 `trusted_proxies`,只在白名单内解析 XFF;否则用 socket IP
   - 实际落地:`ServerConfig.TrustedProxies []string`,main.go `r.SetTrustedProxies(cfg.Server.TrustedProxies)`,空 slice = 不信任任何代理(走 socket IP,安全默认);dev yaml 留空,生产填 ingress/LB CIDR
5. ~~**M2.5 Scope 细化**~~ **(按 ADR 废弃)**:原规划的 `retrieval:read/write` / `agent:invoke:<slug>` / `workflow:run` 细化 scope 不做。ADR §4 定稿"OAuth 只证身份,细粒度授权全部走 org RBAC",OAuth 层只保留单一粗 scope `agent:invoke`;per-agent ACL 合并到 M4 一起做
6. ✅ **M2.6 状态签名隔离**:OAuth state 用专用 HMAC key(config `oauth.state_key`),和 JWT 解耦
   - 实际落地:新增 `IntegrationConfig.StateSignerSecret` + env `INTEGRATION_STATE_SIGNER_SECRET` + dev yaml 默认值;`main.go` 的 integration `NewStateSigner` 从 `cfg.JWT.SecretKey` 切到 `cfg.Integration.StateSignerSecret`;`applyDefaults` 对空串给 dev 固定值,生产走 env 或 yaml 硬值。OAuth login 的 `OAuthLogin.StateCookieSecret` 和 OAuth AS 的 `CookieSecret` 早已独立,无需再改
7. ✅ **M2.7 中间件全打 log(metric 暂缓,并入 M10.1)**:401/403 的 reason 统一枚举,便于告警
   - 实际落地:`internal/common/middleware/auth.go` 加 `AuthRejectReason` 枚举(missing_header / bad_format / invalid_token / token_expired / session_revoked / session_expired)+ package-level logger(`SetLogger` 启动一次注入)+ `logAuthReject(c, reason)` helper;JWTAuth / JWTAuthWithSession / BearerAuth 所有 abort 分支调 helper。`internal/oauth/middleware/middleware.go` 的 `AccessToken` 利用已有 `log` 参数,加 3 个 `oauth_` 前缀 reason(missing_token / invalid_token / insufficient_scope),每次拒绝带 reason/status/method/path/ip 打 warn。响应体未改,对客户端零 breaking。**metric 暂不做**,等 M10.1 统一上 Prometheus 时从 log 字段提取 counter 即可(`auth_reject_total{reason="..."}`)

### 超出 roadmap 原计划的额外收尾(M2 完成后一次模块体检)
- **Login CSRF(`/oauth/login` POST)**:原 M2.1 只给 `/oauth/authorize` 加了 CSRF double-submit,遗漏了 `/oauth/login` 表单。攻击者可诱骗已打开 Synapse 的受害者 POST `/oauth/login` 附攻击者凭证 → 受害者浏览器 flow cookie 指向攻击者账号 → 后续 consent 把受害者的操作授权到攻击者 client。补丁:复用 `csrfCookie`,`renderLogin` 每次签发新 token,`LoginSubmit` 入口先 Verify + Clear(一次性,防重放)
- **登录爆破绕过(`/oauth/login`)**:原 `oauthLoginAdapter.VerifyCredentials` 直接走 `repo + bcrypt`,**完全跳过 `LoginGuard`**,攻击者在 `/api/v1/users/login` 被 per-email / per-IP 锁后可换到 `/oauth/login` 继续刷密码,M1.4 防线在此路径等于不存在。补丁:`UserService` 加 `VerifyPasswordOnly(ctx, email, password, ip, ua)` 方法(复用 `Login` 的反爆破链路,去掉 code / session / JWT 几段),adapter 切到调用此方法,Redis 计数器和 web login 共享同一 key space;`LoginAdapter` 接口签名加 `ip, userAgent`,定义 3 个具名错误 sentinel,handler 按 locked / ip_rate_limited / invalid credentials 分支展示文案

> **M2 完整关闭(2026-04-21)**:7 个 P1–P7 调整方案中 5 项落地(M2.1 / M2.2 / M2.4 / M2.6 / M2.7)、2 项正式否决(M2.3 JWT kid 轮转产品决策不做、M2.5 Scope 细化按 ADR 废弃);额外发现并修复 2 条 out-of-band 漏洞(Login CSRF + 登录爆破绕过)。鉴权模块已无已知重大功能缺陷或隐藏漏洞。

---

## M3. Org 管理

### 现状
- Org 生命周期:create / update / dissolve(soft delete);member cap 可配
- 邀请:`type` 区分 `member` / `ownership_transfer`,状态机 pending/accepted/rejected/expired/revoked;**accept URL 无 token**(凭 invitation_id + JWT 身份校验)
- Member 退出 / 被踢 → 级联撤销其已发布 agent(hook 机制)
- `org_member_role_history` 表已在,角色变更有审计
- Slug 规则严格(`^[a-z][a-z0-9-]{2,31}$`)
- `InvitationService.ExpireJob` 已实现(`UPDATE ... WHERE status='pending' AND expires_at < now()`),但**未接入任何 cron / 启动流程**
- 用户自助注销(`DELETE /api/v1/users/me`,M1.7)**未校验是否为 org owner**,可能产生 owner 孤儿态

### 核心问题
- **M3-P1** Slug 唯一性靠 DB unique index 兜底,无前端友好校验
- **M3-P2** 邀请邮件**没发出去**(`internal/common/email` 已就绪,但 InvitationService 未接入,无邀请模板)—— 前端自己拼 accept URL
- **M3-P3** `ExpireJob` 已写但无调度入口,邀请到期后仍为 `pending`,Accept 时才按 `expires_at` 拒绝
- **M3-P4** 所有权转让(ownership_transfer)代码分支在,但全链路冷路径,未覆盖测试(整个 org 模块 0 个 `_test.go`)
- **M3-P7** owner 自注销未 guard:若 owner 走 M1.7 自助注销流程,users 行被 pseudo 化而 `orgs.owner_user_id` 仍指向原 ID,org 变孤儿
- ⏸ **M3-P5** Org 解散(`dissolved`)后资源清理是懒回收(涉删,挂起等跨模块级联删除统一规划)
- ⏸ **M3-P6** 无 org 级导出 / 迁移 / 归档能力(暂不考虑)

### 调整方案
1. **M3.1 邀请通路**:接 `internal/common/email`,新增 `BuildInvitationEmail()` 中英双模板,`CreateInvitation` / `InitiateOwnershipTransfer` 成功后异步发邀请通知邮件到 invitee(含 org 名、邀请人、邀请类型、`{frontend_base}/invitations/mine` 链接、过期时间);邮件发送失败不回滚邀请(走 WARN 日志)
2. **M3.2 邀请过期清理接入 cron**:`InvitationService.ExpireJob` 已有,把调用并入 `cmd/synapse-cleanup` main(和 M1.7 的 `ExpireStalePendingVerifyAccounts` 并列),生产按 cron 每日跑;SQL 已带 `status='pending' AND expires_at < now()` 条件 + `idx_org_invitations_expires_at`,无需再改表
3. **M3.3 Ownership Transfer E2E 测试**:用 SQLite 内存库 + 真实 service/repo 堆一条 happy path:owner 发起 → invitee accept → 校验 `orgs.owner_user_id` 迁移、old owner 降 admin、new owner 升 owner、`org_member_role_history` 两条记录;顺手覆盖几个失败分支(非成员 accept、邀请已过期、非 owner 发起)
4. **M3.6 Slug 合法性前端校验 API**:`GET /api/v2/orgs/check-slug?slug=xxx` 返回 `{available, reason}`,reason 枚举 `invalid_format|taken`;仅 JWT 保护(避免匿名穷举)
5. **M3.7 Owner 自注销前置 guard**:`UserService.DeleteAccount` 走 pseudo 化之前,查询该 user 是否为任一 `status='active'` org 的 owner;命中则返回 409 `owner_of_active_orgs`,响应体带 `[{slug, display_name}]` 列表,引导前端要么转让(走 M3.3 的 ownership_transfer 邀请)、要么解散;跨模块依赖通过接口注入(user.Service 接一个 `OwnerChecker`,main.go 用 org repo 装配)
6. ⏸ **M3.4 Org 解散清理 job**(挂起):等跨模块级联删除方案统一规划;现状"member 踢/退出 → 级联撤销已发布 agent"的 hook 也在此范围内,不动
7. ⏸ **M3.5 Org 导出**(挂起):暂不考虑

### 超出 roadmap 原计划的额外收尾(M3 完成后一次模块体检)
- **邀请撤销越权**:`invitation_response_service.Revoke` 只靠 handler 的 `PermMemberInvite` 校验,拿到 PermMemberInvite 的自定义角色能撤销他人邀请。补丁:service 层补充"operator==inviter 或持 PermMemberRemove"二选一,否则返 `ErrOrgPermissionDenied`;InvitationService 吞下 roleSvc 依赖
- **邀请创建速率限制**:`CreateInvitation` / `InitiateOwnershipTransfer` 无限流,PermMemberInvite 用户可用 SearchInvitees → CreateInvitation 把邀请邮件刷到任意注册用户邮箱(钓鱼放大)。补丁:Redis 固定窗口(`synapse:rl:invite:inviter:*` 和 `synapse:rl:invite:org:*`)per-inviter 30/min + per-org 100/min,阈值走 config,Redis 故障 fail-open
- 其余中优(3 项:ownership transfer 需校验 target email_verified / Accept 事务内复核 inviter 仍是 owner / DissolveOrg 清 pending 邀请)+ 低优(UpdateOrg 内容字符过滤)挂账到下一轮体检一起改

### 第二轮体检收尾(Mid-1~4 + 低优)
- **Mid-1 邀请限流加 per-invitee 维度**:`redisInvitationRateLimiter.Check(inviter, org, invitee)` 三维复合,invitee 窗口 1h 默认 10 次,防单受害者邮件轰炸
- **Mid-2 所有权转让 target 邮箱验证 guard**:`InitiateOwnershipTransfer` 接 `UserVerifier`,未验证账号拒转让(`ErrTransferTargetNotVerified` / 400110054),防 org 被转进僵尸账号
- **Mid-3 Accept 事务内复核 inviter**:`acceptOwnershipTransfer` 头部 `tx.FindOrgByID` reload,owner 若已变则 `ErrInvitationStale` / 400110038,不继续走降级污染 role_history
- **Mid-4 DissolveOrg 清 pending 邀请**:`DissolveOrg` 用 `WithTx` 包住 `UpdateOrgFields` + `RevokePendingInvitationsByOrg`,解散后 invitee 的 ListMine 不再见僵尸邀请

### 挂账
- **级联删除统一方案**:M3.4 + M3.5 + 现状里 member 踢/退出 的 agent 级联 hook,全部等这个方案出来再做
- **全局 admin 对 owner 孤儿的托底**:M3.7 只解决用户自注销路径,若 owner 被封禁 / 被全局 admin 强制注销,依赖尚未存在的"全局管理员"体系(见 M1 挂账)
- **UpdateOrg display_name / description 内容过滤**:现仅限长度,允许换行 / 控制字符 / 零宽字符,前端漏 escape 就可能炸 UI。建议补 `strings.TrimSpace` + 禁 `\x00-\x1f`(保留 `\t\n`)
- **CheckSlug 无枚举限流**:登录用户可批量探测已占用 slug;slug 本身公开,非高危,可后续加 per-user 轻量限流
- **Reserved slug names**:用户可创建 `slug=mine` / `check-slug` 的 org,之后按 slug 访问不到(静态路由优先);CreateOrg 应维护 reserved list 拦截

> **M3 完整关闭(2026-04-21,二轮体检后)**:原规划 5 项(M3.1 / M3.2 / M3.3 / M3.6 / M3.7)全部落地,M3.4 / M3.5 按跨模块级联删除统一规划挂起;两轮体检额外修复 7 条漏洞 / 功能缺陷:
>   - 一轮:邀请撤销权限收紧(Revoke 越权)+ 邀请创建速率限制(per-inviter/per-org)
>   - 二轮:Revoke 非成员 500 误分类 + per-invitee 限流 + 转让 target 邮箱验证 guard + Accept 事务内复核 inviter + Dissolve 清 pending 邀请
>   - 累计 14 个 service 层单测覆盖(ownership_transfer_test.go)
>
> Org 模块已知重大 bug / 漏洞清零,剩余挂账均为 UX / 低风险边角 case。

---

## M4. 权限管理

### 现状
- 20 个权限点(`PermOrgUpdate`、`PermAgentInvoke`、`PermDocumentRead` 等),存 `org_roles.permissions` JSON 列
- 预设 3 个角色(owner / admin / member)+ 自定义角色(owner 专属 `PermRoleManage`)
- Owner 独占权限(`PermOrgDelete` / `PermOrgTransfer` / `PermRoleManage`)不得分配给其他角色
- 权限检查走 `roleSvc.HasPermission(userID, orgID, perm)` 统一入口
- 多租户隔离:中间件注入 `org_id`,service 层 query 必带 `WHERE org_id=?`
- **无资源级 ACL**(文档、agent 全 org 内可见即可读)

### 核心问题
- **M4-P1** 权限点 JSON 列无索引,未来加"哪些角色有 X 权限"的反查会扫全表
- **M4-P2** Service 层 query 忘加 `org_id` 没有编译期强制,泄露风险靠人肉 review
- **M4-P3** 无文档级 ACL(HR / 财务 / IP 场景刚需,schema 迁移越晚越贵)
- **M4-P4** Agent 代表用户调用时,用的是 **agent 的 org 权限**还是**调用方用户的权限**,当前默认前者,应切后者(否则 agent 就是权限放大器)
- **M4-P5** 审计只覆盖角色变更,不覆盖"谁访问了哪个 doc / 调用了哪个 agent"
- **M4-P6** 无"临时提权 / 代客操作"机制(客服场景)

### 调整方案
1. **M4.1 Repo 层 scope guard**:所有 repo 方法签名必带 `orgID uint64`,内部 query 强制 `WHERE org_id=?`;加 lint rule 检查(`golangci-lint` custom analyzer)
2. **M4.2 Doc 级 ACL**(对齐 kb-roadmap T4.1):`documents.acl_group_ids` 多对多 + 检索 SQL JOIN 过滤;retrieval.Query 带 `user_id`
3. **M4.3 Agent 调用权限链**:MVP 走 **OAuth token 透传**(ADR §5)—— MCP proxy 把 Claude Desktop 送来的 token 原样转给下游 agent,agent 回调 Synapse REST API(如 `POST /api/v2/orgs/:slug/documents`)时带回同一 token,`BearerAuth` 解出 user_id / org_id → `HasPermission(user, **URL 里的目标 org**, PermDocumentWrite 等)`。系统 agent 和第三方 agent 一视同仁。Token exchange(RFC 8693)升级路径挂账,等 bearer 保存风险变成实际问题再上
4. **M4.4 资源访问审计**(对齐 M10.4):`audit_log` 表覆盖 doc read / agent invoke / integration config 变更
5. **M4.5 权限点反查索引**:若要做"此角色能看哪些 agent"聚合,给 permissions JSON 加 GIN 索引(MySQL 8.0 multi-valued index)或改表结构为 `role_permissions(role_id, perm)`
6. **M4.6 临时代客**:owner 发起 `impersonate(user_id, reason, ttl)`,期间操作走代客的权限 + 审计双方身份

---

## M5. 数据摄取

### 现状
- 文档:multipart 上传(1MB 全局上限,需放开)→ OSS 存原文件 → MySQL 落元数据 → async chunk(递归分隔符,1500 runes / 150 overlap)→ Azure embedding → PG/pgvector
- 代码:GitLab PAT 全量 pull → tree-sitter AST 切函数/类 → embedding → PG
- 图片 / DB schema / bug ticket:未接入(kb-roadmap T2.2–T2.4)
- 去重:content_hash(文档)+ blob_sha(代码)
- 索引状态机:pending / indexed / failed;failed 无自动重试队列
- overwrite 是原地替换,旧版本丢失

### 核心问题
- **M5-P1** HTTP body 1MB 全局上限 → 上传 > 1MB 文档直接 413
- **M5-P2** Chunking 是"递归分隔符",语义边界常被切断(kb-roadmap T1.3)
- **M5-P3** Embedding 失败只 mark `failed`,不自动重试,堆积后需手工重跑
- **M5-P4** 无批量摄取管线,上 10w 文档会把 embedding quota 打爆
- **M5-P5** 代码仓库只支持全量 pull,`last_synced_commit` 字段在但未用;改 1 文件触发全仓重跑
- **M5-P6** 无版本化,覆盖上传即丢历史(合规场景必需)
- **M5-P7** 无 PII / 敏感信息扫描

### 调整方案
1. **M5.1 分路由体限**:`/documents/upload`(新架构下主要服务于 agent 回调摄取,参见 ADR §5)单独放开到 50MB(config),其他路由沿用 1MB
2. **M5.2 失败重试队列**:failed chunk 入 retry 队列(Redis stream / DB 轮询),指数退避,最多 N 次进死信
3. **M5.3 结构感知切分**(对齐 kb-roadmap T1.3):Markdown 按 heading 切、代码按 AST 切、表格整块
4. **M5.4 批量摄取 pipeline**:并发 chunker + embedding + rate limit + backpressure;支持 cursor 断点续传
5. **M5.5 代码增量同步**:GitLab webhook + `last_synced_commit` diff,只重跑改动文件;前置需要 M8 的异步任务改造
6. **M5.6 文档版本化**:`documents.version` 自增 + 旧行软删除保留;retrieval 默认查最新,支持 `?at_time=` 历史查询
7. **M5.7 PII 扫描**(对齐 kb-roadmap T4.3):入库前 regex(卡号 / SSN / API key / email / 电话)+ 命中打 `tags.sensitive=true`,可选自动脱敏
8. **M5.8 图片摄取**(对齐 kb-roadmap T2.3):VLM caption → text chunk 主路径;CLIP 跨模态可选

---

## M6. Agent 注册

### 现状
- Agent 类型三种:`chat`(Synapse-native) / `tool` / `mcp`
- 注册流程:owner 填 endpoint_url + auth_token → 存 `agents`(token 加密列)
- 发布流程:owner 向 org 发布 → org admin 审核(`require_agent_review` 可开关)→ approved / rejected / revoked 状态机
- Endpoint guard:`internal/agent/handler/endpoint_guard.go` 挡 loopback / link-local / cloud metadata
- WS hub 注册 agent 实时在线状态(见 M7)
- **无 version 字段,没有 "latest/pin" 概念**
- **无系统 agent 概念**:ADR 定稿"系统 agent 走 `owner_uid=0` 保留值 + 白名单自动注入所有 org + 免审核"但尚未实现(迁移动作见 M0-T5)

### 核心问题
- **M6-P1** Endpoint guard 对 DNS rebinding 无防(resolve OK,connect 时换 IP)
- **M6-P2** HTTP 301/302 跟随未对每一跳重新校验目标
- **M6-P3** `allowPrivateEndpoints` 配置语义不清晰
- **M6-P4** 无 version,升级 agent 悄悄破坏已依赖它的 org
- **M6-P5** auth_token 加密用全局 master key(非 KMS),轮转未实现(见 M9)
- **M6-P6** 无 agent 健康检查(endpoint 挂了,用户才发现)
- **M6-P7** 无 agent 元数据的 signed manifest 概念(owner 自称支持 X tool,无校验)

### 调整方案
1. **M6.1 Pinned dialer**:resolve 一次拿 IP → 校验私网/特殊段 → 用 IP 建 conn(跳过二次 DNS)
2. **M6.2 重定向每跳校验**:custom `CheckRedirect` 对 301/302 的 Location 重跑 guard
3. **M6.3 Endpoint policy 收敛**:config `agent.endpoint_policy: {public|private|any}`,默认 `public`,文档化
4. **M6.4 Agent 版本化**:`agents.version`(semver),`agent_publishes` 绑定到版本;路由默认最新 major,客户端可 pin;配合 `Deprecation` header。**系统 agent 同样受此约束**,以便滚动升级不破坏已发布的依赖
5. **M6.5 Health check**:每 N 分钟对 HTTP endpoint 发 `GET /healthz`(MCP 用 `initialize`),失败连续 3 次标 `degraded`,dashboard 可见
6. **M6.6 Manifest 校验**:agent 注册时 fetch `/manifest.json`,记录其声明的 tool 列表,运行时对照实际 tools/list 漂移告警
7. **M6.7 Agent 限流 per-invoker**:`invoke_count_1m(org_id, agent_id)`,防止单租户打爆某 agent

---

## M7. 连接管理

### 现状
- **WS Hub**(`internal/agent/hub`):agent 主动拨上 `/api/v2/agents/hub`,OAuth 校验 → Attach → readLoop / pingLoop / 握手拉 tools/list;旧连接 evict
- **MCP Proxy**:`/api/v2/agents/:owner_uid/:agent_slug/mcp` 优先走 WS,offline fallback HTTP endpoint
- **Chat SSE**:`POST /agents/:slug/chat` 流式响应
- **Workflow SSE**:`/workflow` 走 Azure LLM + MCP tool loop,返回 SSE
- **无心跳超时告警,无消息 ID 单调,无客户端重连协议**

### 核心问题
- **M7-P1** SSE 无写超时,慢客户端让 server goroutine 挂起(资源泄漏)
- **M7-P2** SSE 无 `event_id` 单调 + `Last-Event-ID` 续传,断线即丢
- **M7-P3** Hub 单机内存存连接(`sync.Map`),多副本部署时"agent 在哪台机器"需要额外路由
- **M7-P4** MCP proxy 的 pending request 没有超时保护,agent 卡住会累积 goroutine
- **M7-P5** WebSocket ping / pong 参数(间隔、超时)未暴露到 config
- **M7-P6** 无 backpressure,workflow 下游 tool 慢 → LLM 流也卡住

### 调整方案
1. **M7.1 SSE 写超时 + 心跳**:flush 5s 超时即断连;每 15s 发 `:keep-alive` 注释行
2. **M7.2 SSE 续传协议**:事件带 `id: <monotonic>`,客户端 `Last-Event-ID` header 续传最近 N 条(内存 ring buffer per session)
3. **M7.3 多副本 Hub**:引入 Redis pub/sub 或 NATS 做"本机无该 agent 时转发到正确副本";或 sticky routing(负载均衡层按 agent_id hash)。**新架构下 MCP proxy 是流量主战场**(ADR §2):所有 MCP client 请求都经 proxy 转到 agent,Hub 路由正确性直接决定用户体验,优先级比 SSE / workflow 相关条目高
4. **M7.4 MCP call 超时**:pending request 带 context timeout(默认 30s,可 config);超时强制返回 error + cleanup
5. **M7.5 Hub 参数化**:`agent.hub.ping_interval / pong_timeout / max_message_size` 走 config
6. **M7.6 Workflow 背压**:SSE 客户端断开 → 立刻 cancel ctx → 中止 LLM / tool call → 停止计费

---

## M8. 异步任务

### 现状
- `internal/asyncjob`:`AsyncJob` 表(pending / running / finished / failed)+ Runner 接口
- Runner 注册式:Feishu sync / GitLab sync
- 启动时 reap 上一次崩溃遗留的 `running` 任务 → mark failed
- **无 retry,无 timeout,无并发上限,无死信**

### 核心问题
- **M8-P1** Runner 崩一次任务就挂掉,只能手工重置状态
- **M8-P2** 无全局并发信号量,同时 100 个 GitLab sync 可能把 PAT 打封号
- **M8-P3** 任务结果(result / error)全内存 + 一行 varchar,长 stacktrace 截断
- **M8-P4** 无优先级队列,紧急同步排不到前面
- **M8-P5** Reap 只在启动做一次,跨实例场景如果某实例不重启,它的 running 孤儿永不清
- **M8-P6** 无任务链(sync → index → embed 三段不能串)

### 调整方案
1. **M8.1 Retry 策略**:runner 声明 `{timeout, max_retries, backoff}`;超时强杀 + 指数退避重试 + 最终进死信
2. **M8.2 并发上限**:全局信号量 + per-provider 并发上限(`gitlab_sync_concurrency: 3`)
3. **M8.3 结果存储**:`result / error` 改 TEXT,附 `stacktrace TEXT`,长结果存 OSS 拿 URL
4. **M8.4 优先级**:`async_jobs.priority` int,调度按 priority 排序;手动触发的同步 priority 高于定时
5. **M8.5 定时 reap**:每 30s 扫 `status=running AND started_at < now - timeout` 兜底(所有副本都跑)
6. **M8.6 DAG 支持**:`parent_job_id` + `status=waiting_parent`;父成功才跑子,失败级联 skip
7. **M8.7 死信 CLI**:`cmd/synapse jobs list-dead / retry <id> / discard <id>` 运维接入

---

## M9. 密钥与加密

### 现状
- `master_keys` 表存全局 master key(Snowflake ID + hex 编码字节)
- Agent `auth_token_encrypted` 用 master key 加密(AES-GCM 或类似)
- **Feishu `app_secret`、user refresh_token 明文存库**
- **无 key 轮转机制**
- Master key 泄露 = 全量穿透

### 核心问题
- **M9-P1** 明文秘钥 5+ 处,任一 DB dump 泄露即被攻破
- **M9-P2** Master key 本身在同一库里,备份/副本/复制都带走了
- **M9-P3** 无轮转能力,怀疑泄露只能停服灌库
- **M9-P4** 无 per-row DEK,单 key 加密全量,攻击面大
- **M9-P5** 加密算法 / IV / 版本号无元数据字段,换算法时无法兼容旧数据

### 调整方案
1. **M9.1 KMS 对接**:接入阿里云 KMS / AWS KMS / Vault Transit,key 留云端,Synapse 只拿 encrypt/decrypt API
2. **M9.2 Envelope encryption**:每行数据随机 DEK → 用 KMS KEK 加密 DEK → 存 `{ciphertext, encrypted_dek, kek_version, algo}`
3. **M9.3 加密对象全量迁移**:`user_integrations.refresh_token / access_token`、`org_feishu_configs.app_secret`、`org_gitlab_configs.private_token`、`agents.auth_token_encrypted` 全部走 M9.2
4. **M9.4 轮转脚本**:`cmd/rotate-keys` 读旧 KEK 解 DEK → 新 KEK 重新封装 → 批量 update;支持 dry-run
5. **M9.5 本地 DX 保留**:fake KMS driver(文件 master key),dev/本地不依赖云
6. **M9.6 Secret scanning**:CI 加 trufflehog / gitleaks 扫代码 / .env,防人为提交秘钥

---

## M10. 可观测与运维

### 现状
- 结构化日志(text/JSON,lumberjack 轮转)+ `index_status` 状态字段
- **零 Prometheus / 零 tracing / 零 request_id / 零 dashboard / 零 alert**
- **零审计日志**(仅 role 变更历史表)
- `docker compose up` 手工部署,无 CI/CD,无 staging,无备份策略
- DB migration 走 GORM AutoMigrate(生产定时炸弹)
- `cmd/synapse/main.go` 多处 `panic()`,依赖任一不可用直接崩
- `/health` 端点存在,无 `/ready` 区分

### 核心问题
- **M10-P1** 线上问题只能 grep 日志,无法回答"P95 延迟"/"谁在刷"/"embedding 成本"
- **M10-P2** 错误无统一格式 / 无 request_id,调用链无法串起来
- **M10-P3** AutoMigrate 在字段重命名 / 类型变更 / 加索引场景会锁表或丢数据
- **M10-P4** 无审计日志,SOC2 / ISO27001 / 企业合规直接卡住
- **M10-P5** 无备份,一次 `DROP TABLE` 归零
- **M10-P6** 无 CI,改代码全靠手感(测试覆盖 ~30%)
- **M10-P7** 启动 panic = K8s crashloop,依赖抖动即故障
- **M10-P8** 无 per-org 配额 / 计费埋点,embedding / LLM 成本不透明

### 调整方案
1. **M10.1 Metrics**:`/metrics` 端点;HTTP 延迟直方图、DB 池、embedder / reranker 延迟+失败率、asyncjob 排队深度、SSE 连接数、`retrieval_hit_rate`、`embedding_tokens_total`
2. **M10.2 Tracing**:OpenTelemetry,trace_id 从 HTTP 入口透传到 DB / 外部调用;sampling 1% 可调
3. **M10.3 统一错误 + request_id**:`pkg/errors.AppError{Code, HTTPStatus, Message}`;middleware 生成 `X-Request-ID`,logger 自动注入 user_id / org_id / request_id;response JSON shape 固定
4. **M10.4 审计日志**:`audit_log(id, org_id, actor, action, resource_type, resource_id, metadata, ip, ua, ts)`;写操作 + 敏感读埋点;hot 3 个月 + 冷存 OSS
5. **M10.5 DB migration 工具化**:切 `golang-migrate`,写 up/down SQL;启动时 diff schema,不一致拒启;CI 跑 up+down 回环
6. **M10.6 启动优雅降级**:`panic` → `return err`;区分硬依赖(MySQL / JWT key)和软依赖(embedder / reranker / OSS),后者失败 WARN + 降级;`/health` 常 200,`/ready` 硬依赖全通才 200
7. **M10.7 备份 + 灾备**:MySQL / PG 每日全备 + binlog/WAL 增量 → OSS 保 30 天;OSS 跨 region 复制;每季度演练恢复
8. **M10.8 CI/CD**:PR → lint + unit + integration(testcontainers)+ build image;main → staging 自动部署;tag → 生产 + 手动 approve
9. **M10.9 覆盖率门槛**:核心包(oauth/org/retrieval/agent)≥ 60%,新增文件 ≥ 70%,PR 不过不合
10. **M10.10 Per-org 配额 + 计费埋点**:`org_quotas` + `org_usage_daily`;每次 embedder / LLM 调用埋点 tokens;超额降级 / 拒写
11. **M10.11 Runbook**:`docs/runbook.md` 覆盖 DB 挂 / Redis 挂 / OSS 不可用 / 全挂的应对

---

## 跨模块推进节奏

### 第 0 波(架构迁移,先行)
**先于所有业务推进**:对齐 ADR 的一次性动作,完成后其他波次才能顺利进行。
- **M0-T1–T6** 下线旧 retrieval 端点 / scope 重命名 / metadata 改造 / 系统 agent 机制打底 / token 透传核对
- **M0-T7** 系统 agent 本体(kb_retrieval / kb_ingest / workflow)独立规划,不阻塞第一波;但第一波结束前至少有一个系统 agent 跑通,验证整个链路闭环

### 第一波(2–3 周,阻塞上线)
安全硬伤 + 最小可运行性,缺一不可:
- **M9.1–M9.4** 秘钥 KMS 化 + 轮转
- **M1.1–M1.5** 邮箱验证 + 密码重置 + 登录限流
- **M2.1–M2.4** OAuth CSRF + DCR 限流 + JWT 轮转 + 可信代理
- **M6.1–M6.3** Endpoint guard(SSRF 抗 DNS rebinding)
- **M10.5–M10.6** DB migration 工具化 + 启动优雅降级

### 第二波(4–6 周,上线后稳住)
观测 + 稳定性,并行推 kb-roadmap T1.1/T1.2/T1.3(检索质量):
- **M10.1–M10.3** Metrics + Tracing + 统一错误 / request_id
- **M8.1–M8.5** AsyncJob retry / 超时 / 并发 / 优先级 / reap
- **M7.1–M7.4** SSE 背压 + 续传 + Hub 多副本 + MCP 超时
- **M5.2–M5.4** 摄取失败重试 + 体限分路由 + 批量管线
- **M10.8–M10.9** CI/CD + 覆盖率门槛

### 第三波(8–12 周,拿企业客户)
合规 + 多租户深化,配合 kb-roadmap T2.1(代码)+ T3.1(Agent CRUD):
- **M4.2–M4.4** Doc 级 ACL + Agent 权限链 + 资源审计
- **M10.4** 审计日志全量
- **M5.6–M5.7** 文档版本化 + PII 扫描
- **M6.4** Agent 版本化
- **M3.4–M3.5** Org 解散清理 + 导出

### 第四波(规模化阶段)
- **M10.7** 备份 / 灾备演练
- **M10.10** Per-org 配额 / 计费
- **M7.3** Hub 多副本 + 水平扩容
- 数据库读写分离 / pgvector 分片
- LLM provider per-org 配置化

### 四个最容易被低估的
1. **M0-T7 系统 agent 本体**——架构已定,但 agent 本身没写就等于新架构空转;务必尽早跑通一个端到端闭环
2. **M9 秘钥轮转**——等泄露了才做,客户信任直接归零
3. **M10.3 request_id + 统一错误**——早做半小时,后面定位线上问题省几十小时
4. **M4.2 Doc ACL schema**——schema 改动,越晚做迁移越贵(和 kb-roadmap 共识)
