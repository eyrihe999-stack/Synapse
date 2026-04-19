# 飞书(Lark)集成接入指南

Synapse 支持把每个用户授权的飞书云文档 / wiki 自动同步成本地知识库。

## 架构

```
用户浏览器
  │  (1) 前端引导:构造 authURL → intgsvc.FeishuService.BuildAuthURL(orgID, state)
  │       凭证按 orgID 从 org_feishu_configs 查
  │
  ▼
飞书 OAuth 授权页
  │  用户点"同意"
  │
  │  (2) 飞书 302 回跳到配置的 redirect_uri,带 code + state
  ▼
Synapse 后端 callback handler
  │  (3) intgsvc.FeishuService.ExchangeCode(userID, orgID, code)
  │      → 调飞书 API 换 refresh_token
  │      → 存 user_integrations 表
  │
  ▼
前端用户点"一键导入飞书文档"(按需触发)
  │  (4) POST /api/v2/orgs/:slug/integrations/feishu/sync
  │      → asyncjob 模块建 async_jobs 行 → goroutine 跑 feishusync Runner
  │  (5) Runner 对当前用户执行 SyncOneUser:
  │       RefreshViaIntegration → 取 / 刷新 access_token
  │       feishu.Adapter.Sync(since=LastSyncAt) → 拿变更列表
  │       feishu.Adapter.Fetch(ref) → 拉 docx blocks → md
  │       docsvc.Upload(source_type=feishu_doc, source_ref=...)
  │         ↓ 自动按 source_ref upsert,同内容走 hash dedup
  │  (6) 前端每 1.5s GET /api/v2/async-jobs/:id 轮询进度条
  ▼
documents + document_chunks 表(Synapse 知识库)
```

## 一次性配置

飞书应用凭证 **per org** 存储在 `org_feishu_configs` 表,由**每个组织的 admin** 在前端"集成"页自助配置。
部署级只需配置回调地址 + 飞书区域(见第 5 节)。

### 1. 飞书开发者后台注册应用(组织管理员)

