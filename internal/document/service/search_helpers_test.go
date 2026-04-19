// search_helpers_test.go 纯函数级测试:per-doc cap、confidence gate、similarity gate。
// 这些 helper 是 SearchChunks 装配步的组件,独立测不依赖 DB / Reranker / Embedder。
package service

import (
	"testing"

	"github.com/eyrihe999-stack/Synapse/internal/document/model"
	"github.com/eyrihe999-stack/Synapse/internal/document/repository"
)

func chunk(docID uint64, idx int) *model.DocumentChunk {
	return &model.DocumentChunk{DocID: docID, ChunkIdx: idx}
}

func hit(docID uint64, idx int, distance float32) repository.ChunkHit {
	return repository.ChunkHit{Chunk: chunk(docID, idx), Distance: distance}
}

func hitWithRerank(docID uint64, idx int, distance float32, score float32) repository.ChunkHit {
	s := score
	return repository.ChunkHit{Chunk: chunk(docID, idx), Distance: distance, RerankScore: &s}
}

func TestApplyPerDocCap_KeepsInputOrder(t *testing.T) {
	hits := []repository.ChunkHit{
		hit(1, 0, 0.1),
		hit(1, 1, 0.15),
		hit(2, 0, 0.2),
		hit(1, 2, 0.25),
		hit(3, 0, 0.3),
	}
	out := applyPerDocCap(hits, 2)
	// doc1 前两条保留,第三条被过滤(idx=2 的那条);doc2/doc3 原样。
	if len(out) != 4 {
		t.Fatalf("len = %d, want 4", len(out))
	}
	want := []struct {
		doc uint64
		idx int
	}{{1, 0}, {1, 1}, {2, 0}, {3, 0}}
	for i, w := range want {
		if out[i].Chunk.DocID != w.doc || out[i].Chunk.ChunkIdx != w.idx {
			t.Errorf("out[%d] = (doc=%d, idx=%d), want (doc=%d, idx=%d)",
				i, out[i].Chunk.DocID, out[i].Chunk.ChunkIdx, w.doc, w.idx)
		}
	}
}

func TestApplyPerDocCap_CapOfOne(t *testing.T) {
	hits := []repository.ChunkHit{
		hit(1, 0, 0.1),
		hit(1, 1, 0.15),
		hit(1, 2, 0.2),
		hit(2, 0, 0.25),
	}
	out := applyPerDocCap(hits, 1)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2 (one per doc)", len(out))
	}
	if out[0].Chunk.DocID != 1 || out[1].Chunk.DocID != 2 {
		t.Errorf("got docs %d,%d, want 1,2", out[0].Chunk.DocID, out[1].Chunk.DocID)
	}
}

func TestApplyPerDocCap_SkipsNilChunks(t *testing.T) {
	hits := []repository.ChunkHit{
		{Chunk: nil, Distance: 0.05},
		hit(1, 0, 0.1),
		{Chunk: nil, Distance: 0.12},
		hit(1, 1, 0.15),
	}
	out := applyPerDocCap(hits, 2)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2 (nil chunks skipped, 2 of doc=1 kept)", len(out))
	}
	for _, h := range out {
		if h.Chunk == nil {
			t.Error("nil chunk leaked into output")
		}
	}
}

func TestFilterByRerankScore_TruncatesAtFirstBelow(t *testing.T) {
	hits := []repository.ChunkHit{
		hitWithRerank(1, 0, 0.1, 5.0),
		hitWithRerank(2, 0, 0.2, 2.0),
		hitWithRerank(3, 0, 0.3, 0.5), // 低于阈值 1.0 → 此处截断
		hitWithRerank(4, 0, 0.4, 3.0),
	}
	out := filterByRerankScore(hits, 1.0)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	if out[0].Chunk.DocID != 1 || out[1].Chunk.DocID != 2 {
		t.Errorf("got docs %d,%d, want 1,2", out[0].Chunk.DocID, out[1].Chunk.DocID)
	}
}

func TestFilterByRerankScore_NilScoreTreatedAsBelow(t *testing.T) {
	hits := []repository.ChunkHit{
		hitWithRerank(1, 0, 0.1, 5.0),
		hit(2, 0, 0.2), // RerankScore = nil → 被过滤(防御,不该走到这)
		hitWithRerank(3, 0, 0.3, 4.0),
	}
	out := filterByRerankScore(hits, 1.0)
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1 (nil score treated as fail)", len(out))
	}
	if out[0].Chunk.DocID != 1 {
		t.Errorf("got doc %d, want 1", out[0].Chunk.DocID)
	}
}

func TestFilterByRerankScore_AllPass(t *testing.T) {
	hits := []repository.ChunkHit{
		hitWithRerank(1, 0, 0.1, 5.0),
		hitWithRerank(2, 0, 0.2, 2.0),
	}
	out := filterByRerankScore(hits, 1.0)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
}

func TestFilterByRerankScore_NonePass(t *testing.T) {
	hits := []repository.ChunkHit{
		hitWithRerank(1, 0, 0.1, 0.5),
		hitWithRerank(2, 0, 0.2, 0.2),
	}
	out := filterByRerankScore(hits, 1.0)
	if len(out) != 0 {
		t.Fatalf("len = %d, want 0", len(out))
	}
}

func TestFilterBySimilarity_TruncatesAtFirstBelow(t *testing.T) {
	// similarity = 1 - distance/2
	// distance 0.2 → similarity 0.9;distance 1.0 → similarity 0.5;distance 1.6 → similarity 0.2
	hits := []repository.ChunkHit{
		hit(1, 0, 0.2), // sim 0.9
		hit(2, 0, 1.0), // sim 0.5
		hit(3, 0, 1.6), // sim 0.2 → 低于阈值 0.4 → 截断
		hit(4, 0, 1.2), // sim 0.4(未达到也被截,输入保证升序)
	}
	out := filterBySimilarity(hits, 0.4)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	if out[0].Chunk.DocID != 1 || out[1].Chunk.DocID != 2 {
		t.Errorf("got docs %d,%d, want 1,2", out[0].Chunk.DocID, out[1].Chunk.DocID)
	}
}

func TestFilterBySimilarity_AllPass(t *testing.T) {
	hits := []repository.ChunkHit{
		hit(1, 0, 0.1), // sim 0.95
		hit(2, 0, 0.2), // sim 0.9
	}
	out := filterBySimilarity(hits, 0.3)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
}
