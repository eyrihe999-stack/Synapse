# M1 身份与登录 — 当前运作

> 本文档只描述**当前代码是怎么跑的**,不讨论设计问题与优化方案。问题与方案见 `docs/production-roadmap.md#m1-身份与登录`,对应代码以 `file:line` 锚点给出。

## 1. 模块边界

`internal/user/` 负责:注册、登录、访问令牌发放与轮换、按设备维度的 session 管理(查询 / 踢单台 / 全部登出)、个人资料读写。

依赖:
- **MySQL** — `users` 表(唯一数据源)
- **Redis** — 活跃 session 存储(真源)
- **JWT**(`internal/common/jwt/jwt.go`)— 无状态 access / refresh token 签发与校验
- **中间件**(`internal/common/middleware/auth.go`)— 请求鉴权注入

下游:几乎所有需要"用户身份"的模块都通过 `middleware.GetUserID(c)` 读注入值,不直查 user 表。

---

## 2. 拓扑图

```
HTTP 入口 (gin)
  │
  ├─ middleware.IPRateLimit(30/min, 1min window)          ← in-memory,sync.Map
  │    │
  │    ▼
  │   /api/v1/auth/register        ┐
  │   /api/v1/auth/login           ├─► handler.Handler ─► service.UserService
  │   /api/v1/auth/refresh         ┘                            │
  │                                                             ├─► repository.Repository (MySQL: users)
  │                                                             ├─► internal/common/jwt.JWTManager
  │                                                             └─► user.SessionStore (Redis)
  │
  └─ middleware.JWTAuthWithSession                         ← JWT 解码 + Redis session 存在性检查
         │
         ▼
       /api/v1/users/me              ┐
       /api/v1/users/me/sessions     ├─► handler.Handler ─► service.UserService ─► 同上
       /api/v1/users/me/sessions/... ┘
```

---

## 3. 数据底座

### 3.1 MySQL `users`

`internal/user/model/models.go`

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | uint64 PK | 自增 |
| `email` | varchar(255) | **唯一索引** `uk_users_email` |
| `password_hash` | varchar(255) | bcrypt(cost=10) |
| `display_name` | varchar(64) | 注册时默认 = email |
| `avatar_url` | varchar(512) | |
| `status` | int32 | `1=active` / `2=banned` |
| `last_login_at` | *time.Time | 每次登录 best-effort 更新 |
| `created_at` / `updated_at` | time.Time | |
| `deleted_at` | gorm.DeletedAt | 软删(当前无删除路径) |

### 3.2 Redis session

`internal/user/session.go` + `internal/user/service/session_store.go:20-69`

- **Key**: `synapse:session:{user_id}:{device_id}`
- **Value**: JSON
  ```go
  type SessionInfo struct {
      JTI        string // 当前 refresh token 的 JTI
      DeviceName string
      LoginIP    string
      LoginAt    int64  // unix seconds
  }
  ```
- **TTL**: `refresh_token_duration`(默认 7 天),每次 Save 重置
- **List 实现**: `SCAN synapse:session:{user_id}:*`(cursor 批 100 条)

### 3.3 JWT

`internal/common/jwt/jwt.go`

- **算法**: HS256,单 key(`cfg.JWT.SecretKey`)签所有 token
- **两种类型**: `AccessToken`(TTL 2h)/ `RefreshToken`(TTL 7d),`type` 字段区分,校验时强制断言
- **Claims**:
  ```go
  type CustomClaims struct {
      UserID   uint64
      Email    string
      DeviceID string
      Type     TokenType   // "access" / "refresh"
      Role     string      // 当前代码中未使用
      jwt.RegisteredClaims // jti/iss/sub/iat/exp/nbf
  }
  ```
- **JTI**: 每次签 token 随机 16B hex,`ID` 字段承载
- **Issuer** 默认 `"synapse"`;`Subject` = userID

---

## 4. 关键设计原则

理解下面流程前提先记住这 5 条。

### 4.1 Token 成对发放
每次**注册 / 登录 / 刷新**三路都产出 (access, refresh) 一对。走同一函数 `generateAuthResponse`(§6.3)。

### 4.2 Session 以 `(user_id, device_id)` 为主键
不是 per-user。每次请求必须带 `device_id`(空串默认 `"default"`)。5 设备上限也是针对**不同 device_id** 计数。