前往 [开放平台](https://open.feishu.cn/app) → **创建企业自建应用**。

### 2. 开权限(Scopes)

**凭证与基础信息:**
- `authen:access_token:update` — OAuth 刷新 access_token
- `authen:user_info:get` — 取用户 open_id / name / email(写入 user_integrations.metadata)

**文档读取:**
- `drive:drive:readonly` — 列 drive 文件、文件夹
- `wiki:wiki:readonly` — 列 wiki 空间、节点
- `docx:document:readonly` — 读 docx 文档 blocks

**开发者后台路径**:`权限管理 → 开通权限 → 搜索上面 scope 勾选 → 提交审批`(部分 scope 需企业管理员审批通过)。

### 3. 重定向 URL 白名单

**路径**:`安全设置 → 重定向 URL`。添加部署实例的回调地址(Synapse 前端 admin 卡片上会展示并提供一键复制):
```
http://localhost:8080/api/v2/integrations/feishu/callback   # dev
https://synapse.example.com/api/v2/integrations/feishu/callback   # prod
```

### 4. 发布应用

**路径**:`应用发布 → 版本管理 → 创建版本 → 申请发布`。审批通过后 app 才能被企业内用户授权。

### 5. Synapse 部署级配置

`config/config.dev.yaml`(只配部署级元信息,**不**配应用凭证):
```yaml
feishu:
  base_url: ""         # 空 = 中国区;海外 Lark 填 https://open.larksuite.com
  redirect_uri: "http://localhost:8080/api/v2/integrations/feishu/callback"
  frontend_redirect_url: "http://localhost:3080/org/integrations/feishu"  # 回调成功/失败后 302 跳前端的飞书详情页
```

`app_id` / `app_secret` 由 org admin 登录 Synapse 后在"集成"页填入,写入 `org_feishu_configs` 表。

### 6. 跑 migration 建相关表

```bash
APP_ENV=dev go run ./cmd/migrate
```

涉及的表:
- `user_integrations` — 用户 OAuth 令牌
- `org_feishu_configs` — 组织飞书 App 凭证
- `async_jobs` — 一键导入的异步任务状态

### 7. Org admin 在 Synapse 内配置应用凭证

登录 → 进入所在组织 → 左侧菜单 **集成** → **飞书应用凭证** 卡片:
1. 填入第 1 步拿到的 App ID + App Secret
2. 复制卡片上展示的回调地址,加入第 3 步飞书后台的白名单
3. 保存

配置完成后,组织成员就能在同一页点 **连接飞书账号** 走 OAuth,以及点 **一键导入飞书文档** 触发同步。

## 用户侧流程

### A. 连接飞书账号

前端 "集成" 页 → **飞书 Lark** 卡片 → 点 **连接飞书账号**:
1. 前端 `POST /api/v2/orgs/:slug/integrations/feishu/connect` 拿 `auth_url` + `state`(HMAC 签的)
2. `window.location.href = auth_url` 跳到飞书授权页
3. 用户同意后飞书 302 回 `/api/v2/integrations/feishu/callback?code=...&state=...`
4. 后端校验 state → `ExchangeCode` → 存 `user_integrations` → 302 回 `frontend_redirect_url?feishu=success`
5. 前端 toast 成功,卡片切到"已连接"形态

**Admin 未配置 App 凭证时**:步骤 1 返 412 Precondition Required。前端 button 根据 GET config 的 `configured` 字段 disable 并显示"请先配置飞书应用"。

### B. 一键导入

"已连接"形态的卡片上点 **一键导入飞书文档**:
1. `POST /api/v2/orgs/:slug/integrations/feishu/sync` → 后端建 async job,返 `{job_id}`
2. 前端每 1.5s `GET /api/v2/async-jobs/:id` 轮询
3. 终态(succeeded/failed)时停止轮询 + toast 结果 + 刷新"上次同步时间"

幂等:已有活跃 job 时,后端返 `already_running=true` + 同一个 `job_id`,前端仍能继续轮询已有任务。

### C. 用户撤销授权

点"断开授权" → `DELETE /api/v2/orgs/:slug/integrations/feishu` → 删 `user_integrations` 行。
已拉进来的文档不受影响。

## 同步策略

### 触发方式

当前只有**用户主动触发**(前端"一键导入"按钮)。无后台定时同步 —— 如有需求,建议在
`cmd/synapse` 里起 ticker goroutine 调 `feishusync.SyncOneUser`,复用现有的 `common.AsyncRunner`,
不需要单独的 binary。

### 增量策略

- `user_integrations.last_sync_at` 记录上次 sync 完成时间
- `Adapter.Sync(since=last_sync_at)` 只返 modified_time > since 的文件
- 首次(`last_sync_at` NULL)走全量
- 飞书 API 不返"已删"信号(list 接口没这维度),删除事件需要 webhook;**MVP 不处理删除**,
  用户触发同步后 drive 里消失的文档在 Synapse 里仍保留(后续可以加 reconcile 对账任务)

## 数据映射

| 飞书 | Synapse documents |
|---|---|
| file_token + type | `source_ref` = `{"file_token":"doxcnXXX","type":"docx","space_id":"wikcnYYY"}` |
| file type | `source_type` = `"feishu_doc"` |
| title | `title` + `file_name` (`.md` 后缀) |
| blocks(heading 树) | `content` = 渲染后的 markdown(走 markdown_structured chunker) |
| owner_id / modified_at | chunks.metadata 里(将来用于 agent 引用回链) |
| user 归属 | `uploader_id` = 发起授权的 Synapse user_id |

## 限制与 TODO

**当前实现覆盖:**
- ✅ Per-org 应用凭证配置(admin 前端自助)
- ✅ Drive 文件遍历 + wiki 节点遍历
- ✅ docx 内容 → markdown 转换(heading / paragraph / list / code / quote / todo / divider / inline bold|italic|inline_code|link|strikethrough)
- ✅ User token OAuth + refresh 轮换 + 持久化
- ✅ source_ref 驱动的 upsert(多次 Sync 同文件自动 overwrite 不重建)
- ✅ 前端"一键导入" + asyncjob 异步执行 + 轮询进度

**暂未实现(占位):**
- ⏸ 后台定时同步(需求明确后按前述方式在 `cmd/synapse` 内置 ticker)
- ⏸ Webhook 接收(`drive.file.updated_v1` / `drive.file.deleted_v1`)
- ⏸ Sheet / Bitable / Mindnote / 幻灯片内容(MVP 只 docx,其他 Fetch 返 ErrUnsupportedType)
- ⏸ 表格 / 图片 block 渲染(blocks_to_md.go 里目前输出占位文本)
- ⏸ Drive 删除事件对账
- ⏸ refresh_token 失效的用户侧通知(前端需要知道要重新授权)
- ⏸ app_secret KMS/envelope 加密(T4 合规一起升级,和 refresh_token 明文存策略一致)

## 运维 Checklist

**部署管理员:**
- [ ] `config.yaml` 的 `feishu.redirect_uri` + `frontend_redirect_url` 填对(base_url 按区域选)
- [ ] 跑 `cmd/migrate` 建 `user_integrations` / `org_feishu_configs` / `async_jobs` 表
- [ ] 监控告警:asyncjob 失败率 / 单用户 token 过期率

**每个启用飞书集成的组织 admin:**
- [ ] 在飞书开放平台创建自建应用 + 开齐 scope + 版本已发布
- [ ] Synapse 集成页填入 App ID / App Secret
- [ ] 复制卡片上的回调地址加到飞书后台"安全设置 → 重定向 URL"白名单
