# 错误处理与日志规范

> 统一的 error return 路径规范：sentinel error 封装 + 日志覆盖。

---

## 一、错误处理通用规则

- 禁止裸 `return err`——service 层的包装格式见第二节 Sentinel Error 封装
- 禁止吞错误（`_ = err`、空 catch）
- 有意吞掉错误（best-effort 操作）时，必须用注释说明为什么选择吞掉而非返回
- 关键操作失败必须有回滚 / 补偿
- 非 main 包禁止 `log.Fatal` / `os.Exit`（`init()` 例外）
- `defer` 中的错误必须处理
- 类型断言必须用 `v, ok := x.(Type)` 双返回值形式

---

## 二、Sentinel Error 封装（service 层）

> **适用范围**：所有模块的 `service/` 包。只要包路径含 `service/` 且函数返回 error，就必须遵循本节规则。

### 基本要求

- 每个模块根目录必须有 `errors.go` 统一存放 sentinel error
- service 层返回的每个 error 都必须包含（是或包装）一个 `errors.go` 中预定义的 sentinel error，禁止返回匿名 `fmt.Errorf("...")` 或 `errors.New("...")`
- 需要携带动态上下文时用 `fmt.Errorf("上下文: %w: %w", err, ErrXxxInternal)` 包装（Go 1.20+ 多 `%w`），同时保留原始错误和 sentinel
- `gorm.ErrRecordNotFound` 等有业务含义的 DB 错误仍需翻译为具体的业务 sentinel error
- handler 的 `handleServiceError` 用 `errors.Is()` 覆盖所有 sentinel 分支

### 违规判定

以下情况视为违规：
- 直接 `return err`，未做任何包装
- `return fmt.Errorf("...: %w", err)` 只有一个 `%w`，未携带 sentinel
- `return errors.New("...")` 或 `return fmt.Errorf("...")`——匿名错误

以下情况**不算**违规：
- 已包含 sentinel：`return fmt.Errorf("...: %w: %w", err, organization.ErrOrgInternal)`
- 仅含 sentinel 无原始 error：`return fmt.Errorf("exceeds limit: %w", organization.ErrMaxOwnedOrgsReached)`
- 重复包装例外：内层已包装 sentinel，外层仅添加上下文透传

### 重复包装

沿调用链追踪，如果内层函数已用 `%w` 包装了某个 sentinel error，外层函数不应再次包装同一个 sentinel。

---

## 三、Handler 层错误路由

- handler 层必须用 `errors.Is()` 路由错误，禁止字符串比较
- handler 层错误分支必须覆盖 service 层所有可能返回的 sentinel error
- handler 层所有 sentinel error → HTTP 响应的映射必须集中在 `handleServiceError` 方法中，禁止在各 handler 方法内 inline 散写 `errors.Is` 判断
- **业务错误统一 HTTP 200**：所有 sentinel error 必须返回 HTTP 200 + body 中的业务错误码。仅以下情况使用非 200 状态码：协议错误 → 400、认证失败 → 401、内部错误 → 500

---

## 四、日志基础规范

- 必须使用结构化日志（`logger.LoggerInterface`），禁止 `fmt.Println` / `fmt.Printf`
- 日志级别合理：`Info`（里程碑）、`Warn`（降级）、`Error`（需人工介入）
- 所有级别的日志都必须携带当前可用的全部非隐私上下文字段（如 user_id、org_id 等业务标识）
- 请求链路上的代码必须统一使用 `*Ctx` 方法（`InfoCtx`、`WarnCtx`、`ErrorCtx`）；仅启动/关闭阶段允许使用普通版本
- 密码 / token / API key 禁止出现在日志中

---

## 五、异常返回日志要求

- **每一层的每个异常返回都必须打详细日志**（handler 层、service 层等所有业务代码层），日志必须携带足够上下文字段使仅凭单条日志即可定位问题。宁可冗余也不漏
- **日志可达性前置检查**：service struct 必须有 `logger` 字段；缺少 logger 注入的函数不可能满足"每个 error return 都有日志"的要求

### 日志等级匹配规则

- **Error 级别**：调用外部服务失败、DB 写入失败、数据不一致等不可恢复错误
- **Warn 级别**：记录不存在、业务条件不满足、幂等跳过等可预期异常
- **无需日志**：error 向上透传且调用方已记录日志的场景
