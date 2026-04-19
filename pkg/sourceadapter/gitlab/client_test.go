package gitlab

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetCurrentUser_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/user" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("PRIVATE-TOKEN"); got != "glpat-xxx" {
			t.Errorf("PRIVATE-TOKEN header = %q, want glpat-xxx", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": 42,
			"username": "alice",
			"name": "Alice A.",
			"email": "alice@example.com",
			"avatar_url": "https://gitlab.example/avatar.png",
			"web_url": "https://gitlab.example/alice",
			"state": "active"
		}`))
	}))
	defer srv.Close()

	c, err := NewClient(Config{BaseURL: srv.URL + "/api/v4"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	u, err := c.GetCurrentUser(context.Background(), "glpat-xxx")
	if err != nil {
		t.Fatalf("GetCurrentUser: %v", err)
	}
	if u.ID != 42 || u.Username != "alice" || u.Email != "alice@example.com" {
		t.Errorf("unexpected user: %+v", u)
	}
}

func TestGetCurrentUser_InvalidToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"401 Unauthorized"}`))
	}))
	defer srv.Close()

	c, err := NewClient(Config{BaseURL: srv.URL + "/api/v4"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = c.GetCurrentUser(context.Background(), "bogus")
	if !errors.Is(err, ErrInvalidToken) {
		t.Errorf("expected ErrInvalidToken, got %v", err)
	}
}

func TestGetCurrentUser_EmptyTokenShortCircuits(t *testing.T) {
	c, err := NewClient(Config{BaseURL: "https://gitlab.com/api/v4"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = c.GetCurrentUser(context.Background(), "")
	if !errors.Is(err, ErrInvalidToken) {
		t.Errorf("empty token should yield ErrInvalidToken, got %v", err)
	}
}

func TestGetCurrentUser_5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"internal"}`))
	}))
	defer srv.Close()

	c, err := NewClient(Config{BaseURL: srv.URL + "/api/v4"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = c.GetCurrentUser(context.Background(), "glpat-xxx")
	if err == nil {
		t.Fatal("expected error on 500, got nil")
	}
	if errors.Is(err, ErrInvalidToken) {
		t.Errorf("500 should not be ErrInvalidToken, got %v", err)
	}
}

func TestListProjects_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/projects" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		q := r.URL.Query()
		// 校验关键查询参数:membership=true 圈定 PAT 可见范围,archived=false 排掉归档
		if q.Get("membership") != "true" || q.Get("archived") != "false" {
			t.Errorf("expected membership=true&archived=false, got %s", r.URL.RawQuery)
		}
		if q.Get("per_page") != "50" || q.Get("page") != "1" {
			t.Errorf("expected per_page=50&page=1, got %s", r.URL.RawQuery)
		}
		w.Header().Set("X-Next-Page", "2") // 模拟还有下一页
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"id": 1, "name": "repo-a", "path_with_namespace": "team/repo-a", "default_branch": "main", "web_url": "https://gitlab.example/team/repo-a", "archived": false},
			{"id": 2, "name": "repo-b", "path_with_namespace": "team/repo-b", "default_branch": "master", "web_url": "https://gitlab.example/team/repo-b", "archived": false}
		]`))
	}))
	defer srv.Close()

	c, _ := NewClient(Config{BaseURL: srv.URL + "/api/v4"})
	page, err := c.ListProjects(context.Background(), "glpat-xxx", 1, 50)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(page.Projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(page.Projects))
	}
	if page.Projects[0].ID != 1 || page.Projects[0].DefaultBranch != "main" {
		t.Errorf("project[0] unexpected: %+v", page.Projects[0])
	}
	if page.NextPage != 2 {
		t.Errorf("NextPage = %d, want 2", page.NextPage)
	}
}

func TestListProjects_LastPage(t *testing.T) {
	// 最后一页:GitLab 把 X-Next-Page 设空,parseNextPage 要返 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Next-Page", "")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c, _ := NewClient(Config{BaseURL: srv.URL + "/api/v4"})
	page, err := c.ListProjects(context.Background(), "glpat-xxx", 99, 50)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if page.NextPage != 0 {
		t.Errorf("NextPage = %d, want 0 (last page)", page.NextPage)
	}
}

