package embedding

import (
	"context"
	"math"
	"os"
	"strconv"
	"testing"
	"time"
)

// TestAzure_LiveEmbed 打真实 Azure 端点,验证 deployment + api_key 可用、返回维度正确。
//
// 默认跳过,设 AZURE_EMBEDDING_LIVE=1 + 以下变量后运行:
//
//	AZURE_EMBEDDING_ENDPOINT   (必填)
//	AZURE_EMBEDDING_DEPLOYMENT (必填)
//	AZURE_EMBEDDING_API_KEY    (必填)
//	AZURE_EMBEDDING_DIM        (可选,默认 1536)
//
// 故意不走 config.Load 避免 CI 误触网络;CI 里没有该环境变量,一律 skip。
func TestAzure_LiveEmbed(t *testing.T) {
	if os.Getenv("AZURE_EMBEDDING_LIVE") != "1" {
		t.Skip("set AZURE_EMBEDDING_LIVE=1 (and AZURE_EMBEDDING_{ENDPOINT,DEPLOYMENT,API_KEY}) to run")
	}
	endpoint := os.Getenv("AZURE_EMBEDDING_ENDPOINT")
	deployment := os.Getenv("AZURE_EMBEDDING_DEPLOYMENT")
	apiKey := os.Getenv("AZURE_EMBEDDING_API_KEY")
	dim := 1536
	if v := os.Getenv("AZURE_EMBEDDING_DIM"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("parse AZURE_EMBEDDING_DIM: %v", err)
		}
		dim = n
	}

	e, err := newAzure(AzureConfig{
		Endpoint:   endpoint,
		Deployment: deployment,
		APIKey:     apiKey,
		APIVersion: "2024-10-21",
	}, dim)
	if err != nil {
		t.Fatalf("newAzure: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	inputs := []string{"hello world", "synapse knowledge system"}
	vecs, err := e.Embed(ctx, inputs)
	if err != nil {
		t.Fatalf("live embed failed: %v", err)
	}
	if len(vecs) != len(inputs) {
		t.Fatalf("got %d vectors, want %d", len(vecs), len(inputs))
	}
	for i, v := range vecs {
		if len(v) != dim {
			t.Fatalf("vec %d dim=%d, want %d", i, len(v), dim)
		}
		var sum float64
		for _, x := range v {
			sum += float64(x) * float64(x)
		}
		// OpenAI 系 embedding 返回近似单位向量,norm 应 ≈ 1(允许小误差)。
		if math.Abs(math.Sqrt(sum)-1.0) > 0.01 {
			t.Errorf("vec %d L2 norm = %v, expected ~1", i, math.Sqrt(sum))
		}
	}
	t.Logf("live embed OK: %d vectors, dim=%d, first5=%v", len(vecs), dim, vecs[0][:5])
}
