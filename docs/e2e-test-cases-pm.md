# PM 模块端到端测试用例(PR-A + PR-B + PR-C)

覆盖 2026-04-28 上线的 5 个 commit(`ef67633`..`f034591`)的所有用户可观测能力。
每个用例独立,可单独跑;按"操作 → 期望 → 验证"三段式,验证既可以走 SQL 也可以走 HTTP GET。

> **谁用这份文档**:你(用户)做端到端验证时,按用例操作 UI 或 curl,然后让 LLM(我)
> 用 SQL / log 验证。用例号是稳定锚点(测试报告里报"Test 4 失败" 我能立即定位)。

## 0. 前置条件

```bash
# 0.1 服务在跑(host:8080),mysql 在 host:13306
docker ps | grep synapse-synapse-1   # Up (healthy)
curl -s http://127.0.0.1:8080/health  # {"code":200,"message":"ok"}

# 0.2 JWT 准备(以 user_id=2 / eyrihe998@gmail.com 为例)
# 用项目的 jwt secret + redis session 自签一份,有效 1 小时
python3 -m venv /tmp/synapse-test-venv && /tmp/synapse-test-venv/bin/pip install --quiet pyjwt redis
cat > /tmp/gen_token.py <<'EOF'
import json, secrets, time, jwt, redis
SECRET = "synapse-dev-secret-change-me"
USER_ID = 2; DEVICE_ID = "e2e-test"
now = int(time.time()); jti = secrets.token_hex(16)
payload = {"user_id":USER_ID,"email":"eyrihe998@gmail.com","device_id":DEVICE_ID,"type":"access",
           "jti":jti,"iss":"synapse","sub":str(USER_ID),"iat":now,"exp":now+3600,"nbf":now}
token = jwt.encode(payload, SECRET, algorithm="HS256")
r = redis.Redis(host="127.0.0.1", port=6379, db=0)
r.setex(f"synapse:session:{USER_ID}:{DEVICE_ID}", 3600, json.dumps({
  "jti":jti,"device_name":"e2e","login_ip":"127.0.0.1","login_at":now,"session_start_at":now
}))
print(token)
EOF
TOKEN=$(/tmp/synapse-test-venv/bin/python3 /tmp/gen_token.py)
echo "TOKEN=$TOKEN" > /tmp/synapse-token.env

# 0.3 后续命令统一加 -H "Authorization: Bearer $TOKEN"
```

`org_id=1` 是默认测试 org;前端 UI 触发的请求等价于 `curl -H "Authorization: Bearer $JWT"`。

## A 组: PM 基础 CRUD

### Test 1 — 创建 project 自动 seed default initiative + Backlog version + Project Console channel

**操作**:
- UI:进 org → projects 页 → 点"新建项目" → 名字 `e2e-test-1` → 提交
- 或 curl: `curl -X POST -H "$H" -H "Content-Type: application/json" -d '{"org_id":1,"name":"e2e-test-1"}' http://127.0.0.1:8080/api/v2/projects`

**期望**:
- 响应 `code=200, message="project created"`,带 `result.id=N`(记下)

**验证**(把 `<PID>` 替换成上一步的 id):
```sql
-- (a) project 行存在,owner = 操作人
SELECT id, org_id, name, created_by FROM projects WHERE id = <PID>;
-- 期望: 1 行, created_by=<操作 user_id>

-- (b) default initiative 自动建出来
SELECT id, name, status, is_system FROM initiatives
 WHERE project_id = <PID> AND is_system = TRUE;
-- 期望: 1 行, name='Default', status='active', is_system=1

-- (c) Backlog version 自动建出来
SELECT id, name, status, is_system FROM versions
 WHERE project_id = <PID> AND is_system = TRUE;
-- 期望: 1 行, name='Backlog', status='active', is_system=1

-- (d) Project Console channel 自动建,owner + Architect 都加入
SELECT c.id, c.kind, c.name FROM channels c WHERE c.project_id = <PID> AND c.kind = 'project_console';
SELECT cm.principal_id, cm.role
  FROM channel_members cm
  JOIN channels c ON c.id = cm.channel_id
  WHERE c.project_id = <PID> AND c.kind = 'project_console';
-- 期望: console channel 1 行;成员 2 行 — owner (user 的 principal_id) 和 admin (architect_pid=204)
```

