---
name: merge-main
description: 同步 upstream/main 并将当前功能分支合并到 main，推送到 origin
disable-model-invocation: true
allowed-tools: Bash(git *)
---

ultrathink

将当前功能分支集成到 main 分支并推送到 origin。

## 步骤 1：前置检查

1. 记录当前分支名 `{feature_branch}`。
2. 如果当前分支就是 `main`，提示"请切换到功能分支后再执行"并结束。
3. 检查工作区是否干净（`git status --porcelain`）。如果有未提交的改动，提示用户先提交或 stash，并结束。

## 步骤 2：同步 main 与 upstream

1. `git fetch upstream`
2. `git checkout main`
3. `git merge upstream/main`
   - 如果有冲突，提示用户手动解决后再执行，并切回 `{feature_branch}`，结束。

## 步骤 3：合并功能分支到 main

1. `git merge {feature_branch}`
   - 如果有冲突，提示用户手动解决，列出冲突文件，结束（保持在 main 分支让用户处理）。
2. 合并成功后展示 `git log --oneline -5` 确认合并结果。

## 步骤 4：安全检查

1. **大文件检查**：用 `git diff --name-only upstream/main..main` 获取新增/修改的文件列表，检查每个文件大小。超过 **1MB** 的文件拒绝推送，提示用户处理。
2. 检查不通过则停止，不推送。

## 步骤 5：推送

1. `git push origin main`
2. 推送成功后输出结果。
3. 推送失败时：
   - non-fast-forward → 提示用户检查 origin/main 状态，**不自动 force push**。
   - 其他错误直接展示。
4. 切回 `{feature_branch}`。
