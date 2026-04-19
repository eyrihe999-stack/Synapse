package reranker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newMockTEI 起一个假 TEI 服务,按 handler 返 JSON。签验请求 body 格式正确。
func newMockTEI(t *testing.T, handler func(req teiRerankRequest) ([]teiRerankItem, int)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rerank" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "bad content-type", http.StatusBadRequest)
			return
		}
		var req teiRerankRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		items, status := handler(req)
		w.WriteHeader(status)
		if status == http.StatusOK {
			_ = json.NewEncoder(w).Encode(items)
		}
	}))
}

func TestBGETEI_HappyPath(t *testing.T) {
	// mock 端按 docs 长度排序:短的分高 —— 只是一个确定性规则,验证 client 能把响应正确映射回 Index。
	srv := newMockTEI(t, func(req teiRerankRequest) ([]teiRerankItem, int) {
		if req.Query != "stripe" {
			return nil, http.StatusBadRequest
		}
		if !req.RawScores {
			return nil, http.StatusBadRequest
		}
		// 输入 3 条 doc,按 len 升序 → index=2 (最短) 第一, 0 第二, 1 最后
		return []teiRerankItem{
			{Index: 2, Score: 3.0},
			{Index: 0, Score: 1.5},
			{Index: 1, Score: 0.2},
		}, http.StatusOK
	})
	defer srv.Close()

	rr, err := NewBGETEI(BGEConfig{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewBGETEI: %v", err)
	}

	out, err := rr.Rerank(context.Background(), "stripe", []string{
		"medium content about stripe payment",
		"very long content with lots of details about stripe integration and related stuff",
		"short stripe",
	})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("got %d results, want 3", len(out))
	}
	// 第一个 Index=2,分最高。
	if out[0].Index != 2 || out[0].Score != 3.0 {
		t.Errorf("top hit = {Index=%d,Score=%f}, want {2, 3.0}", out[0].Index, out[0].Score)
	}
}

func TestBGETEI_EmptyDocs(t *testing.T) {
	// 空 docs 直接返 nil,不发网络请求 —— mock 没起服务也过。
	rr, err := NewBGETEI(BGEConfig{BaseURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("NewBGETEI: %v", err)
	}
	out, err := rr.Rerank(context.Background(), "anything", nil)
	if err != nil || out != nil {
		t.Errorf("empty docs → want (nil, nil), got (%v, %v)", out, err)
	}
}

func TestBGETEI_ServerError(t *testing.T) {
	// 5xx 响应应当返包含 status 的 error,不 panic。
	srv := newMockTEI(t, func(req teiRerankRequest) ([]teiRerankItem, int) {
		return nil, http.StatusInternalServerError
	})
	defer srv.Close()

	rr, err := NewBGETEI(BGEConfig{BaseURL: srv.URL, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewBGETEI: %v", err)
	}
	_, err = rr.Rerank(context.Background(), "q", []string{"d1", "d2"})
	if err == nil {
		t.Fatal("expected error on 5xx, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("err should mention status 500, got: %v", err)
	}
}

func TestBGETEI_RequiresBaseURL(t *testing.T) {
	_, err := NewBGETEI(BGEConfig{})
	if err == nil {
		t.Errorf("empty BaseURL should error")
	}
}

func TestBGETEI_Name(t *testing.T) {
	rr, _ := NewBGETEI(BGEConfig{BaseURL: "http://x"})
	if rr.Name() != "bge_tei" {
		t.Errorf("default Name() = %q, want bge_tei", rr.Name())
	}
	rr, _ = NewBGETEI(BGEConfig{BaseURL: "http://x", Name: "bge_prod"})
	if rr.Name() != "bge_prod" {
		t.Errorf("override Name() = %q, want bge_prod", rr.Name())
	}
}
