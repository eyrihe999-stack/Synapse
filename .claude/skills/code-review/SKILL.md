---
name: code-review
description: 运行 lint 审计并自动修复发现的问题
argument-hint: "<module-name>"
allowed-tools: Read, Write, Edit, Grep, Glob, Agent, Bash(go *), Bash(mkdir *)
---

ultrathink

## 输入

- `/code-review organization` — 审计 organization 模块并自动修复

**必须指定模块名**。未指定时立即结束并提示。

---

## 步骤 1：运行 sayso-lint 获取当前问题

```bash
mkdir -p .claude/tmp/{module}
go run ./tools/sayso-lint {module} > .claude/tmp/{module}/{module}-lint.json 2>.claude/tmp/{module}/{module}-lint-summary.txt
```

读取 stderr 摘要。如果 **0 个问题**，输出"无需修复"并结束。

---

## 步骤 2：分类问题并制定修复策略

读取 JSON，按规则分类：

| 规则 | 修复方式 |
|------|---------|
| err-swallow | 读取代码确认：真吞错误 → 修复；`_, err :=` 误报 → `//sayso-lint:ignore err-swallow` |
| err-shadow | 读取代码确认 WithTx 回调 → `//sayso-lint:ignore err-shadow` |
| sentinel-wrap | 透传已含 sentinel 的 error → ignore；缺少 sentinel → 添加 `fmt.Errorf("...: %w: %w", err, ErrXxx)` |
| log-coverage | 有 logger → 添加 `s.logger.ErrorCtx(...)` 或 `log.ErrorCtx(...)`；无 logger → 先加字段再加日志 |
| file-naming | handler 包文件名缺 handler/router → `git mv` 重命名 |
| handler-router-split | 将 RegisterRoutes 拆到 router.go |
| file-size | 按职责拆分文件 |
| func-size | 提取子函数 |
| deep-nesting | 提取辅助函数或提前返回 |
| interface-pollution | 记录为设计决策，不修复 |
| 其他 | 按规则语义修复 |

**不修复的规则**（输出但跳过）：interface-pollution、file-size（略超标）

---

## 步骤 3：执行修复

按以下优先级修复（高 → 低），每类修复可并行启动 Agent：

1. **结构性修改**（添加 logger 字段、重命名文件）— 先做，影响后续修复
2. **err-swallow / err-shadow** — 批量加 ignore 或修复
3. **log-coverage** — 批量添加日志
4. **sentinel-wrap** — 批量加 ignore 或包装
5. **file-naming / handler-router-split** — 文件重命名和拆分
6. **deep-nesting / func-size** — 重构

每个修复完成后立即 `go build ./internal/{module}/...` 验证编译。

---

## 步骤 4：验证

```bash
go vet ./internal/{module}/...
go build ./internal/{module}/...
go run ./tools/sayso-lint {module} 2>&1 1>/dev/null
```

输出修复前后对比：

```markdown
## {module} 代码审查完成

| 指标 | 修复前 | 修复后 |
|------|--------|--------|
| 总问题数 | {N} | {M} |
| 被 ignore 抑制 | {A} | {B} |

### 修复明细
（按规则分组列出修复了什么）

### 剩余项（设计决策，不修复）
（interface-pollution 等）

### 下一步
如仍有剩余问题，可再次运行 `/code-review {module}` 继续修复。
```

---

## 注意事项

- 修复前必须读取对应源码，不要盲改
- `//sayso-lint:ignore` 格式严格为 `//sayso-lint:ignore rule-name`，规则名后不要加任何文字
- 添加 logger 字段时需同步更新构造函数和所有测试文件中的构造调用
- 不自动 commit
- 用中文写报告
