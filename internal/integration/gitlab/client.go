// Package gitlab 是 Synapse 对 GitLab REST API 的最小子集封装。
//
// 设计取舍:
//   - 不引第三方 SDK(go-gitlab) — 我们用到的端点 ≤ 5 个,直接 net/http 写薄一层比拉一个完整 SDK 干净
//   - error 走 sentinel:GitLab 401/404 / 5xx 翻成 source.ErrSourceGitLabAuthFailed /
//     ErrSourceGitLabRepoNotFound / ErrSourceGitLabUpstream,service 层 / runner 层用 errors.Is 路由
//   - Client 无状态,可并发使用;BaseURL + PAT 在构造时绑定;失败重试由调用方决定
//
// 使用方:
//   - source.service.CreateGitLabSource:VerifyToken + GetProject 双校验
//   - asyncjob.runners.gitlabsync:VerifyToken(确认凭据未失效)+ GetProject + ListTreeRecursive +
//     GetFileRaw + GetCommit
package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/source"
)

// DefaultBaseURL 公共 SaaS GitLab 实例。自托管走构造时传入。
const DefaultBaseURL = "https://gitlab.com"

// httpTimeout 单个请求超时。同步 runner 拉单文件慢 + GitLab 偶发抖动 → 30s 给宽。
// list_tree 走分页(per_page≤100),单页一般 < 5s;真有超大 repo 树需求,分页本身就把单请求体积压住了。
const httpTimeout = 30 * time.Second

