// Package prompts 顶级系统 agent 的 system prompt(硬编码 + go:embed)。
//
// 为什么不直接在 runtime 里 go:embed ../prompts/...:go:embed 不允许指令模式
// 包含父目录路径(`..`),所以在 prompts 包内部做 embed,再用 exported var 暴露。
package prompts

import _ "embed"

// TopOrchestrator 顶级系统 agent 的 system prompt 原文(markdown)。
//
// 路径 internal/agentsys/prompts/top_orchestrator.md。任何改动随 PR review,
// 不做运行时热加载(运维侧想改要走发版流程)。
//
//go:embed top_orchestrator.md
var TopOrchestrator string
