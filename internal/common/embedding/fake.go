package embedding

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math"
	"math/rand/v2"
)

// fakeModelName 写入 document_chunks.embedding_model,便于后续区分真/假向量(批量清理 dev 数据用)。
const fakeModelName = "fake-deterministic"

// fake 是离线确定性 embedder:同一输入永远得到同一向量,支持任意维度。
//
// 流程:sha256(input) 前 16 字节做成 PCG seed → math/rand/v2 采高斯 → L2 单位化。
// 单位化后 cosine 与 dot product 等价,适配上层 HNSW cosine 索引(空间分布接近真实 embedding)。
type fake struct {
	dim int
}

func newFake(dim int) *fake {
	return &fake{dim: dim}
}

func (f *fake) Dim() int      { return f.dim }
func (f *fake) Model() string { return fakeModelName }

func (f *fake) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([][]float32, len(inputs))
	for i, s := range inputs {
		out[i] = f.vectorFor(s)
	}
	return out, nil
}

// vectorFor 从字符串派生一个单位化的伪随机向量。
//
// 使用 PCG(math/rand/v2)而不是过时的 math/rand:Go 1.22+ 的 PCG 周期更长,
// Source 不需要显式 NewSource;此处用两段 uint64 作为种子,足以保证不同输入向量近似独立。
func (f *fake) vectorFor(s string) []float32 {
	h := sha256.Sum256([]byte(s))
	seed1 := binary.BigEndian.Uint64(h[0:8])
	seed2 := binary.BigEndian.Uint64(h[8:16])
	rng := rand.New(rand.NewPCG(seed1, seed2))

	v := make([]float32, f.dim)
	var sumSq float64
	for j := range v {
		x := float32(rng.NormFloat64())
		v[j] = x
		sumSq += float64(x) * float64(x)
	}
	if sumSq == 0 {
		// 几乎不可能(正态不会全 0),兜底返回归一的第一维。
		v[0] = 1
		return v
	}
	inv := float32(1.0 / math.Sqrt(sumSq))
	for j := range v {
		v[j] *= inv
	}
	return v
}
