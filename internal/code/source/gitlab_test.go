package source

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/eyrihe999-stack/Synapse/pkg/sourceadapter/gitlab"
)

// fakeClient in-memory 实现 gitlab.ClientAPI,按 pageCalls 注入多页响应。
// 不做复杂状态机,直接按 page 序号返对应 response。
type fakeClient struct {
	// listProjectsPages page (1-based) → response
	listProjectsPages map[int]*gitlab.ProjectsPage
	// listTreePages    (projectID, page) → response
	listTreePages map[string]*gitlab.TreePage
	// files           (projectID, ref, path) → FileContent
	files     map[string]*gitlab.FileContent
	// tokenError       非 nil 时所有调用直接返此错误
	tokenError error
	// notFoundProject  这个 ID 调 ListTree 返 ErrProjectNotFound
	notFoundProject int64
}

func (f *fakeClient) GetCurrentUser(ctx context.Context, token string) (*gitlab.CurrentUser, error) {
	return nil, errors.New("not used in these tests")
}

func (f *fakeClient) ListProjects(ctx context.Context, token string, page, perPage int) (*gitlab.ProjectsPage, error) {
	if f.tokenError != nil {
		return nil, f.tokenError
	}
	if p, ok := f.listProjectsPages[page]; ok {
		return p, nil
	}
	return &gitlab.ProjectsPage{NextPage: 0}, nil
}

func (f *fakeClient) ListTree(ctx context.Context, token string, projectID int64, ref, path string, page, perPage int) (*gitlab.TreePage, error) {
	if f.tokenError != nil {
		return nil, f.tokenError
	}
	if projectID == f.notFoundProject && f.notFoundProject != 0 {
		return nil, gitlab.ErrProjectNotFound
	}
	key := treeKey(projectID, page)
	if p, ok := f.listTreePages[key]; ok {
		return p, nil
	}
	return &gitlab.TreePage{NextPage: 0}, nil
}

func (f *fakeClient) GetRawFile(ctx context.Context, token string, projectID int64, ref, path string) (*gitlab.FileContent, error) {
	if f.tokenError != nil {
		return nil, f.tokenError
	}
	key := fileKey(projectID, ref, path)
	if fc, ok := f.files[key]; ok {
		return fc, nil
	}
	return nil, gitlab.ErrFileNotFound
}

func treeKey(projectID int64, page int) string {
	return string(rune(projectID)) + "/" + string(rune(page))
}
func fileKey(projectID int64, ref, path string) string {
	return string(rune(projectID)) + "/" + ref + "/" + path
}

// ─── ListRepos ──────────────────────────────────────────────────────────────

func TestGitLabSource_ListRepos_FiltersArchivedAndEmpty(t *testing.T) {
	fc := &fakeClient{
		listProjectsPages: map[int]*gitlab.ProjectsPage{
			1: {
				Projects: []gitlab.Project{
					{ID: 1, PathWithNamespace: "a/active", DefaultBranch: "main", WebURL: "u1", Archived: false},
					{ID: 2, PathWithNamespace: "a/archived", DefaultBranch: "main", WebURL: "u2", Archived: true},
					{ID: 3, PathWithNamespace: "a/empty", DefaultBranch: "", WebURL: "u3", Archived: false}, // 无 default_branch
					{ID: 4, PathWithNamespace: "a/active2", DefaultBranch: "master", WebURL: "u4", Archived: false},
				},
				NextPage: 0,
			},
		},
	}
	src := NewGitLabSource(fc, "glpat-xxx")
	repos, err := src.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("expected 2 repos (archived + empty filtered out), got %d: %+v", len(repos), repos)
	}
	if repos[0].ExternalProjectID != "1" || repos[1].ExternalProjectID != "4" {
		t.Errorf("expected IDs 1,4, got %s,%s", repos[0].ExternalProjectID, repos[1].ExternalProjectID)
	}
}

func TestGitLabSource_ListRepos_Paginated(t *testing.T) {
	fc := &fakeClient{
		listProjectsPages: map[int]*gitlab.ProjectsPage{
			1: {
				Projects: []gitlab.Project{{ID: 1, PathWithNamespace: "p/a", DefaultBranch: "main"}},
				NextPage: 2,
			},
			2: {
				Projects: []gitlab.Project{{ID: 2, PathWithNamespace: "p/b", DefaultBranch: "main"}},
				NextPage: 0, // 终止
			},
		},
	}
	src := NewGitLabSource(fc, "x")
	repos, err := src.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("expected 2 repos across pages, got %d", len(repos))
	}
}

func TestGitLabSource_ListRepos_InvalidToken(t *testing.T) {
	fc := &fakeClient{tokenError: gitlab.ErrInvalidToken}
	src := NewGitLabSource(fc, "bogus")
	_, err := src.ListRepos(context.Background())
	if !errors.Is(err, ErrInvalidToken) {
		t.Errorf("expected ErrInvalidToken, got %v", err)
	}
}

// ─── ListFiles ──────────────────────────────────────────────────────────────

