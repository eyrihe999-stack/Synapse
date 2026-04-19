package sourceadapter

import (
	"context"
	"testing"
	"time"
)

type stubAdapter struct {
	typ string
}

func (s stubAdapter) Type() string { return s.typ }
func (s stubAdapter) Sync(context.Context, uint64, time.Time) ([]Change, error) {
	return nil, nil
}
func (s stubAdapter) Fetch(context.Context, uint64, SourceRef) (*RawDocument, error) {
	return nil, nil
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	a := stubAdapter{typ: "git_file"}
	if err := r.Register(a); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, ok := r.Get("git_file")
	if !ok || got.Type() != "git_file" {
		t.Errorf("Get(git_file) → %v, ok=%v", got, ok)
	}
	if _, ok := r.Get("jira_issue"); ok {
		t.Errorf("Get(jira_issue) should miss")
	}
}

func TestRegistry_DuplicateRejected(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(stubAdapter{typ: "git_file"}); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := r.Register(stubAdapter{typ: "git_file"}); err == nil {
		t.Fatal("duplicate Register should error")
	}
}

func TestRegistry_NilAndEmptyRejected(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(nil); err == nil {
		t.Error("nil adapter should error")
	}
	if err := r.Register(stubAdapter{typ: ""}); err == nil {
		t.Error("empty Type() should error")
	}
}

func TestRegistry_Types(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(stubAdapter{typ: "git_file"})
	_ = r.Register(stubAdapter{typ: "jira_issue"})
	got := r.Types()
	if len(got) != 2 {
		t.Fatalf("Types() len = %d, want 2", len(got))
	}
	// order 无序,转 set 比较。
	set := map[string]bool{got[0]: true, got[1]: true}
	if !set["git_file"] || !set["jira_issue"] {
		t.Errorf("Types() missing entries: %v", got)
	}
}