### 4.3 Refresh 靠 JTI 覆盖实现"一次一换"
- Redis `SessionInfo.JTI` 始终记录**最新** refresh token 的 JTI
- 成功 refresh 时用新 JTI 覆盖,旧 JTI 作废
- 旧 refresh token 再来: `session.JTI != claims.ID` → `ErrSessionRevoked`
- 等价于 "refresh 一次使用一次",无独立 blocklist

### 4.4 踢下线靠删 Redis 即可
- Access token 无服务端黑名单
- `JWTAuthWithSession` 每次请求都查一次 Redis session
- 删掉 session → 下一次用 access token 的请求直接 401
- **延迟上限 = 当前请求的剩余生命周期**(SSE 长连接不会被立即切)

### 4.5 `JWTAuth` vs `JWTAuthWithSession`
- `JWTAuth`:只校验 JWT 签名 + 过期(快,但踢不动设备)
- `JWTAuthWithSession`:JWT + Redis session 存在性(慢一跳,但踢设备 1 次请求就生效)
- 用户模块所有受保护接口用后者(`router.go:30`)

---

## 5. 中间件栈

`internal/common/middleware/auth.go`

| 中间件 | 行为 | 注入到 `gin.Context` |
|---|---|---|
| `JWTAuth` | Authorization Bearer → ValidateAccessToken → 通过即放行 | `user_id / user_email / device_id / jwt_claims` |
| `JWTAuthWithSession` | 同上 + `sessionStore.Get(userID, deviceID)`;不存在直接 401 `"Session revoked"` | 同上 |
| `OptionalJWTAuth` | 有 token 且合法就注入,没有或非法也放行 | 有就注入,无则空 |
| `BearerAuth` | OAuth access token 优先,失败回落到 web JWT | OAuth 路径注入 `oauth_claims`;JWT 路径同 `JWTAuth` |
| `IPRateLimit(max, window)` | per-IP 固定窗口计数,超限 429 | — |

`IPRateLimit`(`ip_rate_limit.go`)细节:
- 内存 `sync.Map[ip]*bucket`
- 清理 goroutine 每 `window*5`(至少 1min)扫一遍过期 bucket
- **多实例部署时各自独立计数**
- `ip = c.ClientIP()`

---

## 6. 核心流程

### 6.1 注册

```
POST /api/v1/auth/register
body: {email, password, device_id?, device_name?}
  │
  ▼ IPRateLimit(30/min)
  ▼ handler.Register (handler.go:27)
     ├─ ShouldBindJSON         ← email / password required,仅非空校验
     ├─ req.LoginIP = c.ClientIP()
     ├─ req.DeviceID defaults "default"
     ▼
  service.Register (service.go:112)
     1. mail.ParseAddress(email)      → 失败返 ErrInvalidEmail
     2. len(password) >= 8            → 不足返 ErrPasswordTooShort
     3. repo.FindByEmail(email)
          ├─ 命中: ErrEmailAlreadyRegistered
          └─ ErrRecordNotFound: 继续
     4. bcrypt.GenerateFromPassword(pwd, DefaultCost=10)
     5. repo.CreateUser(User{email, hash, display_name=email||display, status=1 active})
          └─ MySQL 1062(TOCTOU 并发注册): 兜底返 ErrEmailAlreadyRegistered
     6. generateAuthResponse (§6.3)
     ▼
  response.Created { access_token, refresh_token, expires_in, user }
```

### 6.2 登录

```
POST /api/v1/auth/login
body: {email, password, device_id?, device_name?}
  │
  ▼ IPRateLimit(30/min)
  ▼ handler.Login (handler.go:49)
  ▼ service.Login (service.go:176)
     1. repo.FindByEmail → 不存在: ErrInvalidCredentials
     2. bcrypt.CompareHashAndPassword → 不匹配: ErrInvalidCredentials
     3. repo.UpdateFields(id, {last_login_at: now})   ← best-effort,失败不阻塞
     4. generateAuthResponse (§6.3)
```

> **注:** `status=2(banned)` 在当前代码中没有校验点,banned 用户同样可登录。(这是个现状事实,调整方案另议。)

### 6.3 generateAuthResponse(三路共享)

`service.go:326`

