---
name: test-review
description: 分析模块测试覆盖缺口并补充/重写测试用例（支持增量修复和全量重写两种模式）
argument-hint: "<module-name> [rewrite]"
allowed-tools: Read, Write, Edit, Grep, Glob, Agent, Bash(go *), Bash(mkdir *), Bash(cat *), Bash(find *), Bash(wc *)
---

ultrathink

**⚠️ 硬性前置条件**：
1. 执行前建议先 `/clear` 清空上下文。
2. 仅服务于 Nexus 模块（见 [shared/v2-modules.md](../shared/v2-modules.md)），目标不在列表中时立即结束并提示。

## 输入

- `/test-review organization` — 增量模式：分析 organization 模块覆盖缺口，补充缺失的测试
- `/test-review organization rewrite` — 重写模式：删除旧测试，从零编写全部单元测试

**必须指定模块名**。未指定时立即结束并提示。

---

## 模式判断

| 参数 | 模式 | 工具命令 | 行为 |
|------|------|---------|------|
| `{module}` | 增量（默认） | `sayso-lint test` | 保留已有测试，只补充 gaps |
| `{module} rewrite` | 重写 | `sayso-lint retest` | 删除所有旧测试后重写 |

重写模式会**物理删除**模块下所有 `_test.go` 文件，执行前确认用户理解这一点。

---

## 步骤 1：收集确定性数据

### 增量模式

```bash
mkdir -p .claude/tmp/{module}

# 1a. 运行测试确认当前状态
go test -race -timeout 120s \
  ./internal/{module}/service/... \
  ./internal/{module}/handler/... \
  -count=1 2>&1 | tee .claude/tmp/{module}/{module}-test-output.txt

# 1b. 运行确定性覆盖度分析（不删除文件）
go run ./tools/sayso-lint test {module} \
  > .claude/tmp/{module}/{module}-coverage.json \
  2>.claude/tmp/{module}/{module}-coverage-summary.txt
```

如果测试有失败，**先修复失败的测试再继续**。

### 重写模式

```bash
mkdir -p .claude/tmp/{module}

# 一条命令：删除旧测试 + 生成确定性 JSON（含 mock、调用链、场景 spec）
go run ./tools/sayso-lint retest {module} \
  > .claude/tmp/{module}/{module}-testplan.json \
  2>.claude/tmp/{module}/{module}-testplan-summary.txt
```

---

## 步骤 2：读取数据 + 源码

1. 读取摘要 `.txt` 文件
2. 读取 JSON 文件
3. 读取 [shared/code-limits.md](../shared/code-limits.md) 和 [shared/admin-policy.md](../shared/admin-policy.md)

**不需要读取** architecture.md 或 doc.json — 调用链和场景已在 JSON 中。

### JSON 格式

**增量模式**（`sayso-lint test` 输出）：

```json
{
  "module": "organization",
  "packages": [{
    "path": "internal/organization/service",
    "layer": "service",
    "mocks": [...],
    "files": [{
      "source_path": "...",
      "test_path": "...",
      "functions": [{
        "test_name": "TestCreateOrg",
        "target": "CreateOrg",
        "calls": [...],
        "gaps": [
          {"name": "find_org_error", "type": "error", "failing_call": "repo.FindOrgBySlug", "sentinel": "ErrInternal", "not_called_after": ["..."]}
        ],
        "covered": ["success", "slug_taken"]
      }]
    }]
  }],
  "summary": {
    "total_scenarios": 324,
    "covered_scenarios": 69,
    "missing_scenarios": 255
  }
}
```

**只有 `gaps` 非空的函数才需要补充测试**。`covered` 已有的场景不动。

**重写模式**（`sayso-lint retest` 输出）：

