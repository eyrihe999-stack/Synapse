# 注释内容规范

> 语言政策见 [annotation-policy.md](annotation-policy.md)（必须中文）。本文件定义注释的**内容要求**。

---

## 一、源码注释规范

### 导出函数/方法（godoc 注释）

每个导出函数/方法前必须有**详细的** godoc 注释（缺失标记为 P2），内容包含：

1. **第一行**：函数名 + 功能概述
2. **业务语义**：做什么、为什么、在什么场景下被调用
3. **关键参数**：非自明参数的含义和约束
4. **返回值语义**：尤其是 error 的可能类型（哪些 sentinel error）
5. **事务/锁**（如有）：事务边界、加锁顺序、FOR UPDATE 范围
6. **副作用**（如有）：会修改哪些数据、是否发送通知
7. **best-effort 说明**（如有）：哪些操作失败不阻塞，为什么

示例：

```go
// CreateOrganization 创建新组织，同时将创建者设为 owner 成员。
//
// 在事务内执行：校验用户拥有组织数上限 → 创建组织记录 → 创建 owner 成员记录。
// 返回 ErrMaxOwnedOrgsReached 表示超限，ErrOrgInternal 表示 DB 异常。
```

### 事务函数专项注释

包含 `db.Transaction` / `tx.Begin` 的函数**必须**在头部注释中包含：
- 事务内操作步骤（按执行顺序列出）
- 加锁顺序（如适用）
- FOR UPDATE / FOR SHARE 的锁范围和目的
- 事务失败时的回滚影响

### 未导出函数/方法

超过 10 行的未导出函数应有注释，描述职责和关键逻辑。

### 类型/接口/常量

- 导出的类型和接口必须有注释
- `const (` / `var (` 块上方有统一注释时内部各项可省略
- sentinel error（`var ErrXxx = errors.New(...)`）每个都应有一行说明

### 不需要注释的

- 接口方法的实现（注释写在接口定义处即可）
- `TableName()` 等 GORM 约定方法
- 简单的 getter/setter
- `init()` 函数

---

## 二、测试注释规范

### 单元测试（handler/ 和 service/）

每个顶层 `func Test*` 前必须有**三行**结构化注释：

```go
// TestCreateOrg_MaxOwnedReached 测试创建组织时超过拥有数上限的处理。
// 前置条件：用户已拥有 max_owned_orgs 个组织。
// 预期结果：返回 ErrMaxOwnedOrgsReached。
```

1. **函数名 + 一句话场景描述**（动词开头：测试/验证）
2. **前置条件**：mock/setup 的关键配置
3. **预期结果**：核心断言的文字描述

### 不需要测试注释的

- 子测试（`t.Run`）— 不需要此格式
- `TestMain` — 职责通常自明
- benchmark 函数
- helpers/mock 函数 — 一行说明用途即可