### Test 2 — Initiative CRUD + 防同名

**操作**(Project 沿用 Test 1 的 PID):
1. 创建 initiative `e2e-themed-1`, target_outcome="validate cross-version"
2. 再次创建同名 initiative
3. PATCH 改 status='active'
4. archive

**curl**:
```bash
H="Authorization: Bearer $TOKEN"; BASE="http://127.0.0.1:8080/api/v2"

# 1. create
curl -s -H "$H" -H "Content-Type: application/json" \
  -d '{"name":"e2e-themed-1","target_outcome":"validate cross-version"}' \
  "$BASE/projects/<PID>/initiatives"
# 期望: code=200, result.id=<INIT_ID> (记下)

# 2. 重名(在 active 状态下应该 409)
curl -s -H "$H" -H "Content-Type: application/json" \
  -d '{"name":"e2e-themed-1"}' "$BASE/projects/<PID>/initiatives"
# 期望: code=409290031, message="initiative name duplicated"

# 3. 改 status
curl -s -X PATCH -H "$H" -H "Content-Type: application/json" \
  -d '{"status":"active"}' "$BASE/initiatives/<INIT_ID>"
# 期望: code=200, message="initiative updated"

# 4. archive(无未归档 workstream → 成功)
curl -s -X POST -H "$H" "$BASE/initiatives/<INIT_ID>/archive"
# 期望: code=200, message="initiative archived"
```

**SQL 验证**:
```sql
SELECT status, archived_at FROM initiatives WHERE id = <INIT_ID>;
-- 期望: status='completed', archived_at 非空
```

### Test 3 — Version CRUD + status='released' 转换

**操作**(同 PID):
1. 创建 version `v1.0` status='planning'
2. PATCH status='active'
3. PATCH status='released' + released_at='2026-05-01T00:00:00Z'
4. 再次创建同名 → 应失败

**curl**:
```bash
# 1.
curl -s -H "$H" -H "Content-Type: application/json" \
  -d '{"name":"v1.0","status":"planning"}' "$BASE/projects/<PID>/versions"
# 记下 result.id 为 <VER_ID>

# 2. → 3. → 4.(顺序操作)
curl -s -X PATCH -H "$H" -H "Content-Type: application/json" \
  -d '{"status":"active"}' "$BASE/versions/<VER_ID>"

curl -s -X PATCH -H "$H" -H "Content-Type: application/json" \
  -d '{"status":"released","released_at":"2026-05-01T00:00:00Z"}' "$BASE/versions/<VER_ID>"

curl -s -H "$H" -H "Content-Type: application/json" \
  -d '{"name":"v1.0","status":"planning"}' "$BASE/projects/<PID>/versions"
# 期望第 4 步: code=409290041, message="version name duplicated"
```

**SQL 验证**:
```sql
SELECT status, released_at FROM versions WHERE id = <VER_ID>;
-- 期望: status='released', released_at='2026-05-01 00:00:00'
```

### Test 4 — Workstream lifecycle + 自动 channel lazy-create + 反指

**目的**: 验证 PR-B B1 的 pm event consumer 真在工作。

**操作**(用 Test 2 的 INIT_ID + Test 3 的 VER_ID):
1. 在 INIT_ID 下创建 workstream,挂 VER_ID
2. **等 1-2 秒**(consumer 异步)
3. GET workstream 看 channel_id 反指
4. PATCH workstream version_id=0(移到 backlog)
5. PATCH 改 status='done'

**curl**:
```bash
# 重新建一个未 archive 的 initiative 给 workstream 用(因为 Test 2 archive 了 INIT_ID)
INIT2=$(curl -s -H "$H" -H "Content-Type: application/json" \
  -d '{"name":"e2e-themed-2"}' "$BASE/projects/<PID>/initiatives" \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['result']['id'])")

# 1. 建 workstream
WS=$(curl -s -H "$H" -H "Content-Type: application/json" \
  -d "{\"version_id\":<VER_ID>,\"name\":\"e2e-feature-x\"}" \
  "$BASE/initiatives/$INIT2/workstreams" \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['result']['id'])")

# 2. 等 lazy-create
sleep 2

# 3. 验证 channel_id 已反指
curl -s -H "$H" "$BASE/workstreams/$WS"
# 期望: result.channel_id 非空

# 4. 移到 backlog
curl -s -X PATCH -H "$H" -H "Content-Type: application/json" \
  -d '{"version_id":0}' "$BASE/workstreams/$WS"

# 5. 完成
curl -s -X PATCH -H "$H" -H "Content-Type: application/json" \
  -d '{"status":"done"}' "$BASE/workstreams/$WS"
```

