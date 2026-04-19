package tokenizer

import (
	"strings"
	"testing"
)

func TestGse_EmptyAndWhitespace(t *testing.T) {
	tk, err := NewGse()
	if err != nil {
		t.Fatalf("NewGse: %v", err)
	}
	if tk.Tokenize("") != nil {
		t.Errorf("empty → want nil")
	}
	if tk.Tokenize("   \n\t ") != nil {
		t.Errorf("whitespace → want nil")
	}
	if tk.TokensString("") != "" {
		t.Errorf("empty TokensString → want empty")
	}
}

func TestGse_ChineseSegments(t *testing.T) {
	tk, err := NewGse()
	if err != nil {
		t.Fatalf("NewGse: %v", err)
	}
	// "支付模块架构" 应该切成多词(至少 2 个),而不是一整坨。
	// 具体边界由 gse 内置词典决定,这里只验证"有切",不校对每个 token。
	got := tk.Tokenize("支付模块架构")
	if len(got) < 2 {
		t.Errorf("expected multi-token split, got %d tokens: %v", len(got), got)
	}
	joined := tk.TokensString("支付模块架构")
	if !strings.Contains(joined, " ") {
		t.Errorf("TokensString should contain spaces between tokens: %q", joined)
	}
}

func TestGse_EnglishStaysWhole(t *testing.T) {
	tk, err := NewGse()
	if err != nil {
		t.Fatalf("NewGse: %v", err)
	}
	// 英文标识符(camelCase / underscore)应作为**整体**一个 token,不按大小写边界拆。
	// gse 默认会 lowercase,所以 "V2Feedback" → "v2feedback";和 PG 'simple' 配置对称 ——
	// 查询端同样 lowercase,双边都是 "v2feedback" 所以 match。
	got := tk.Tokenize("V2Feedback 表")
	joined := strings.Join(got, "|")
	if !strings.Contains(strings.ToLower(joined), "v2feedback") {
		t.Errorf("expected V2Feedback-as-token (case-insensitive): %v", got)
	}
}

func TestGse_MixedTextPreservesPaths(t *testing.T) {
	tk, err := NewGse()
	if err != nil {
		t.Fatalf("NewGse: %v", err)
	}
	// 文件路径 / URL 片段这种 "/" 分隔的应当被 gse 认为是整体或按合理边界切分,
	// 关键是"api" 和 "feedbacks" 这些子串不丢(BM25 match 需要)。
	got := tk.TokensString("/api/v2/feedbacks")
	if !strings.Contains(got, "api") {
		t.Errorf("path lost 'api' token: %q", got)
	}
	if !strings.Contains(got, "feedbacks") {
		t.Errorf("path lost 'feedbacks' token: %q", got)
	}
}