```
参数: (user, deviceID, deviceName, loginIP)

1. accessToken, _  = jwtManager.GenerateAccessToken(uid, email, deviceID)
   → HS256 JWT, exp=2h, jti=随机16B

2. refreshToken, _ = jwtManager.GenerateRefreshToken(uid, email, deviceID)
   → HS256 JWT, exp=7d, jti'=另一随机16B

3. refreshClaims   = jwtManager.ValidateRefreshToken(refreshToken)
   └─ 自己再解一次只为拿 jti'(签的时候没回传 jti)

4. 多设备上限检查(仅新设备算):
     existing, _ = sessionStore.Get(uid, deviceID)
     if existing == nil:
       sessions = sessionStore.List(uid)
       if len(sessions) >= max_sessions_per_user (默认 5):
         return ErrSessionLimitReached

5. sessionStore.Save(uid, deviceID,
      SessionInfo{JTI: jti', device_name, login_ip, login_at: now},
      ttl = 7d)
   └─ 同 deviceID 已存在即覆盖(旧 jti 作废)

6. 返回 AuthResponse{
     access_token:  accessToken,
     refresh_token: refreshToken,
     expires_in:    7200s (access TTL),
     user:          UserProfile
   }
```

### 6.4 刷新

```
POST /api/v1/auth/refresh
body: {refresh_token, device_id?, device_name?}
  │
  ▼ IPRateLimit(30/min)
  ▼ handler.RefreshToken (handler.go:71)
  ▼ service.RefreshToken (service.go:240)

  1. claims = jwtManager.ValidateRefreshToken(refresh_token)
     → 签名错 / 过期 / 类型非 refresh: ErrInvalidRefreshToken

  2. deviceID = claims.DeviceID || "default"

  3. session = sessionStore.Get(userID, deviceID)
     ├─ Redis 无此 key(被踢 / TTL 到期): ErrSessionRevoked
     └─ session.JTI != claims.ID(被下一次 refresh 顶掉): ErrSessionRevoked

  4. user = repo.FindByID(userID)
     └─ 不存在: ErrUserNotFound

  5. device_name / login_ip 取请求值 || session 原值

  6. generateAuthResponse ← 新 jti 覆盖 Redis(§6.3 第 5 步)
```

### 6.5 受保护接口访问

```
任意 /api/v1/users/me* 请求
  │
  ▼ JWTAuthWithSession (auth.go:106)

  1. Authorization: Bearer <token>
  2. parts split on space → Bearer + token
  3. claims = jwtManager.ValidateAccessToken(token)
     ├─ ErrExpiredToken → 401 "Token has expired"
     └─ 其他 → 401 "Invalid token"
  4. deviceID = claims.DeviceID || "default"
  5. sessionStore.Get(claims.UserID, deviceID)
     └─ err(key 不存在)→ 401 "Session revoked"
  6. c.Set("user_id", claims.UserID)
     c.Set("user_email", claims.Email)
     c.Set("device_id", deviceID)
     c.Set("jwt_claims", claims)
  7. c.Next()

  ▼ handler
     userID, _ = middleware.GetUserID(c)
     ...
```

### 6.6 Session 管理三路

共用 `JWTAuthWithSession` 前置。

| 路由 | handler | service 调用 | Redis 动作 |
|---|---|---|---|
| `GET /users/me/sessions` | `ListSessions` (handler.go:130) | `svc.ListSessions` | `SCAN synapse:session:{uid}:*` → `GET` 每 key → 解析 device_id |
| `DELETE /users/me/sessions/:device_id` | `KickSession` (handler.go:147) | `svc.KickSession` | 先 `Get` 判存在(不存在返 `ErrSessionNotFound`),再 `DEL` |
| `POST /users/me/sessions/logout-all` | `LogoutAll` (handler.go:169) | `svc.LogoutAll` | `SCAN` + 批量 `DEL`(cursor 循环到 0) |

`ListSessions` 返回字段(`user.SessionEntry`):`device_id / device_name / login_ip / login_at`,不回传 JTI。

### 6.7 个人资料

| 路由 | handler | 行为 |
|---|---|---|
| `GET /users/me` | `GetProfile` | `repo.FindByID` → `UserProfile` DTO |
| `PATCH /users/me` | `UpdateProfile` | 仅支持 `display_name / avatar_url`(指针判存在),`UpdateFields` 后 `GetProfile` 返回 |

`UserProfile` 不含 `password_hash`(`json:"-"`)也不含 `deleted_at`。

---

## 7. 错误语义映射

`errors.go` 定义 sentinel + code;`handler/error_map.go` 把 sentinel 映射到 HTTP 响应。

编码约定: `HHHSSCCCC`(HTTP 状态码 + 模块号 01 + 业务码),业务错误走 HTTP 200 + body code,仅内部错 500。

