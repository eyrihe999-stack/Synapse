# Nexus 模块目录结构规范

> 以 `internal/organization/` 为标杆，所有模块必须遵循此结构。

## 目录结构

```
internal/{module}/
├── const.go               ← 【必须】所有常量（超时、分页、时间格式、跨模块表名等），每个常量必须有中文注释
├── errors.go              ← 【必须】错误码常量（CodeXxx）+ sentinel errors（ErrXxx），统一入口
├── migration.go           ← 【按需】RunMigrations()：AutoMigrate + EnsureIndexes
├── handler/
│   ├── handler.go         ← Handler struct 定义 + NewXxxHandler 构造函数
│   ├── {resource}_handler.go ← 各资源 handler 方法（如 org_handler.go、member_handler.go）
│   ├── error_map.go       ← 【必须】handleServiceError（sentinel error → HTTP 响应映射）
│   ├── router.go          ← 【必须】RegisterRoutes()
│   └── middleware_handler.go ← 【按需】模块级中间件
├── dto/
│   └── dto.go             ← 请求/响应 DTO
├── model/
│   ├── models.go          ← GORM 数据模型 + 模型级常量（状态值、类型枚举等）
│   └── indexes.go         ← 索引定义 + EnsureXxxIndexes()
├── service/
│   ├── service.go         ← Service 接口定义 + 构造函数
│   ├── {feature}_service.go ← 业务逻辑（按功能拆分文件）
│   └── hooks.go           ← 【按需】跨模块事件钩子
└── repository/
    ├── repository.go      ← Repository struct 定义 + New 构造函数
    └── {resource}.go      ← 数据访问方法（按资源拆分文件）
```

## 关键规则

### 1. const.go 和 errors.go 只允许在模块根目录

- 子包（service/、handler/、model/ 等）**禁止**有独立的 `const.go` 或 `errors.go` 文件
- 所有常量和 sentinel errors 统一收敛到根目录，子包通过 import 根包引用
- model/models.go 中的模型级常量（如 `StatusActive = "active"`）是例外，因为它们与模型定义紧密绑定

### 2. handler 必须在子包中

- handler 放在 `handler/` 子包而非模块根目录
- 原因：避免 root ↔ service 循环依赖（root 持有 const/errors，service 需要引用它们）
- `RegisterRoutes()` 定义在 handler 包中，与 handler 类型同包
- handler 通过 import 根包引用错误码和 sentinel errors

### 2.1 错误映射必须独立文件

- `handleServiceError` 放在 `handler/error_map.go`，禁止写在 `handler.go` 中
- 错误映射函数集中了所有 sentinel error → HTTP 响应的映射，独立文件便于审计和维护

### 3. 代码规模约束

详细阈值和例外规则见 [code-limits.md](code-limits.md)。摘要：源文件/测试文件不超过 400 行，函数不超过 200 行，测试文件不超过 20 个顶层 Test 函数。均非硬性限制，强行拆分反而降低可读性时可适当放宽。

### 4. 注释规范

完整规则见 [annotation-rules.md](annotation-rules.md)（内容要求）+ [annotation-policy.md](annotation-policy.md)（语言政策：必须中文）。

### 5. 文件命名规范

- 禁止 `service2.go`、`utils.go`、`helpers.go` 等无语义命名
- 文件名必须反映其内容（如 `org_service.go`、`member_service.go`）

### 6. 硬编码 / 魔法值

禁止散落在各文件中的魔法值，必须收敛到 `const.go` 或 `model/models.go`。

### 7. 依赖方向（禁止反向）

```
handler/ → service（接口）、根包（const/errors）、dto、model
service  → 根包（const/errors）、dto、model、common
根包     → model（仅 migration 用）
```

根包**不**依赖 handler/ 和 service/，因此不存在循环依赖。

### 8. 异步 goroutine 管理

- 使用 `internal/common/async.AsyncRunner`，禁止裸 `go func()`
- AsyncRunner 由 main.go 创建并注入 service，shutdown 时由 main.go 调用 `runner.Wait()`

### 9. main.go 中的初始化模式

```go
// 创建 repository
xxxRepo := xxxrepo.New(db)

// 创建 service（注入 repo）
xxxSvc := xxxsvc.NewXxxService(xxxRepo, appLogger)

// 创建 handler
xxxH := xxxhandler.NewXxxHandler(xxxSvc, appLogger)

// 注册路由
xxxhandler.RegisterRoutes(r, xxxH, jwtManager, ...)

// --- shutdown 序列 ---
// 如有 AsyncRunner：xxxAsync.Wait()
```