**SQL 验证**:
```sql
-- workstream channel 已建 + 反指
SELECT id, channel_id, version_id, status FROM workstreams WHERE id = <WS_ID>;
-- 期望: channel_id 非 NULL, version_id NULL(已移 backlog), status='done'

-- workstream channel 元数据
SELECT id, kind, name, workstream_id FROM channels WHERE workstream_id = <WS_ID>;
-- 期望: 1 行, kind='workstream', name='e2e-feature-x'

-- workstream channel 成员(owner + top-orchestrator auto-include,Architect 不在场,见决策 4)
SELECT cm.principal_id, cm.role,
  COALESCE(u.email, a.agent_id) AS who
  FROM channel_members cm
  LEFT JOIN users u ON u.principal_id = cm.principal_id
  LEFT JOIN agents a ON a.principal_id = cm.principal_id
  JOIN channels c ON c.id = cm.channel_id
  WHERE c.workstream_id = <WS_ID>;
-- 期望: 2 行 — owner (user) 和 member (synapse-top-orchestrator);
--      不应有 synapse-project-architect
```

**事件流验证(可选)**:
```bash
# 看 pm 事件流(应至少 2 个事件:initiative.created + workstream.created)
docker exec redis redis-cli XLEN synapse:pm:events
docker exec redis redis-cli XINFO GROUPS synapse:pm:events
# 期望: lag=0, pending=0

# 看 consumer log
docker logs synapse-synapse-1 2>&1 | grep "pmevent: workstream channel created" | tail -5
```

### Test 5 — Workstream invite_to_workstream

**操作**(用 Test 4 的 WS_ID — 此时仍未 archive,但 status='done'。invite 不要求 active,`status != cancelled` 即可):
- HTTP 没有 invite 路由,只能走 MCP tool。**或者** SQL 直查 workstream channel 当前成员模拟。

**MCP tool 路径(等待 OAuth client 接入后跑)**:
```
{ "name": "invite_to_workstream",
  "arguments": { "workstream_id": <WS_ID>, "principal_ids": [3, 121] } }
```

**SQL 验证**:
```sql
-- channel_members 该 channel 应新增 2 行(principal_id IN (3, 121))
SELECT principal_id, role FROM channel_members
  WHERE channel_id = (SELECT channel_id FROM workstreams WHERE id = <WS_ID>)
    AND principal_id IN (3, 121);
-- 期望: 2 行,role='member'
```

### Test 6 — Project KB ref attach + 二选一守卫 + UNIQUE 守卫

**操作**:
1. attach kb_source_id=999
2. 重复 attach(应被拒)
3. 同时传 source + doc(应被拒)
4. detach

**curl**:
```bash
# 1.
curl -s -H "$H" -H "Content-Type: application/json" \
  -d '{"kb_source_id":999}' "$BASE/projects/<PID>/kb-refs"
# 期望: code=200, result.id=<REF_ID>

# 2. 重复
curl -s -H "$H" -H "Content-Type: application/json" \
  -d '{"kb_source_id":999}' "$BASE/projects/<PID>/kb-refs"
# 期望: code=409290061, message="project kb ref already attached"

# 3. 二选一
curl -s -H "$H" -H "Content-Type: application/json" \
  -d '{"kb_source_id":1,"kb_document_id":1}' "$BASE/projects/<PID>/kb-refs"
# 期望: code=400290060

# 4. detach
curl -s -X DELETE -H "$H" "$BASE/project-kb-refs/<REF_ID>"
# 期望: code=200, message="kb ref detached"
```

**SQL 验证**:
```sql
SELECT COUNT(*) FROM project_kb_refs WHERE project_id = <PID>;
-- 期望: 0(经过第 4 步 detach 之后)
```

### Test 7 — Roadmap 聚合视图