| Sentinel | Code | 对应路径 |
|---|---|---|
| `ErrInvalidEmail` | 400010010 | Register |
| `ErrPasswordTooShort` | 400010011 | Register |
| `ErrInvalidCredentials` | 401010010 | Login |
| `ErrInvalidRefreshToken` | 401010011 | Refresh |
| `ErrSessionRevoked` | 401010012 | Refresh / JWTAuthWithSession |
| `ErrSessionLimitReached` | 403010010 | 任何 generateAuthResponse 新设备 |
| `ErrUserNotFound` | 404010010 | Refresh / GetProfile / UpdateProfile |
| `ErrSessionNotFound` | 404010011 | KickSession |
| `ErrEmailAlreadyRegistered` | 409010010 | Register(含 TOCTOU 兜底) |
| `ErrUserInternal` | 500010000 | 所有内部错(bcrypt / DB / Redis 失败) |

---

## 8. IP 限流的数据流

`middleware/ip_rate_limit.go`

```
┌────── 请求进入 gin ──────┐
│                           │
│  c.ClientIP()             │
│    ├─ gin 默认信任所有 proxy header
│    ├─ 优先读 X-Forwarded-For(首个值)
│    ├─ 次读 X-Real-IP
│    └─ 回退 RemoteAddr 的 IP 部分
│                           │
│  buckets[ip].count++      │
│  count > 30 → 429         │
└───────────────────────────┘
```

当前状态(事实陈述):
- `cmd/synapse/main.go` 启动 gin 时**未调用** `SetTrustedProxies` / `TrustedPlatform`,gin 使用默认信任策略(即所有 header 都认)
- `IPRateLimit` 仅挂在 `/api/v1/auth/*`;其他入口(含 `/api/v1/users/*`)无 IP 级限流
- 计数器内存态,进程重启归零;多副本部署各副本独立计数

---

## 9. 启动期装配

`cmd/synapse/main.go`

大致顺序(与 user 模块相关的部分):

1. 读 config → `cfg.JWT.SecretKey` / `cfg.JWT.AccessTokenDuration` / `cfg.JWT.RefreshTokenDuration` / `cfg.JWT.MaxSessionsPerUser`
2. 构造 `utils.NewJWTManager(JWTConfig{...})`
3. 构造 `service.NewSessionStore(redisClient, logger)`
4. 构造 `repo := repository.New(mysqlDB)`
5. 构造 `svc := service.NewUserService(repo, jwtMgr, sessionStore, maxSessions, logger)`
6. 构造 `h := handler.NewHandler(svc, logger)`
7. `handler.RegisterRoutes(r, h, jwtMgr, sessionStore)` — `router.go:22`
   - `/api/v1/auth/*` 挂 `IPRateLimit(30, 1min)`
   - `/api/v1/users/*` 挂 `JWTAuthWithSession(jwtMgr, sessionStore)`

同一个 `jwtMgr` 和 `sessionStore` 实例会被下游 org / agent / document 等模块在各自 `JWTAuthWithSession` 中复用。

---

## 10. 与其他模块的交接点

| 下游 | 读什么 | 怎么读 |
|---|---|---|
| organization | `user_id`、`user_email` | `middleware.GetUserID(c)` / `GetUserEmail(c)` |
| agent(web 路径)| 同上 | 同上 |
| agent(OAuth 路径)| OAuth claims 的 `subject = user_id` | `BearerAuth` 注入 `oauth_claims`,不走 user session |
| integration | `user_id` | 同 organization |

**注意:OAuth 路径(agent CLI / MCP)完全绕开 user 模块的 SessionStore**;它用 `internal/oauth/` 的独立 signing key 和 token 存储,不受"踢设备"影响。这是两套并行的凭证体系,只在 `middleware.BearerAuth` 里汇合。

---

## 附:涉及文件清单

```
internal/user/
├── const.go              状态枚举
├── errors.go             sentinel + code
├── session.go            SessionStore 接口 + DTO
├── migration.go          schema (users 表)
├── model/
│   └── models.go         User gorm 模型
├── repository/
│   └── repository.go     FindByEmail/FindByID/CreateUser/UpdateFields
├── service/
│   ├── service.go        UserService 核心业务
│   └── session_store.go  Redis SessionStore 实现
└── handler/
    ├── router.go         路由注册(含限流)
    ├── handler.go        HTTP 绑定
    └── error_map.go      sentinel → HTTP 响应

internal/common/middleware/
├── auth.go               JWTAuth / JWTAuthWithSession / OptionalJWTAuth / BearerAuth
└── ip_rate_limit.go      IPRateLimit(内存版)

internal/common/jwt/
└── jwt.go                JWTManager

config/
└── config.go / .yaml     JWTConfig
```