```json
{
  "packages": [{
    "path": "internal/{module}/service",
    "layer": "service",
    "mocks": [{ "mock_name": "mockRepository", "interface": "Repository", "methods": [...] }],
    "files": [{
      "test_path": "..._test.go",
      "source_path": "....go",
      "functions": [{
        "test_name": "TestCreateOrg",
        "target": "CreateOrg",
        "calls": [
          {"target": "repo.FindOrgBySlug", "kind": "repo"},
          {"target": "repo.CreateOrg", "kind": "repo"}
        ],
        "scenarios": [
          {"name": "success", "type": "success"},
          {"name": "find_org_error", "type": "error", "failing_call": "repo.FindOrgBySlug", "sentinel": "ErrInternal", "not_called_after": ["..."]}
        ]
      }]
    }]
  }]
}
```

场景由 AST 确定性推导：

| 场景类型 | 推导规则 |
|---------|---------|
| success | 每个函数固定 1 个 |
| error | 每个 `returned` 的 error 调用点 1 个 |
| not_found | 有 `gorm.ErrRecordNotFound` 分支时额外 1 个 |
| idempotent | 调用名含 `Idempotent` 时 1 个 |

---

## 步骤 3：展示计划并确认

**⚠️ 本步骤必须在 Plan 模式下运行**。

### 增量模式计划

```markdown
## 测试补充计划（{module}）

覆盖率: {covered}/{total}（{pct}%）

### 按文件分组的缺口

| # | 文件 | 函数 | 缺口数 | 缺口列表 |
|---|------|------|--------|---------|
| 1 | org_test.go | CreateOrg | 3 | find_org_error, ... |

### 缺口明细（按 sentinel 分组）

| sentinel | 缺口数 | 涉及函数 |
|----------|--------|---------|
| ErrInternal | 45 | CreateOrg, UpdateOrg, ... |

共需补充 {N} 个子测试。是否继续？
```

### 重写模式计划

```markdown
## 测试重写计划（{module}）

已删除旧测试文件: {N} 个

### 包计划

| # | 包路径 | 层级 | 测试文件数 | Mock 数 | 场景总数 |
|---|--------|------|-----------|---------|---------|
| 1 | internal/{module}/service | service | 6 | 3 | 85 |

### Service 层场景摘要（从 JSON scenarios 字段直接列出）

| 方法 | 调用链长度 | 场景数 | 场景类型分布 |
|------|-----------|--------|------------|
| CreateOrg | 7 | 11 | 1 success + 7 error + 3 idempotent |

是否按此计划生成测试？
```

用户确认后退出 Plan 模式。

---

## 步骤 4：生成 / 补充测试

每个 package 一个子 Agent，所有包**同时并行启动**。

### 子 Agent 需要的上下文

对每个包，注入以下内容到 prompt：

1. **JSON 中该包的数据**：mock 定义、函数签名、调用链、场景 spec / gaps
2. **被测源码**：该包下所有非测试 `.go` 文件的完整内容
3. **模式标识**：增量 or 重写

### 子 Agent prompt 模板

