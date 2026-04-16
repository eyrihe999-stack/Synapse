---
name: code-doc-review
description: 基于 AST 提取生成或更新模块架构文档
argument-hint: "<module-name>"
allowed-tools: Read, Write, Edit, Grep, Glob, Bash(go *), Bash(mkdir *)
---

ultrathink

## 输入

- `/code-doc-review organization` — 生成或更新 organization 模块架构文档

**必须指定模块名**。未指定时立即结束并提示。

---

## 步骤 1：运行 sayso-lint 提取结构化数据

```bash
mkdir -p .claude/tmp/{module}

# 1a. 提取模块文档数据
go run ./tools/sayso-lint doc {module} > .claude/tmp/{module}/{module}-doc.json 2>.claude/tmp/{module}/{module}-doc-summary.txt

# 1b. 提取时区审计数据
go run ./tools/sayso-lint time-audit {module} > .claude/tmp/{module}/{module}-time-audit.json 2>.claude/tmp/{module}/{module}-time-audit-summary.txt
```

读取 stderr 摘要和 JSON。

**doc JSON** 包含以下确定性数据：

| 字段 | 内容 |
|------|------|
| routes | 路由表（方法、路径、handler） |
| error_codes | 错误码常量 |
| sentinels | 哨兵错误变量 |
| models | 数据模型（表名、字段、tag） |
| dtos | 请求/响应 DTO |
| services | Service 类型、依赖、方法签名、调用链 |
| repositories | Repository 接口、方法签名 |
| transactions | 事务调用位置 |
| locks | 加锁位置 |
| external_callers | 外部模块对本模块的调用（含 error_handling） |
| caller_sources | 调用方模块的完整源码 |

**time-audit JSON** 包含以下确定性数据：

| 字段 | 内容 |
|------|------|
| stats | time.Now().UTC() / 裸 time.Now() / time.Since 等各类操作的计数 |
| entries | 每一处时间操作的文件、行号、函数、类型（kind）、表达式、上下文行 |

---

## 步骤 2：判断新建/更新

检查 `docs/modules/{module}/architecture.md` 是否存在：

- **不存在** → 步骤 3（新建）
- **存在** → 步骤 4（更新）

---

## 步骤 3：新建文档

读取补充源文件：
1. `internal/{module}/errors.go`
2. `internal/{module}/const.go`
3. `internal/{module}/handler/*router*.go`
4. `internal/{module}/handler/*error_map*.go`
5. 核心 service 文件（按 JSON 中 services 的 File 字段）

将 JSON 数据 + 源码理解整合写入 `docs/modules/{module}/architecture.md`，按以下模板：

```markdown
# {module} 模块架构

## 概览

（模块职责、核心实体、与其他模块的关系。从 external_callers 列出依赖本模块的模块。）

## 数据模型

（从 models 渲染表格：字段名、类型、约束、说明。标注索引和唯一约束。）

## API 接口

按路由分组（用户侧 / Admin 侧）。

**用户侧接口**必须给出完整的请求和响应 payload 示例（从 DTO 字段推导 JSON，字段值用合理示例数据）：

- 方法 + 路径
- 请求 payload（JSON 示例）
- 响应 payload（JSON 示例）
- 可能的错误码

Admin 侧接口：方法+路径、请求参数、可能的错误码（无需完整 payload 示例）

## 错误码

（从 error_codes + sentinels 渲染表格：Code / Sentinel / HTTP 状态码 / 含义）

## 业务场景

**必须覆盖所有入口（API + 外部模块调用）**。从 routes + services.calls + external_callers 推导。

每个场景包含：
- 触发方式（API / 外部模块调用）
- 完整调用链（handler → service → repo 层级）
- 事务边界（从 transactions 提取）
- 加锁信息（从 locks 提取）
- 可能的错误及处理（从 sentinel + error_map 提取）
- 调用方如何处理错误（从 external_callers.error_handling 读取）

## 时区一致性

（从 time-audit JSON 渲染。）

### 统计概览

| 分类 | 数量 |
|------|------|
| time.Now().UTC() | {stats.time_now_utc} |
| 裸 time.Now() | {stats.time_now_bare} |
| time.Now().In(...) | {stats.time_now_in} |
| time.Since/Until | {stats.time_since_until} |
| 时间比较 | {stats.time_compare} |
| 时间计算 | {stats.time_calc} |
| time.Date | {stats.time_date} |
| time.Parse* | {stats.time_parse} |
| time.LoadLocation/FixedZone | {stats.time_location} |

### 详细清单

（从 entries 数组按 kind 分组渲染表格：文件、行号、函数、表达式、上下文行）

### 问题项

（裸 time.Now() 或 time.Now().In() 的项，分析风险并给出修复建议。全部为 UTC 则写"全部 time.Now() 调用均使用 .UTC()，时区使用一致"。）

## 数据一致性与并发安全

（从 transactions + locks 合并分析，包含事务边界、乐观锁/悲观锁使用、竞态风险点。）
```

写入后跳到步骤 5。

---

## 步骤 4：更新文档

读取现有 `architecture.md` 和补充源文件（同步骤 3）。

**逐章节对比 JSON 数据与文档内容**：

| 章节 | 对比数据 |
|------|---------|
| API 接口 | routes、dtos |
| 数据模型 | models |
| 错误码 | error_codes、sentinels |
| 业务场景 | services.methods.calls、external_callers |
| 数据一致性/并发安全 | transactions、locks |
| 时区一致性 | time-audit JSON 的 stats 和 entries |
| 概览 | external_callers 的模块列表 |

**输出变更检测报告**：

```markdown
## {module} 架构文档变更检测

| 章节 | 状态 | 变更摘要 |
|------|------|---------|
| 概览 | / | ... |
| 数据模型 | / | ... |
| API 接口 | / | ... |
| ... | | |
```

如果**所有章节均为最新**，输出"架构文档已是最新"并结束。

否则**仅更新有变化的章节**，保留未变化章节的原有内容。更新后写回文件。

---

## 步骤 5：验证

1. 文档中不包含 JSON 原始数据
2. 更新的章节与 JSON 数据一致
3. 所有 API 接口都有对应的业务场景
4. 所有 external_callers 都在业务场景中体现
5. 用中文写报告

---

## 注意事项

- JSON 数据是确定性的，直接使用不要猜测
- external_callers 是生成完整业务场景的必要数据，不能跳过
- 读取外部调用者源码时只读相关函数
- 不自动 commit
- 用中文写文档
