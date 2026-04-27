package llm

import "fmt"

// ModelPrice 单个模型的 input/output token 价格(美元 / 1K tokens)。
//
// 写死在代码里、不做动态价目表 —— 模型升级/切换本来就是跨 PR 的事,
// 价格表和 prompt、tool schema 一起随版本走,review 时一起看。
type ModelPrice struct {
	InputPer1K  float64
	OutputPer1K float64
}

// modelPriceTable 已知模型价目表。key 是 llm 包落库用的 modelTag("{deployment}@{provider}")。
//
// gpt-5.4 价格依据:内部 Azure 部署价(2026 Q2)。换模型时**同步更新**此表。
var modelPriceTable = map[string]ModelPrice{
	"gpt-5.4@azure": {
		InputPer1K:  0.005, // $5 / 1M input tokens
		OutputPer1K: 0.015, // $15 / 1M output tokens
	},
}

// EstimateCostUSD 基于 modelTag 查表 + usage 算本次调用的美元成本。
//
// modelTag 未知 → 返回 0 + 错误;调用方(orchestrator handler)按 error 写 audit
// 警告但不中断流程(避免因为价表漏登就拒绝响应用户)。
func EstimateCostUSD(modelTag string, usage Usage) (float64, error) {
	p, ok := modelPriceTable[modelTag]
	if !ok {
		return 0, fmt.Errorf("llm: unknown model price for %q, add to modelPriceTable", modelTag)
	}
	cost := float64(usage.PromptTokens)/1000.0*p.InputPer1K +
		float64(usage.CompletionTokens)/1000.0*p.OutputPer1K
	return cost, nil
}
