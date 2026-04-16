---
name: push
description: 提交当前改动并推送到 origin 对应分支
disable-model-invocation: true
allowed-tools: Read, Grep, Glob, Bash(git *)
---

ultrathink

将当前工作区的改动提交并推送到 origin 的对应分支。

## 步骤 1：检查状态

并行执行：
- `git status`
- `git diff --stat` + `git diff --cached --stat`
- `git log --oneline -5`
- `git branch --show-current`

如果没有任何改动（无 untracked、无 modified、无 staged），提示用户"没有需要推送的改动"并结束。

## 步骤 2：安全检查

1. **大文件检查**：对所有待提交文件（modified + untracked），用 `git ls-files -o --exclude-standard` 和 `git diff --name-only` 获取文件列表，然后检查每个文件大小。超过 **1MB** 的文件**拒绝提交**，列出文件名和大小，提示用户处理（加入 .gitignore 或确认是否真的需要提交）。
2. **敏感文件检查**：排除可能包含敏感信息的文件（`.env`、`credentials.*`、`*.pem`、`*.key`、`*.p12`、`*.pfx`）。如果发现敏感文件，警告用户并跳过这些文件。

任一检查不通过则停止，不进入暂存和提交。

## 步骤 3：暂存与提交

1. 展示所有改动文件列表。
2. 用 `git diff`（含 staged 和 unstaged）分析改动内容。
3. 根据改动内容生成简洁的 commit message（1-2 句，聚焦 why 而非 what），遵循仓库已有的 commit 风格。
4. 暂存相关文件（用具体文件名，不用 `git add -A`）。
5. 提交，message 末尾附加：
   ```
   Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
   ```

## 步骤 4：推送

1. 获取当前分支名。
2. 执行 `git push -u origin {branch}`。
3. 推送成功后输出分支名和远程 URL。
4. 推送失败时：
   - 如果是远程有新提交（non-fast-forward），提示用户先 pull 或 rebase，**不自动执行 force push**。
   - 其他错误直接展示给用户。
