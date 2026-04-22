package embedding

import (
	"context"
	"math"
	"testing"
)

func TestFake_Deterministic(t *testing.T) {
	e := newFake(8)
	ctx := context.Background()

	a, err := e.Embed(ctx, []string{"hello world"})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	b, err := e.Embed(ctx, []string{"hello world"})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	// 同输入必须完全相等,fake 的核心语义就是确定性。
	for i := range a[0] {
		if a[0][i] != b[0][i] {
			t.Fatalf("fake embedder non-deterministic at dim %d: %v vs %v", i, a[0][i], b[0][i])
		}
	}
}

func TestFake_UnitNormalized(t *testing.T) {
	e := newFake(64)
	got, err := e.Embed(context.Background(), []string{"synapse", "agent", ""})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	for i, v := range got {
		var sum float64
		for _, x := range v {
			sum += float64(x) * float64(x)
		}
		// 允许浮点精度误差;超过 1e-5 说明归一化有 bug。
		if math.Abs(math.Sqrt(sum)-1.0) > 1e-5 {
			t.Errorf("input %d: L2 norm = %v, want 1", i, math.Sqrt(sum))
		}
	}
}

func TestFake_DifferentInputsDifferentVectors(t *testing.T) {
	e := newFake(32)
	got, err := e.Embed(context.Background(), []string{"alpha", "beta"})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	// 不同输入应该给出不同向量;完全相等几乎不可能(哈希冲突概率 ~1/2^256)。
	same := true
	for i := range got[0] {
		if got[0][i] != got[1][i] {
			same = false
			break
		}
	}
	if same {
		t.Errorf("different inputs produced identical fake vectors")
	}
}

func TestFake_DimMatches(t *testing.T) {
	e := newFake(1536)
	got, _ := e.Embed(context.Background(), []string{"x"})
	if len(got[0]) != 1536 {
		t.Errorf("dim = %d, want 1536", len(got[0]))
	}
}

func TestFake_Cancellable(t *testing.T) {
	e := newFake(4)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := e.Embed(ctx, []string{"x"})
	if err == nil {
		t.Error("expected error from cancelled context, got nil")
	}
}