````
Agent(subagent_type="general-purpose", prompt="""
你是一个 Go 测试工程师。请按照 JSON 场景 spec 逐条编写测试。

## 当前模式

{增量 | 重写}

## 核心规则

**严格按 JSON 场景列表编写，不多不少。** 每个 scenario / gap 对应一个 t.Run 子测试。

## Mock 定义（来自 JSON mocks 字段）

```json
{该包的 Mocks JSON}
```

## 被测源码

```go
// {source_file_path}
{源码内容}
```

## 场景 spec（来自 JSON）

```json
{该包所有 functions 的 JSON，含 calls + scenarios/gaps}
```

## 编写规则

### 1. 按场景逐条编写

JSON 中每个 function 的 scenarios（重写）或 gaps（增量）数组精确列出了需要的测试场景。按顺序编写：

- **success**：所有调用返回成功，断言主要返回值
- **error**：`failing_call` 返回 error，断言 `errors.Is(err, sentinel)`
  - `not_called_after` 列出的方法不应被调用
- **not_found**：`failing_call` 返回 `gorm.ErrRecordNotFound`，断言对应 sentinel
- **idempotent**：`failing_call` 返回 `rowsAffected=0`，断言无 error

### 2. 增量模式特殊规则

- **不删除已有测试**，只追加
- 追加到已有 Test 函数的末尾
- 如果 `test_path` 对应的文件不存在或没有对应的 Test 函数，可新建

### 3. 重写模式特殊规则

- 先创建 `mock_test.go`：按 JSON mocks 编写所有 mock struct（func 字段模式）
- 再创建 `helpers_test.go`：编写共享的 factory 函数
- 最后按源文件逐个创建测试文件

### 4. 结构化注释

每个 t.Run 必须有注释：
```go
t.Run("find_org_error — FindOrgBySlug 失败", func(t *testing.T) {
    // 场景：查询组织时 DB 出错
    // 前置条件：FindOrgBySlug 返回 error
    // 预期结果：返回 error，errors.Is(err, ErrInternal) == true
    // 后续调用不触发：CreateOrg, CreateMember, ...
})
```

### 5. 测试模式
- **service 层**：白盒测试（`package service`），mock 所有 repo 依赖
- **handler 层**：黑盒测试（`package handler_test`），mock service 接口，用 httptest 驱动

### 6. 命名
- 测试函数名使用 JSON 中的 `test_name`
- t.Run 名使用 JSON 中的 scenario `name`（可追加中文描述）

### 7. 断言
- 成功路径：断言返回值关键字段
- 错误路径：`errors.Is(err, {module}.ErrXxx)` 断言 JSON 中的 sentinel
- 验证提前返回：确认 `not_called_after` 中的 mock 方法未被调用

### 8. 规模约束
- 单文件不超过 400 行 / 20 个顶层 Test 函数
- 超标时按被测函数拆分

### 9. Admin 测试宽松策略
Admin handler/service 只需覆盖：happy path + 参数校验 + not found + duplicate key + 非法状态转换。

### 完成后
1. `go vet ./{pkg_dir}/...`
2. `go test -v -race -timeout 120s ./{pkg_dir}/... -count=1`
3. 编译错误或测试失败时修复后重试
4. 输出文件列表和每个文件的测试函数数

用中文写注释。
""")
````

---

## 步骤 5：汇总与全量回归

所有子 Agent 返回后：

1. **全量回归**：
   ```bash
   go vet ./internal/{module}/...
   go test -race -timeout 120s ./internal/{module}/handler/... ./internal/{module}/service/... -count=1
   ```

2. **回归失败处理**：定位失败的测试，修复后重试

3. **测试注释质量检查**：
   ```bash
   go run ./tools/sayso-lint --tests-only {module} 2>/dev/null
   ```
   如果有 `test-comment` 问题，逐个修复：
   - **缺少结构化注释**：补充三行格式注释（场景描述 / 前置条件 / 预期结果）
   - **缺少关键词**：在已有注释中补充「前置条件」和「预期结果」
   
   修复后重新运行检查，确认 `test-comment` 问题归零。

4. **输出变更摘要**：

### 增量模式摘要

重新运行覆盖度分析，对比前后：

```bash
go run ./tools/sayso-lint test {module} 2>&1 1>/dev/null
```

```markdown
## 测试补充完成（{module}）

| 指标 | 补充前 | 补充后 |
|------|--------|--------|
| 总场景 | 324 | 324 |
| 已覆盖 | 69 | {新值} |
| 覆盖率 | 21% | {新值}% |

### 新增子测试
（列出每个新增的 t.Run 名称 + 所在文件）

### 剩余未覆盖
（如有，说明原因：admin 低优先级跳过 / 无法 mock 等）
```

### 重写模式摘要

```markdown
## 测试重写完成（{module}）

| # | 包 | 文件 | Test 函数数 | 行数 | 状态 |
|---|----|------|-----------|------|------|
| 1 | service | org_test.go | 8 | 320 | PASS |

总计: {N} 个测试文件, {M} 个测试函数, 全部通过
```

---

## 注意事项

- **增量模式不删除已有测试**，只追加；**重写模式会物理删除所有旧测试**
- **场景清单由工具决定，LLM 只负责写代码**
- LLM 的唯一自由度：mock 数据的具体值和注释措辞
- repo 层不写测试（service 测试用 mock，禁止 SQLite）
- Admin 方法的 error 路径为低优先级，用户可选择跳过
- 不自动 commit
- 用中文写报告和注释