func TestGitLabSource_ListFiles_AppliesFilter(t *testing.T) {
	fc := &fakeClient{
		listTreePages: map[string]*gitlab.TreePage{
			treeKey(42, 1): {
				Entries: []gitlab.TreeEntry{
					{ID: "blob-a", Type: "blob", Path: "src/main.go"},
					{ID: "tree-1", Type: "tree", Path: "src"},       // 被过滤(非 blob)
					{ID: "blob-b", Type: "blob", Path: "node_modules/foo.js"}, // 被过滤(目录黑名单)
					{ID: "blob-c", Type: "blob", Path: "go.sum"},              // 被过滤(basename 黑名单)
					{ID: "blob-d", Type: "blob", Path: "README.md"},
					{ID: "blob-e", Type: "blob", Path: "assets/logo.png"}, // 被过滤(扩展名黑名单)
					{ID: "", Type: "blob", Path: "weird.go"},              // 被过滤(空 blob sha)
				},
				NextPage: 0,
			},
		},
	}
	src := NewGitLabSource(fc, "x")
	entries, err := src.ListFiles(context.Background(), RepoSnapshot{
		ExternalProjectID: "42", DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	// 期望保留:src/main.go + README.md
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries after filter, got %d: %+v", len(entries), entries)
	}
	paths := entries[0].Path + "," + entries[1].Path
	if !strings.Contains(paths, "src/main.go") || !strings.Contains(paths, "README.md") {
		t.Errorf("unexpected entries: %s", paths)
	}
}

func TestGitLabSource_ListFiles_ProjectNotFound(t *testing.T) {
	fc := &fakeClient{notFoundProject: 999}
	src := NewGitLabSource(fc, "x")
	_, err := src.ListFiles(context.Background(), RepoSnapshot{
		ExternalProjectID: "999", DefaultBranch: "main",
	})
	if !errors.Is(err, ErrRepoUnavailable) {
		t.Errorf("expected ErrRepoUnavailable, got %v", err)
	}
}

// ─── FetchFile ──────────────────────────────────────────────────────────────

func TestGitLabSource_FetchFile_Happy(t *testing.T) {
	fc := &fakeClient{
		files: map[string]*gitlab.FileContent{
			fileKey(42, "main", "foo.go"): {
				Content:  []byte("package foo\n"),
				BlobSHA:  "sha-from-header",
				CommitID: "commit-abc",
			},
		},
	}
	src := NewGitLabSource(fc, "x")
	got, err := src.FetchFile(context.Background(),
		RepoSnapshot{ExternalProjectID: "42", DefaultBranch: "main"},
		FileEntry{Path: "foo.go", BlobSHA: "sha-from-tree"},
	)
	if err != nil {
		t.Fatalf("FetchFile: %v", err)
	}
	if string(got.Content) != "package foo\n" {
		t.Errorf("Content = %q", got.Content)
	}
	// header 优先于 tree 阶段的 sha
	if got.BlobSHA != "sha-from-header" {
		t.Errorf("BlobSHA = %q, want sha-from-header", got.BlobSHA)
	}
	if got.LastCommitID != "commit-abc" {
		t.Errorf("LastCommitID = %q", got.LastCommitID)
	}
}

func TestGitLabSource_FetchFile_HeaderShaEmpty_FallbackToTree(t *testing.T) {
	fc := &fakeClient{
		files: map[string]*gitlab.FileContent{
			fileKey(42, "main", "foo.go"): {
				Content: []byte("hello"),
				// BlobSHA 空 —— 测试 fallback
			},
		},
	}
	src := NewGitLabSource(fc, "x")
	got, err := src.FetchFile(context.Background(),
		RepoSnapshot{ExternalProjectID: "42", DefaultBranch: "main"},
		FileEntry{Path: "foo.go", BlobSHA: "tree-sha"},
	)
	if err != nil {
		t.Fatalf("FetchFile: %v", err)
	}
	if got.BlobSHA != "tree-sha" {
		t.Errorf("expected fallback to tree-sha, got %q", got.BlobSHA)
	}
}

func TestGitLabSource_FetchFile_TooLarge(t *testing.T) {
	big := make([]byte, gitlab.MaxFileBytes+1)
	fc := &fakeClient{
		files: map[string]*gitlab.FileContent{
			fileKey(42, "main", "big.txt"): {
				Content: big,
				BlobSHA: "x",
			},
		},
	}
	src := NewGitLabSource(fc, "x")
	_, err := src.FetchFile(context.Background(),
		RepoSnapshot{ExternalProjectID: "42", DefaultBranch: "main"},
		FileEntry{Path: "big.txt"},
	)
	if !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("expected ErrFileTooLarge, got %v", err)
	}
}

func TestGitLabSource_FetchFile_Gone(t *testing.T) {
	fc := &fakeClient{files: map[string]*gitlab.FileContent{}}
	src := NewGitLabSource(fc, "x")
	_, err := src.FetchFile(context.Background(),
		RepoSnapshot{ExternalProjectID: "42", DefaultBranch: "main"},
		FileEntry{Path: "missing.go"},
	)
	if !errors.Is(err, ErrFileGone) {
		t.Errorf("expected ErrFileGone, got %v", err)
	}
}