**操作**:
```bash
curl -s -H "$H" "$BASE/projects/<PID>/roadmap"
```

**期望 JSON**:
```json
{
  "code": 200,
  "result": {
    "project_id": <PID>,
    "initiatives": [{ "id":..., "name":"Default", "is_system":true, ... }, ...],
    "versions":    [{ "id":..., "name":"Backlog","is_system":true, ... }, { "id":<VER_ID>, "name":"v1.0", ... }],
    "workstreams": [{ "id":<WS_ID>, "name":"e2e-feature-x", ... }]
  }
}
```

**验证要点**:
- `initiatives` 不含 archived 行(Test 2 archive 的 `e2e-themed-1` 不出现)
- `versions` 不含 status='cancelled' 行
- `workstreams` 不含 archived 行
- 所有外键 ID(workstream.initiative_id / version_id)在同 payload 里能找到

## B 组: 系统守卫

### Test 8 — system initiative / Backlog version 不可改

**操作**(用 Test 1 创建的 default initiative 和 Backlog version):
```bash
# default initiative 拒改 status
curl -s -X PATCH -H "$H" -H "Content-Type: application/json" \
  -d '{"status":"completed"}' "$BASE/initiatives/<DEFAULT_INIT_ID>"
# 期望: code=400290035, message="system initiative cannot be modified"

# default initiative 拒 archive
curl -s -X POST -H "$H" "$BASE/initiatives/<DEFAULT_INIT_ID>/archive"
# 期望: code=400290035

# Backlog version 拒改
curl -s -X PATCH -H "$H" -H "Content-Type: application/json" \
  -d '{"status":"released"}' "$BASE/versions/<BACKLOG_VER_ID>"
# 期望: code=400290043, message="system version cannot be modified"
```

### Test 9 — archive 含 active workstream 的 initiative 应被拒

**操作**:
```bash
# 建一个 initiative + 在它下面建一个 active workstream → 然后 archive initiative
INIT3=$(curl -s -H "$H" -H "Content-Type: application/json" \
  -d '{"name":"e2e-not-empty"}' "$BASE/projects/<PID>/initiatives" \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['result']['id'])")
curl -s -H "$H" -H "Content-Type: application/json" \
  -d '{"name":"blocking-ws"}' "$BASE/initiatives/$INIT3/workstreams"

# 试图 archive
curl -s -X POST -H "$H" "$BASE/initiatives/$INIT3/archive"
# 期望: code=400290034, message="initiative has active workstreams"
```

## C 组: 数据迁移正确性(老数据)

### Test 10 — 存量 23 个 project 都已 seed

**仅 SQL,不需要操作**:
```sql
-- 23 个 project 应有 23 个 default initiative
SELECT
  (SELECT COUNT(*) FROM projects WHERE archived_at IS NULL) AS active_projects,
  (SELECT COUNT(*) FROM initiatives WHERE is_system AND name='Default') AS default_initiatives,
  (SELECT COUNT(*) FROM versions WHERE is_system AND name='Backlog') AS backlog_versions,
  (SELECT COUNT(*) FROM channels WHERE kind='project_console') AS console_channels;
-- 期望: 4 个数字相等(各 23,或当前 active project 数)
```

### Test 11 — 老 channel_kb_refs / channel_versions 表已 DROP

```sql
SHOW TABLES LIKE 'channel_kb_refs';
SHOW TABLES LIKE 'channel_versions';
-- 期望: 两个都返空(0 rows)
```

### Test 12 — versions.status 老枚举值已升级

```sql
-- 不应有 'planned' / 'in_progress' 残留
SELECT status, COUNT(*) FROM versions GROUP BY status;
-- 期望: 只见 'planning' / 'active' / 'released' / 'cancelled' 四种合法值
```

## D 组: Architect 集成(挂起,等真实 LLM 调用验证)

### Test 13 — Architect agent seed 正确

```sql
SELECT id, agent_id, kind, principal_id, auto_include_in_new_channels
  FROM agents WHERE agent_id = 'synapse-project-architect';
-- 期望: 1 行, kind='system', auto_include_in_new_channels=0,
--      principal_id 非 0(实际部署里 = 204)
```

### Test 14 — Architect 进入每个 Console channel 的 admin

