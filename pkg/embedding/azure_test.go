package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newAzureHarness 起一个本地 httptest.Server 模拟 Azure 端点,h 里自己决定返回什么。
// 返回已构造好的 azure 实例,其 url 指向 httptest —— 不打真实网络,CI 安全。
func newAzureHarness(t *testing.T, dim int, h http.HandlerFunc) *azure {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	// 用 v1 形态 endpoint(含 /openai/v1),这样 buildAzureURL 会生成 "{base}/embeddings",
	// 和 srv.URL 直接拼对齐,便于单测预期路径。
	a, err := newAzure(AzureConfig{
		Endpoint:   srv.URL + "/openai/v1/",
		Deployment: "text-embedding-3-large",
		APIKey:     "test-key",
		APIVersion: "2024-10-21",
	}, dim)
	if err != nil {
		t.Fatalf("newAzure: %v", err)
	}
	return a
}

func TestAzure_Embed_Success(t *testing.T) {
	a := newAzureHarness(t, 3, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/v1/embeddings" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Header.Get("api-key") != "test-key" {
			t.Errorf("api-key header missing/wrong")
		}
		var body azureEmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode req: %v", err)
		}
		if body.Dimensions != 3 {
			t.Errorf("dimensions = %d, want 3", body.Dimensions)
		}
		if body.Model != "text-embedding-3-large" {
			t.Errorf("model = %q, want deployment name", body.Model)
		}
		// 故意乱序 index,验证客户端会重排。
		_ = json.NewEncoder(w).Encode(azureEmbedResponse{
			Data: []struct {
				Index     int       `json:"index"`
				Embedding []float32 `json:"embedding"`
			}{
				{Index: 1, Embedding: []float32{0.4, 0.5, 0.6}},
				{Index: 0, Embedding: []float32{0.1, 0.2, 0.3}},
			},
			Model: "text-embedding-3-large",
		})
	})

	got, err := a.Embed(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if got[0][0] != 0.1 || got[1][0] != 0.4 {
		t.Errorf("reorder by index broken: got[0]=%v got[1]=%v", got[0], got[1])
	}
}

func TestAzure_Embed_EmptyInput(t *testing.T) {
	// 空输入不应打网络 —— 如果打了,handler 会 panic。
	a := newAzureHarness(t, 3, func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called for empty input")
	})
	got, err := a.Embed(context.Background(), nil)
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty result, got %v", got)
	}
}

func TestAzure_Embed_DimMismatch(t *testing.T) {
	a := newAzureHarness(t, 3, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(azureEmbedResponse{
			Data: []struct {
				Index     int       `json:"index"`
				Embedding []float32 `json:"embedding"`
			}{{Index: 0, Embedding: []float32{0.1, 0.2}}}, // dim 2,期望 3
		})
	})
	_, err := a.Embed(context.Background(), []string{"x"})
	if !errors.Is(err, ErrEmbeddingDimMismatch) {
		t.Errorf("expected ErrEmbeddingDimMismatch, got %v", err)
	}
}

func TestAzure_Embed_ErrorClassification(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		want       error
	}{
		{"401 -> auth", http.StatusUnauthorized, ErrEmbeddingAuth},
		{"403 -> auth", http.StatusForbidden, ErrEmbeddingAuth},
		{"429 -> rate-limit", http.StatusTooManyRequests, ErrEmbeddingRateLimited},
		{"400 -> invalid", http.StatusBadRequest, ErrEmbeddingInvalid},
		{"422 -> invalid", http.StatusUnprocessableEntity, ErrEmbeddingInvalid},
		{"500 -> server", http.StatusInternalServerError, ErrEmbeddingServer},
		{"503 -> server", http.StatusServiceUnavailable, ErrEmbeddingServer},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newAzureHarness(t, 3, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(`{"error":{"message":"boom","code":"x","type":"y"}}`))
			})
			_, err := a.Embed(context.Background(), []string{"x"})
			if !errors.Is(err, tt.want) {
				t.Errorf("got err %v, want wrapping %v", err, tt.want)
			}
		})
	}
}

func TestAzure_Embed_NetworkError(t *testing.T) {
	a, err := newAzure(AzureConfig{
		Endpoint:   "http://127.0.0.1:1/openai/v1/", // 保证无法连接
		Deployment: "d",
		APIKey:     "k",
	}, 3)
	if err != nil {
		t.Fatalf("newAzure: %v", err)
	}
	_, err = a.Embed(context.Background(), []string{"x"})
	if !errors.Is(err, ErrEmbeddingNetwork) {
		t.Errorf("expected ErrEmbeddingNetwork, got %v", err)
	}
}

func TestBuildAzureURL_V1Surface(t *testing.T) {
	got := buildAzureURL(AzureConfig{
		Endpoint:   "https://x.openai.azure.com/openai/v1/",
		Deployment: "dep",
		APIVersion: "2024-10-21",
	})
	want := "https://x.openai.azure.com/openai/v1/embeddings"
	if got != want {
		t.Errorf("v1: got %q, want %q", got, want)
	}
}

func TestBuildAzureURL_LegacySurface(t *testing.T) {
	got := buildAzureURL(AzureConfig{
		Endpoint:   "https://x.openai.azure.com/",
		Deployment: "dep",
		APIVersion: "2024-10-21",
	})
	if !strings.Contains(got, "/openai/deployments/dep/embeddings") {
		t.Errorf("legacy path missing: %q", got)
	}
	if !strings.Contains(got, "api-version=2024-10-21") {
		t.Errorf("legacy api-version missing: %q", got)
	}
}

func TestNewAzure_MissingFields(t *testing.T) {
	tests := []AzureConfig{
		{Deployment: "d", APIKey: "k"},               // no endpoint
		{Endpoint: "https://x/openai/v1/", APIKey: "k"},  // no deployment
		{Endpoint: "https://x/openai/v1/", Deployment: "d"}, // no key
	}
	for i, cfg := range tests {
		if _, err := newAzure(cfg, 3); !errors.Is(err, ErrEmbeddingInvalid) {
			t.Errorf("case %d: expected ErrEmbeddingInvalid, got %v", i, err)
		}
	}
}
