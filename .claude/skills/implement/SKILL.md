---
name: implement
description: 分析 PRD 并结合 Nexus 架构给出实现方案，经用户确认后执行
argument-hint: "<prd-file-path>"
allowed-tools: Read, Write, Edit, Grep, Glob, Agent, Bash(go *), Bash(mkdir *), Bash(git mv *), AskUserQuestion, EnterPlanMode, ExitPlanMode
---

ultrathink

## 输入

- `/implement docs/prd/some-feature.md` — 分析 PRD 并实现
- `/implement` （无参数）— 提示用户提供 PRD 文件路径或直接粘贴需求

**如果未提供参数**，用 AskUserQuestion 询问用户提供 PRD 文件路径或直接描述需求，然后继续。

---

## 步骤 1：读取 PRD

如果参数是文件路径，读取该文件。如果用户直接粘贴了需求文本，直接使用。

提取以下关键信息：
- 功能目标（做什么）
- 用户场景（谁用、怎么用）
- 数据需求（新表、新字段、新状态）
- API 需求（新接口、修改接口）
- 与现有功能的关系（依赖、互斥、扩展）

---

## 步骤 2：分析 Nexus 架构上下文

### 2a. 确定影响范围

读取以下文件建立架构全局视图：

1. `.claude/skills/shared/v2-modules.md` — 模块列表
2. `.claude/skills/shared/v2-module-structure.md` — 模块目录结构规范
3. `.claude/skills/shared/logging-policy.md` — 错误处理与日志规范

判断 PRD 涉及哪些模块：
- **主模块**：功能主要归属的模块（新建或已有）
- **受影响模块**：需要修改的其他模块（调用方/被调用方）

### 2b. 读取相关模块的源码

对每个涉及的模块：

1. 读取模块关键文件：
   - `internal/{module}/errors.go` — 已有的 sentinel errors
   - `internal/{module}/const.go` — 已有的常量
   - `internal/{module}/model/models.go` — 已有的数据模型
   - `internal/{module}/dto/dto.go` — 已有的 DTO
   - `internal/{module}/handler/router.go` — 已有的路由
   - `internal/{module}/repository/*.go` — 已有的 repo 方法
   - `internal/{module}/service/*.go` — 已有的 service 接口和实现

2. 如果是新建模块，读取标杆模块（organization）的结构作为参考

### 2c. 检查跨模块依赖

用 Grep 搜索 PRD 中提及的关键实体/接口，确认：
- 是否已有相关的模型/接口可以复用
- 是否会产生新的跨模块依赖
- 是否影响现有的调用链

---

## 步骤 3：进入 Plan 模式，制定实现方案

调用 EnterPlanMode，在 plan 文件中写出方案。

方案结构：

```markdown
# {功能名} 实现方案

## PRD 摘要
（一句话概括需求）

## 影响分析

| 模块 | 角色 | 变更类型 |
|------|------|---------|
| {module} | 主模块 | 新建 / 大改 |
| {module2} | 调用方 | 小改（新增调用） |

## 数据模型变更

### 新增表
（表名、字段、索引、与现有表的关系）

### 修改表
（新增字段、字段类型变更）

## API 设计

### 新增接口
| 方法 | 路径 | 描述 | 请求体 | 响应体 |
|------|------|------|--------|--------|

### 修改接口
（变更说明）

## 实现步骤

按依赖顺序排列：

### 第 1 步：数据模型层
### 第 2 步：错误码与常量
### 第 3 步：Repository 层
### 第 4 步：Service 层
### 第 5 步：DTO 层
### 第 6 步：Handler 层
### 第 7 步：跨模块集成
### 第 8 步：测试

## 设计决策

| 决策点 | 选项 A | 选项 B | 推荐 | 原因 |
|--------|--------|--------|------|------|

## 风险与注意事项
```

方案中如有需要用户决策的设计选项，用 AskUserQuestion 询问后再写入 plan。

调用 ExitPlanMode 等待用户审批。

---

## 步骤 4：执行实现

用户批准后，按 plan 中的步骤顺序执行：

### 执行原则

1. **严格遵循 Nexus 模块结构规范**（见 shared/v2-module-structure.md）
2. **错误处理遵循 logging-policy.md**：sentinel error 包装 + 日志覆盖
3. **每完成一个步骤，立即编译验证**：
   ```bash
   go build ./internal/{module}/...
   ```
4. **可并行的步骤用 Agent 并行执行**
5. **每个步骤完成后向用户报告进度**

### 执行顺序

按 plan 中定义的步骤顺序执行。如果某步骤失败：
- 分析原因
- 如果是小问题（编译错误、import 缺失），直接修复
- 如果需要调整方案，向用户说明并获得确认

---

## 步骤 5：验证

全部实现完成后：

```bash
go vet ./internal/{module}/...
go build ./internal/{module}/...
```

对每个受影响的模块都执行验证。

输出实现总结：

```markdown
## {功能名} 实现完成

### 变更统计
| 模块 | 新增文件 | 修改文件 | 新增代码行 |
|------|---------|---------|-----------|

### 变更明细
（按模块分组列出具体改了什么）

### 下一步
- 需要手动测试的场景
- 需要补充的测试用例
```

---

## 注意事项

- 实现前必须读取对应源码，不要盲改
- 新建模块必须严格遵循 v2-module-structure.md 的目录结构
- 跨模块调用必须通过 service 接口，禁止直接访问其他模块的 repository
- 所有 sentinel error 必须定义在模块根目录的 errors.go
- 所有常量必须定义在模块根目录的 const.go
- handler 必须在 handler/ 子包中
- 不自动 commit
- 用中文写报告和方案