```sql
-- 当前所有 console channel 都应有 architect 作为 admin
SELECT
  COUNT(DISTINCT c.id) AS console_count,
  COUNT(*) AS architect_member_rows
  FROM channels c
  LEFT JOIN channel_members cm ON cm.channel_id = c.id
    AND cm.principal_id = (SELECT principal_id FROM agents WHERE agent_id = 'synapse-project-architect')
  WHERE c.kind = 'project_console';
-- 期望: console_count = architect_member_rows(每个 console 都有 architect 行)
```

### Test 15 — 在 Console channel @ Architect 触发 LLM(待 UI / 真实测)

**操作**(等 Console channel UI 入口完成):
1. 进任意 project 的 Project Console channel
2. 发消息 `@Synapse Architect 帮我建一个 v2.0 + 一个 'API 重构' workstream`
3. 等 5-10 秒

**期望**:
- channel 内 Architect 发回一条消息(post_message tool 触发)
- 通过 SQL 看 audit_events / llm_usage 有新行
- 视决策可能新建了 version 'v2.0' + workstream 'API 重构'

```sql
SELECT * FROM audit_events ORDER BY id DESC LIMIT 5;
SELECT * FROM llm_usage ORDER BY id DESC LIMIT 5;
SELECT id, name, status FROM versions
  WHERE project_id = <PID> AND name = 'v2.0';
SELECT id, name, status FROM workstreams
  WHERE project_id = <PID> AND name = 'API 重构';
```

> 这个用例依赖 LLM 真实调用 + prompt 是否引导 Architect 正确动作,可能需要多轮 prompt 调优。
> 第一次跑出错很正常 — 让我看 log 找根因。

## 附录 A:API 路径速查

```
POST   /api/v2/projects                                     创建 project
GET    /api/v2/projects?org_id=<oid>                        列出 org 下 project
GET    /api/v2/projects/:id                                 项目详情
POST   /api/v2/projects/:id/archive                         归档 project
POST   /api/v2/projects/:id/initiatives                     创建 initiative
GET    /api/v2/projects/:id/initiatives                     列出 initiative
POST   /api/v2/projects/:id/versions                        创建 version
GET    /api/v2/projects/:id/versions                        列出 version
GET    /api/v2/projects/:id/workstreams                     列出 workstream(by-project)
POST   /api/v2/projects/:id/kb-refs                         挂 KB(source / doc 二选一)
GET    /api/v2/projects/:id/kb-refs                         列 KB 挂载
GET    /api/v2/projects/:id/roadmap                         聚合视图(initiatives + versions + workstreams)

GET    /api/v2/initiatives/:id                              单个 initiative
PATCH  /api/v2/initiatives/:id                              更新
POST   /api/v2/initiatives/:id/archive                      归档
POST   /api/v2/initiatives/:id/workstreams                  在 initiative 下建 workstream
GET    /api/v2/initiatives/:id/workstreams                  列 initiative 的 workstream

GET    /api/v2/versions/:id                                 单个 version
PATCH  /api/v2/versions/:id                                 改 status / target_date / released_at
GET    /api/v2/versions/:id/workstreams                     列 version 下交付的 workstream

GET    /api/v2/workstreams/:id                              单个 workstream
PATCH  /api/v2/workstreams/:id                              改 name / description / status / version_id

DELETE /api/v2/project-kb-refs/:ref_id                      卸载 KB
```

## 附录 B:错误码索引(模块号 29 = pm)

```
400290010   pm invalid request(泛用)
400290020   project name invalid
409290021   project name duplicated
400290022   project archived
400290030   initiative name invalid
409290031   initiative name duplicated
400290032   initiative status invalid
400290033   initiative archived
400290034   initiative has active workstreams
400290035   system initiative cannot be modified
400290040   version name invalid
409290041   version name duplicated
400290042   version status invalid
400290043   system version cannot be modified
400290050   workstream name invalid
400290051   workstream status invalid
400290052   workstream initiative invalid
400290053   workstream version invalid
400290060   project kb ref invalid (二选一守卫)
409290061   project kb ref already attached
403290010   forbidden(actor 非 org 成员)
404290010   project not found
404290020   initiative not found
404290030   version not found
404290040   workstream not found
404290050   project kb ref not found
500290010   pm internal error
```
