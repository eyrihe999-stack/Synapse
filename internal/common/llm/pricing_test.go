package llm

import (
	"math"
	"testing"
)

func TestEstimateCostUSD_Gpt54(t *testing.T) {
	got, err := EstimateCostUSD("gpt-5.4@azure", Usage{
		PromptTokens:     1000,
		CompletionTokens: 500,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 1000/1000 * 0.005 + 500/1000 * 0.015 = 0.005 + 0.0075 = 0.0125
	want := 0.0125
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("cost = %v, want %v", got, want)
	}
}

func TestEstimateCostUSD_UnknownModel(t *testing.T) {
	got, err := EstimateCostUSD("unknown-model@azure", Usage{PromptTokens: 100})
	if err == nil {
		t.Fatal("expected error for unknown model")
	}
	if got != 0 {
		t.Errorf("cost for unknown model = %v, want 0", got)
	}
}

func TestEstimateCostUSD_ZeroUsage(t *testing.T) {
	got, err := EstimateCostUSD("gpt-5.4@azure", Usage{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 0 {
		t.Errorf("zero-usage cost = %v, want 0", got)
	}
}