// User GitLab `/api/v4/user` 响应的 minimal 字段子集(VerifyToken 用)。
type User struct {
	ID       uint64 `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Email    string `json:"email"`
}

// Project GitLab `/api/v4/projects/:id` 响应的 minimal 字段子集。
type Project struct {
	ID                uint64 `json:"id"`
	PathWithNamespace string `json:"path_with_namespace"`
	WebURL            string `json:"web_url"`
	DefaultBranch     string `json:"default_branch"`
}

// TreeNode `/api/v4/projects/:id/repository/tree` 单条目。
//
// Type:"tree"(目录)/ "blob"(文件)/ "commit"(submodule)。同步只关心 blob。
type TreeNode struct {
	ID   string `json:"id"` // blob/tree sha
	Name string `json:"name"`
	Type string `json:"type"`
	Path string `json:"path"`
	Mode string `json:"mode"`
}

// Commit `/api/v4/projects/:id/repository/commits/:sha` 响应的 minimal 字段。
type Commit struct {
	ID        string    `json:"id"`        // 完整 sha
	ShortID   string    `json:"short_id"`  // 8 位
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
}

// FileChange repository/compare 返回的 diffs 单条 — 按变更类型分类后的最小信息集。
//
// Status 取值:
//   - "added"   :新增文件;Path=新路径,OldPath=""
//   - "modified":内容变更,路径不变
//   - "renamed" :重命名(可能含内容修改);Path=新路径,OldPath=旧路径
//   - "removed" :删除;Path=旧路径(给上层删 documents 用),OldPath=""
type FileChange struct {
	Path    string
	OldPath string
	Status  string
}

// gitlabDiffEntry GitLab compare API 返回的 diff 子结构。
// 完整字段含 diff 文本(uchunk 格式),我们只关心元数据。
type gitlabDiffEntry struct {
	OldPath     string `json:"old_path"`
	NewPath     string `json:"new_path"`
	NewFile     bool   `json:"new_file"`
	DeletedFile bool   `json:"deleted_file"`
	RenamedFile bool   `json:"renamed_file"`
}

// gitlabCompareResp `/api/v4/projects/:id/repository/compare` 顶层响应。
type gitlabCompareResp struct {
	Diffs []gitlabDiffEntry `json:"diffs"`
}

// Client GitLab REST API 薄封装。无状态;并发安全。
type Client struct {
	baseURL string // 不带尾 /,构造期 trim
	pat     string
	hc      *http.Client
}

// New 构造。baseURL 空串 → DefaultBaseURL;尾部多余的 / 会被 trim。
// pat 不能为空;空 PAT 任何调用都会被 GitLab 401。
func New(baseURL, pat string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	return &Client{
		baseURL: baseURL,
		pat:     pat,
		hc:      &http.Client{Timeout: httpTimeout},
	}
}

// BaseURL 返回构造时绑定的 base url(已 trim)。
func (c *Client) BaseURL() string { return c.baseURL }

// VerifyToken 调 `/api/v4/user`,验 PAT 有效并返当前 user。
//
// 错误:
//   - GitLab 401 → ErrSourceGitLabAuthFailed
//   - 5xx / 网络 → ErrSourceGitLabUpstream
//   - 4xx 其他 → ErrSourceGitLabUpstream(包装 status + body 摘要)
func (c *Client) VerifyToken(ctx context.Context) (*User, error) {
	var u User
	if err := c.doJSON(ctx, http.MethodGet, "/api/v4/user", nil, &u); err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	return &u, nil
}

// GetProject 调 `/api/v4/projects/:id`(URL-encoded id 兼容数字 id 和 path_with_namespace)。
//
// 错误:
//   - 401 → ErrSourceGitLabAuthFailed
//   - 404 / 403 → ErrSourceGitLabRepoNotFound(GitLab 对凭据看不到的 repo 返 404 不返 403)
//   - 5xx → ErrSourceGitLabUpstream
func (c *Client) GetProject(ctx context.Context, projectID string) (*Project, error) {
	path := "/api/v4/projects/" + url.PathEscape(projectID)
	var p Project
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &p); err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	return &p, nil
}

// ListTreeRecursive 递归列 (project, ref) 下所有 blob/tree 节点。
//
// 自动分页:GitLab 对 tree 端点用 X-Next-Page header 分页,per_page=100 是上限;
// 我们一直翻直到 X-Next-Page 为空。
//
// 调用方一般只关心 Type=="blob",自行过滤;返 []TreeNode 包含目录节点(可由调用方判路径前缀)。
//
// 错误同 GetProject。
func (c *Client) ListTreeRecursive(ctx context.Context, projectID, ref string) ([]TreeNode, error) {
	path := "/api/v4/projects/" + url.PathEscape(projectID) + "/repository/tree"
	q := url.Values{}
	q.Set("recursive", "true")
	q.Set("per_page", "100")
	q.Set("ref", ref)

	var out []TreeNode
	page := 1
	for {
		q.Set("page", strconv.Itoa(page))
		batch := []TreeNode{}
		next, err := c.doJSONPaged(ctx, http.MethodGet, path+"?"+q.Encode(), nil, &batch)
		if err != nil {
			//sayso-lint:ignore sentinel-wrap
			return nil, err
		}
		out = append(out, batch...)
		if next == 0 {
			break
		}
		page = next
	}
	return out, nil
}

// GetFileRaw 拉单文件原始字节。GitLab 对 raw 端点不强制 ref query,这里显式传。
//
// projectID:数字 id 字符串 / path_with_namespace 都可
// path:相对 repo 根的文件路径(如 "internal/foo/bar.go")
// ref:branch / tag / commit sha
//
// 错误:
//   - 401 → ErrSourceGitLabAuthFailed
//   - 404 → ErrSourceGitLabRepoNotFound(文件不存在或被删)
//   - 5xx → ErrSourceGitLabUpstream
func (c *Client) GetFileRaw(ctx context.Context, projectID, filePath, ref string) ([]byte, error) {
	path := "/api/v4/projects/" + url.PathEscape(projectID) +
		"/repository/files/" + url.PathEscape(filePath) + "/raw"
	q := url.Values{}
	q.Set("ref", ref)
	return c.doRawBytes(ctx, http.MethodGet, path+"?"+q.Encode(), nil)
}

// CompareCommits 调 `/api/v4/projects/:id/repository/compare?from=&to=`,把 diffs 翻成
// FileChange 列表。
//
// 注意:GitLab compare 不分页 —— 大 diff 全在一个响应里。1000+ 文件改动时单次响应可能 ~MB 级,
// 但相比全量 ListTreeRecursive(动辄 ~10k 文件)仍小一个量级,可以接受。
//
// 错误同 ListTreeRecursive。
func (c *Client) CompareCommits(ctx context.Context, projectID, fromSHA, toSHA string) ([]FileChange, error) {
	q := url.Values{}
	q.Set("from", fromSHA)
	q.Set("to", toSHA)
	// straight=true 让 GitLab 走"二点比较"语义(...A..B 对应的 git diff),而非 "三点 merge-base"
	// 后者会少掉对主线 force-push / rebase 后的真实变更。
	q.Set("straight", "true")
	path := "/api/v4/projects/" + url.PathEscape(projectID) + "/repository/compare?" + q.Encode()

	var resp gitlabCompareResp
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &resp); err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}

	out := make([]FileChange, 0, len(resp.Diffs))
	for _, d := range resp.Diffs {
		switch {
		case d.DeletedFile:
			// 删除:用 OldPath(GitLab 在 deleted_file 时 NewPath 仍填了一份等于 OldPath 的值,以 OldPath 为权威)
			out = append(out, FileChange{Path: d.OldPath, Status: "removed"})
		case d.RenamedFile:
			out = append(out, FileChange{Path: d.NewPath, OldPath: d.OldPath, Status: "renamed"})
		case d.NewFile:
			out = append(out, FileChange{Path: d.NewPath, Status: "added"})
		default:
			out = append(out, FileChange{Path: d.NewPath, Status: "modified"})
		}
	}
	return out, nil
}

// GetCommit 拉单个 commit 元信息(用来把 ref=branch 解析到具体 sha,作 documents.version 写入)。
func (c *Client) GetCommit(ctx context.Context, projectID, ref string) (*Commit, error) {
	path := "/api/v4/projects/" + url.PathEscape(projectID) + "/repository/commits/" + url.PathEscape(ref)
	var cm Commit
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &cm); err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	return &cm, nil
}

// ─── 内部 HTTP 帮手 ──────────────────────────────────────────────────────────

// doJSON 发请求,把 200 body 解到 out。out 为 nil 表示丢弃 body。
func (c *Client) doJSON(ctx context.Context, method, path string, body io.Reader, out any) error {
	resp, err := c.do(ctx, method, path, body)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("gitlab: decode %s: %w: %w", path, err, source.ErrSourceGitLabUpstream)
	}
	return nil
}

// doJSONPaged 同 doJSON,额外读 X-Next-Page。next == 0 表示没有下一页。
func (c *Client) doJSONPaged(ctx context.Context, method, path string, body io.Reader, out any) (int, error) {
	resp, err := c.do(ctx, method, path, body)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return 0, err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return 0, fmt.Errorf("gitlab: decode %s: %w: %w", path, err, source.ErrSourceGitLabUpstream)
	}
	next := 0
	if h := resp.Header.Get("X-Next-Page"); h != "" {
		// 解析失败留 0 等价于无下一页 — GitLab 偶尔返空字符串 / 非数字
		if n, parseErr := strconv.Atoi(h); parseErr == nil {
			next = n
		}
	}
	return next, nil
}

// doRawBytes 把 200 body 全读完返字节。文件类端点用。
//
// 内存:files/raw 端点最大读 source.MaxGitLabFileBytes(5MB);超过用 io.LimitReader 截断 + 返 upstream 错。
// 设计上不允许"拉一个 200MB 的二进制全到内存",所以硬限制比 chunker 8KB 更早一道防线。
func (c *Client) doRawBytes(ctx context.Context, method, path string, body io.Reader) ([]byte, error) {
	resp, err := c.do(ctx, method, path, body)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, source.MaxGitLabFileBytes+1)
	buf, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("gitlab: read body %s: %w: %w", path, err, source.ErrSourceGitLabUpstream)
	}
	if len(buf) > source.MaxGitLabFileBytes {
		return nil, fmt.Errorf("gitlab: file %s exceeds %d bytes: %w", path, source.MaxGitLabFileBytes, source.ErrSourceGitLabUpstream)
	}
	return buf, nil
}

// do 共用的请求构造 + 状态码翻译。返 200~299 才返 resp;其他翻成 sentinel 并 drain body。
func (c *Client) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("gitlab: build request: %w: %w", err, source.ErrSourceGitLabUpstream)
	}
	if c.pat != "" {
		// PRIVATE-TOKEN 比 Authorization: Bearer 更通用 —— GitLab 自托管老版本支持 PRIVATE-TOKEN
		// 已久,新版双兼容;Bearer 在某些 OAuth-only 实例下行为不一致。
		req.Header.Set("PRIVATE-TOKEN", c.pat)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		// network / context canceled / TLS — 全部归 upstream
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			//sayso-lint:ignore sentinel-wrap
			return nil, err
		}
		return nil, fmt.Errorf("gitlab: %s %s: %w: %w", method, path, err, source.ErrSourceGitLabUpstream)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	// 非 2xx — 翻成 sentinel,drain body
	defer resp.Body.Close()
	bodySnippet := readSnippet(resp.Body, 256)
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return nil, fmt.Errorf("gitlab: %s %s 401 %s: %w", method, path, bodySnippet, source.ErrSourceGitLabAuthFailed)
	case http.StatusNotFound, http.StatusForbidden:
		return nil, fmt.Errorf("gitlab: %s %s %d %s: %w", method, path, resp.StatusCode, bodySnippet, source.ErrSourceGitLabRepoNotFound)
	default:
		return nil, fmt.Errorf("gitlab: %s %s %d %s: %w", method, path, resp.StatusCode, bodySnippet, source.ErrSourceGitLabUpstream)
	}
}

// readSnippet 安全读前 N 字节 body(用于错误信息),失败返空串。
func readSnippet(r io.Reader, n int) string {
	buf := make([]byte, n)
	got, _ := io.ReadFull(r, buf)
	return strings.TrimSpace(string(buf[:got]))
}
