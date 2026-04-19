// Package reranker 交叉编码器(cross-encoder)打分服务的统一接口。
//
// 在检索 pipeline 里的位置:
//   retrieve(vector / bm25 / hybrid) → 候选池 (top M)
//                  ↓
//              Reranker              ← 本包
//                  ↓
//            截取 top K 返回给调用方
//
// 为什么要 rerank:双塔 embedding 模型独立编码 query 和 doc,在向量空间算距离 —— 快,但信号
// 丢失。cross-encoder 把 (query, doc) 对一起喂给 transformer,用 full attention 算相关性,
// 精度远高于余弦。代价是每对都要一次模型调用,所以只用在"少量候选的重排"阶段,不当召回用。
//
// 典型组合(T1.1 → T1.2):
//   topK = 10,candidateK = topK * 5 = 50
//   retrieve 拿 50 条 → rerank 重排 → 截 10 条给 LLM
//
// 本接口故意不绑定具体模型 —— 实现可以是 BGE-reranker / Cohere API / Voyage 等。
package reranker

import "context"

// Reranker 交叉编码器接口。无状态、并发安全(实现层负责连接池 / rate limit)。
type Reranker interface {
	// Rerank 按相关性从高到低重排 docs。
	//
	// 输入:query 是用户查询原文,docs 是候选文档文本(通常是 chunk content)。
	// 输出:和 docs 等长的 Result 列表,按 score 降序排列;Index 指回 docs 原位置,
	//       方便调用方拿到排序后的对应业务实体(如 ChunkHit)。
	//
	// 空 docs 返 nil, nil。空 query 行为由实现定义,建议返 docs 原顺序(score=0)。
	// 超时 / 网络错误返 (nil, err),调用方自行决定 fallback(如继续用原顺序)。
	Rerank(ctx context.Context, query string, docs []string) ([]Result, error)

	// Name 实现标识(诊断日志 / metrics tag 用),如 "bge_tei" / "cohere"。
	Name() string
}

// Result 单个文档的 rerank 分数与原始下标。
type Result struct {
	// Index 在 Rerank 输入 docs 切片中的位置。用来回映射到业务对象(如 ChunkHit)。
	Index int
	// Score 相关性分数,越大越相关。具体范围由实现定(BGE 通常 [-10, 10],Cohere 返 [0, 1]),
	// 上层不该假设绝对值含义,只用相对顺序。需要阈值过滤时按分位数而非硬值。
	Score float32
}
