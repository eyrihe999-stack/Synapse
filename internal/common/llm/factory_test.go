package llm

import (
	"errors"
	"testing"
)

// TestNew_RejectsFakeProvider 强制"所有环境一致":不允许 fake。
func TestNew_RejectsFakeProvider(t *testing.T) {
	_, err := New(Config{Provider: "fake"})
	if err == nil {
		t.Fatal("expected error for fake provider, got nil")
	}
}

// TestNew_RejectsEmptyProvider 强制显式 provider(不默默兜底)。
func TestNew_RejectsEmptyProvider(t *testing.T) {
	_, err := New(Config{Provider: ""})
	if err == nil {
		t.Fatal("expected error for empty provider, got nil")
	}
}

// TestNew_AzureRequiresAllFields factory 层校验 azure 必填字段完整。
func TestNew_AzureRequiresAllFields(t *testing.T) {
	tests := []struct {
		name string
		cfg  AzureConfig
	}{
		{"missing endpoint", AzureConfig{Deployment: "d", APIKey: "k"}},
		{"missing deployment", AzureConfig{Endpoint: "e", APIKey: "k"}},
		{"missing api_key", AzureConfig{Endpoint: "e", Deployment: "d"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(Config{Provider: ProviderAzure, Azure: tc.cfg})
			if err == nil {
				t.Fatalf("%s: expected error, got nil", tc.name)
			}
			if !errors.Is(err, ErrLLMInvalid) {
				t.Errorf("%s: expected ErrLLMInvalid, got %v", tc.name, err)
			}
		})
	}
}

// TestNew_AzureOK 全字段齐备时能构造出来。
func TestNew_AzureOK(t *testing.T) {
	c, err := New(Config{
		Provider: ProviderAzure,
		Azure: AzureConfig{
			Endpoint:   "https://x.openai.azure.com/openai/v1/",
			Deployment: "gpt-5.4",
			APIKey:     "k",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Model() != "gpt-5.4@azure" {
		t.Errorf("Model() = %q, want gpt-5.4@azure", c.Model())
	}
}
