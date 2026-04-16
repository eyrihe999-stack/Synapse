---
name: test-doc-review
description: 基于确定性测试数据生成或更新模块测试总结文档
argument-hint: "<module-name>"
allowed-tools: Read, Write, Edit, Grep, Glob, Bash(go *), Bash(mkdir *)
---

ultrathink

## 输入

- `/test-doc-review organization` — 生成或更新 organization 模块测试总结文档

**必须指定模块名**。未指定时立即结束并提示。

---

## 步骤 1：运行 sayso-lint test-doc 提取测试数据

```bash
mkdir -p .claude/tmp/{module}

# 1a. 运行测试确认当前状态
go test -race -timeout 120s \
  ./internal/{module}/service/... \
  ./internal/{module}/handler/... \
  -count=1 2>&1 | tee .claude/tmp/{module}/{module}-test-output.txt

# 1b. 提取确定性测试文档数据
go run ./tools/sayso-lint test-doc {module} \
  > .claude/tmp/{module}/{module}-testdoc.json \
  2>.claude/tmp/{module}/{module}-testdoc-summary.txt
```

读取 JSON。输出包含以下确定性数据：

| 字段 | 内容 |
|------|------|
| stats | 文件数、Test 函数数、子测试数、总行数、场景覆盖统计 |
| files | 每个测试文件的统计（路径、层级、函数数、子测试数、行数、被测对象） |
| scenarios | 完整场景矩阵（目标函数、场景名、类型、sentinel、是否已覆盖） |
| sentinel_coverage | 每个 sentinel 是否被 errors.Is 断言覆盖 |
| call_chain_coverage | 每个 repo 调用点的 error 路径是否被测试 |
| mocks | mock 清单（名称、接口、方法数、文件） |

---

## 步骤 2：判断新建/更新

检查 `docs/modules/{module}/test-summary.md` 是否存在：

- **不存在** → 步骤 3（新建）
- **存在** → 步骤 4（更新）

---

## 步骤 3：新建文档

从 JSON 直接渲染 `docs/modules/{module}/test-summary.md`，按以下模板：

```markdown
# {module} 测试总结

## 概览

| 指标 | 值 |
|------|-----|
| 测试文件数 | {stats.file_count} |
| 顶层 Test 函数数 | {stats.test_functions} |
| 子测试用例数 | {stats.sub_tests} |
| 总行数 | {stats.total_lines} |
| 场景覆盖率 | {stats.covered}/{stats.total}（{pct}%） |

## 测试文件清单

| 文件 | 层级 | Test 函数数 | 子测试数 | 行数 | 被测对象 |
|------|------|-----------|---------|------|---------|
（从 files 数组渲染）

## Service 层测试场景矩阵

| 目标函数 | 场景 | 类型 | Sentinel | 覆盖 |
|---------|------|------|----------|------|
（从 scenarios 数组渲染，按 layer=service 过滤）

## Handler 层测试场景矩阵

（同上格式）
（从 scenarios 数组渲染，按 layer=handler 过滤）

场景类型：happy path / error path / validation / 幂等 / 降级 / 业务规则

## Sentinel 覆盖情况

| Sentinel | 覆盖 | 测试函数 |
|----------|------|---------|
（从 sentinel_coverage 数组渲染）

### 调用链覆盖

| 方法 | 调用点 | error 路径已测 | 测试名 |
|------|--------|--------------|--------|
（从 call_chain_coverage 数组渲染）

## 未覆盖项及原因

（从 scenarios 中 covered=false 的项 + sentinel_coverage 中 covered=false 的项汇总，按重要性排序）

## Mock 清单

| Mock 名称 | 接口 | 方法数 | 文件 |
|-----------|------|--------|------|
（从 mocks 数组渲染）
```

写入后跳到步骤 5。

---

## 步骤 4：更新文档

读取现有文档。

**逐章节对比 JSON 数据与文档内容**：

| 章节 | 对比数据 |
|------|---------|
| 概览 | stats |
| 测试文件清单 | files |
| 测试场景矩阵 | scenarios |
| Sentinel 覆盖 | sentinel_coverage |
| 调用链覆盖 | call_chain_coverage |
| 未覆盖项 | 基于覆盖度重新计算 |
| Mock 清单 | mocks |

**输出变更检测报告**：

```markdown
## {module} 测试文档变更检测

| 章节 | 状态 | 变更摘要 |
|------|------|---------|
| 概览 | / | Test 函数数 20->23 |
| 测试文件清单 | / | 新增 xxx_test.go |
| 场景矩阵 | / | 新增 8 个子测试 |
| ... | | |
```

如果**所有章节均为最新**，输出"测试文档已是最新"并结束。

否则**仅更新有变化的章节**，保留未变化章节。更新后写回文件。

---

## 步骤 5：验证

1. 文档中不包含 JSON 原始数据
2. 指标数据与 JSON 一致
3. 场景矩阵完整（每个被测函数的每个场景都有一行）
4. 用中文写报告

---

## 注意事项

- **所有数据从 JSON 渲染，不手动读测试文件、不猜测**
- 场景矩阵直接从 scenarios 数组转表格
- Sentinel 覆盖直接从 sentinel_coverage 数组转表格
- 不自动 commit
- 用中文写文档