func TestListTree_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/projects/42/repository/tree" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("ref") != "main" || q.Get("recursive") != "true" {
			t.Errorf("expected ref=main&recursive=true, got %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"id": "blob1", "name": "README.md", "type": "blob", "path": "README.md", "mode": "100644"},
			{"id": "tree1", "name": "src", "type": "tree", "path": "src", "mode": "040000"}
		]`))
	}))
	defer srv.Close()

	c, _ := NewClient(Config{BaseURL: srv.URL + "/api/v4"})
	page, err := c.ListTree(context.Background(), "glpat-xxx", 42, "main", "", 1, 100)
	if err != nil {
		t.Fatalf("ListTree: %v", err)
	}
	if len(page.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(page.Entries))
	}
	if page.Entries[0].Type != "blob" || page.Entries[0].ID != "blob1" {
		t.Errorf("entry[0] unexpected: %+v", page.Entries[0])
	}
}

func TestListTree_ProjectNotFound(t *testing.T) {
	// 404 要转成 ErrProjectNotFound,不是底层 errNotFound —— adapter 按此 skip project
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c, _ := NewClient(Config{BaseURL: srv.URL + "/api/v4"})
	_, err := c.ListTree(context.Background(), "glpat-xxx", 99, "main", "", 1, 100)
	if !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("expected ErrProjectNotFound, got %v", err)
	}
}

func TestGetRawFile_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// path 里的 / 必须被 URL encode 成 %2F,否则 GitLab 会 404
		// httptest 收到的 r.URL.Path 是已经解码的,但 r.URL.RawPath 保留原始 encoded 形式
		if !strings.Contains(r.URL.RawPath, "src%2Fmain.go") && !strings.Contains(r.RequestURI, "src%2Fmain.go") {
			t.Errorf("expected path to contain encoded 'src%%2Fmain.go', got RawPath=%q RequestURI=%q",
				r.URL.RawPath, r.RequestURI)
		}
		w.Header().Set("X-Gitlab-Blob-Id", "abc123blob")
		w.Header().Set("X-Gitlab-Last-Commit-Id", "def456commit")
		w.Header().Set("X-Gitlab-Size", "1024")
		w.Header().Set("X-Gitlab-Ref", "main")
		_, _ = w.Write([]byte("package main\n\nfunc main() {}\n"))
	}))
	defer srv.Close()

	c, _ := NewClient(Config{BaseURL: srv.URL + "/api/v4"})
	fc, err := c.GetRawFile(context.Background(), "glpat-xxx", 42, "main", "src/main.go")
	if err != nil {
		t.Fatalf("GetRawFile: %v", err)
	}
	if string(fc.Content) != "package main\n\nfunc main() {}\n" {
		t.Errorf("Content = %q", string(fc.Content))
	}
	if fc.BlobSHA != "abc123blob" || fc.CommitID != "def456commit" || fc.Size != 1024 {
		t.Errorf("headers parsed wrong: %+v", fc)
	}
}

func TestGetRawFile_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c, _ := NewClient(Config{BaseURL: srv.URL + "/api/v4"})
	_, err := c.GetRawFile(context.Background(), "glpat-xxx", 42, "main", "missing.txt")
	if !errors.Is(err, ErrFileNotFound) {
		t.Errorf("expected ErrFileNotFound, got %v", err)
	}
}

func TestParseNextPage(t *testing.T) {
	tests := []struct {
		header string
		want   int
	}{
		{"", 0},     // 最后一页
		{"0", 0},    // 老版本兼容
		{"3", 3},    // 正常
		{"abc", 0},  // 非法值兜底 0
		{"-1", 0},   // 负值兜底 0
	}
	for _, tc := range tests {
		h := http.Header{}
		h.Set("X-Next-Page", tc.header)
		if got := parseNextPage(h); got != tc.want {
			t.Errorf("parseNextPage(%q) = %d, want %d", tc.header, got, tc.want)
		}
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"empty base url", Config{}, true},
		{"missing api path", Config{BaseURL: "https://gitlab.com"}, true},
		{"valid", Config{BaseURL: "https://gitlab.com/api/v4"}, false},
		{"self-hosted with insecure", Config{BaseURL: "https://git.internal/api/v4", InsecureSkipVerify: true}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate: wantErr=%v, got err=%v", tc.wantErr, err)
			}
		})
	}
}
