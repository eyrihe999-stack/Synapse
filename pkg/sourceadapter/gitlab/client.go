package gitlab

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ClientAPI Adapter / service 真正依赖的 HTTP 调用集合。单测用 fake 实现替换。
type ClientAPI interface {
	// GetCurrentUser 调 GET /user,用传入的 PAT 作身份验证。
	// token 无效返回 ErrInvalidToken;网络或服务端错误走普通 error。
	GetCurrentUser(ctx context.Context, token string) (*CurrentUser, error)

	// ListProjects 拉 PAT 可见的 project 一页。membership=true,只返当前用户参与的项目
	// (避免 SaaS 模式下把整个 gitlab.com 的公开 repo 扫进来)。simple=true 减响应体。
	// 返回 NextPage=0 表示没有下一页。调用方(adapter)负责循环拉全部页。
	ListProjects(ctx context.Context, token string, page, perPage int) (*ProjectsPage, error)

	// ListTree 拉某 project 指定 ref 下的文件树一页。recursive=true 递归列子目录。
	// path 可空(空=repo 根)。ref 空 GitLab 会用 project 的 default_branch,
	// 但我们要求显式传 —— ListProjects 已经返了 DefaultBranch,adapter 层自己拼。
	//
	// 返回 ErrProjectNotFound 表示 project 不存在或 PAT 无权限;ErrInvalidToken 表示 401。
	ListTree(ctx context.Context, token string, projectID int64, ref, path string, page, perPage int) (*TreePage, error)

	// GetRawFile 按 project + ref + path 拉原始字节。path 不做 URL encode,
	// 由实现内部处理(GitLab 要求整条 path 做 percent-encode,包括斜杠变 %2F)。
	//
	// 返回 FileContent.Content = 原始字节(未 base64),BlobSHA / CommitID 从 response header 取。
	// 文件不存在返 ErrFileNotFound;401 返 ErrInvalidToken。
	GetRawFile(ctx context.Context, token string, projectID int64, ref, path string) (*FileContent, error)
}

// CurrentUser GET /user 返回的用户信息子集。GitLab 实际字段很多,只挑我们展示需要的。
// https://docs.gitlab.com/ee/api/users.html#list-current-user
type CurrentUser struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	AvatarURL string `json:"avatar_url"`
	WebURL    string `json:"web_url"`
	State     string `json:"state"` // "active" / "blocked" / ...
}

// Project GET /projects 返回列表项的字段子集。Sync 只需要定位 project + 拉文件树所需信息。
type Project struct {
	ID                int64  `json:"id"`
	Name              string `json:"name"`
	PathWithNamespace string `json:"path_with_namespace"` // 如 "group/subgroup/repo",组织内稳定标识
	DefaultBranch     string `json:"default_branch"`      // 空 repo 可能为空 —— adapter 要跳过
	WebURL            string `json:"web_url"`
	Archived          bool   `json:"archived"` // 归档 repo adapter 层会 skip
}

// ProjectsPage 一页 project + 下一页页码。NextPage=0 = 没有更多。
type ProjectsPage struct {
	Projects []Project
	NextPage int
}

// TreeEntry GET /projects/:id/repository/tree 列表项。
type TreeEntry struct {
	ID   string `json:"id"`   // blob 时是 blob sha,tree 时是 tree sha
	Name string `json:"name"` // 文件名,不带路径
	Type string `json:"type"` // "blob" / "tree" / "commit"(submodule)
	Path string `json:"path"` // 相对 repo root 的完整路径
	Mode string `json:"mode"` // "100644" / "040000" 等 git 文件模式
}

// TreePage 一页 tree entry + 下一页页码。NextPage=0 = 没有更多。
type TreePage struct {
	Entries  []TreeEntry
	NextPage int
}

// FileContent GetRawFile 返回。Content 是原始字节(GitLab raw endpoint 不做 base64 编码)。
// BlobSHA 用作 Change.ContentHash —— 相同 blob 的文件跨 repo 会触发 Upload dedup 分支,省 embed。
type FileContent struct {
	Content  []byte
	BlobSHA  string // X-Gitlab-Blob-Id header,内容指纹
	CommitID string // X-Gitlab-Last-Commit-Id header,最后改动此文件的 commit
	Size     int64  // X-Gitlab-Size header;header 缺失或解析失败时为 0
	Ref      string // X-Gitlab-Ref header 回显
}

// ErrInvalidToken PAT 无效或已撤销(GitLab 返回 401)。调用方拿到这个错误应提示用户重新粘贴 token。
var ErrInvalidToken = errors.New("gitlab: invalid or revoked token")

// ErrProjectNotFound ListTree 时 project 不存在或 PAT 没权限访问(404)。
// adapter Sync 应该 skip 该 project 继续扫其他,不让一个被删/无权的 repo 整体失败。
var ErrProjectNotFound = errors.New("gitlab: project not found or not accessible")

// ErrFileNotFound GetRawFile 时文件不存在(404)。tree 列出后文件又被删的并发窗口,或符号链接指向仓库外。
// adapter Fetch 应作为单文件失败处理(skip,别让整轮 sync 炸)。
var ErrFileNotFound = errors.New("gitlab: file not found")

// ─── HTTP 实现 ───────────────────────────────────────────────────────────────

type httpClient struct {
	cfg  Config
	http *http.Client
}

// NewClient 构造 GitLab HTTP 客户端。Token 不在此传入,每次调用时带。
// 一个实例可被多 user 共享(GitLab client 本身无状态,不缓存任何用户信息)。
func NewClient(cfg Config) (ClientAPI, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg.applyDefaults()

	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.InsecureSkipVerify {
		// 仅限内网自签名证书场景,生产必须 false。Config.Validate 不拦,靠部署者自觉。
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	return &httpClient{
		cfg: cfg,
		http: &http.Client{
			Timeout:   cfg.RequestTimeout,
			Transport: transport,
		},
	}, nil
}

func (c *httpClient) GetCurrentUser(ctx context.Context, token string) (*CurrentUser, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrInvalidToken
	}
	var out CurrentUser
	if _, err := c.getJSON(ctx, "/user", token, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpClient) ListProjects(ctx context.Context, token string, page, perPage int) (*ProjectsPage, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrInvalidToken
	}
	page, perPage = normalizePage(page, perPage)
	q := url.Values{}
	q.Set("membership", "true")
	q.Set("simple", "true")
	q.Set("archived", "false")
	q.Set("per_page", strconv.Itoa(perPage))
	q.Set("page", strconv.Itoa(page))
	q.Set("order_by", "id")
	q.Set("sort", "asc")

	var projects []Project
	hdr, err := c.getJSON(ctx, "/projects?"+q.Encode(), token, &projects)
	if err != nil {
		return nil, err
	}
	return &ProjectsPage{
		Projects: projects,
		NextPage: parseNextPage(hdr),
	}, nil
}

func (c *httpClient) ListTree(ctx context.Context, token string, projectID int64, ref, path string, page, perPage int) (*TreePage, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrInvalidToken
	}
	if projectID == 0 {
		return nil, fmt.Errorf("gitlab: projectID required")
	}
	if strings.TrimSpace(ref) == "" {
		return nil, fmt.Errorf("gitlab: ref required")
	}
	page, perPage = normalizePage(page, perPage)
	q := url.Values{}
	q.Set("ref", ref)
	q.Set("recursive", "true")
	q.Set("per_page", strconv.Itoa(perPage))
	q.Set("page", strconv.Itoa(page))
	if path != "" {
		q.Set("path", path)
	}

	var entries []TreeEntry
	apiPath := fmt.Sprintf("/projects/%d/repository/tree?%s", projectID, q.Encode())
	hdr, err := c.getJSON(ctx, apiPath, token, &entries)
	if err != nil {
		// 404 → project 不存在或无权限,抛语义错让 adapter skip
		if errors.Is(err, errNotFound) {
			return nil, ErrProjectNotFound
		}
		return nil, err
	}
	return &TreePage{
		Entries:  entries,
		NextPage: parseNextPage(hdr),
	}, nil
}

func (c *httpClient) GetRawFile(ctx context.Context, token string, projectID int64, ref, path string) (*FileContent, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrInvalidToken
	}
	if projectID == 0 {
		return nil, fmt.Errorf("gitlab: projectID required")
	}
	if strings.TrimSpace(ref) == "" {
		return nil, fmt.Errorf("gitlab: ref required")
	}
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("gitlab: path required")
	}

	// GitLab 要求把文件路径整个做 percent-encode 塞进 URL path,包括 '/' 也要变 %2F。
	// 这就是为啥不能直接用 http.NewRequest 的 URL 拼接 —— Go 的 URL 构造会把 %2F 还原成 /。
	// 解决方法:手动 QueryEscape 路径,拼完整 URL 字符串后交给 http.NewRequest(它不重编码 path)。
	escapedPath := url.QueryEscape(path)
	q := url.Values{}
	q.Set("ref", ref)
	apiPath := fmt.Sprintf("/projects/%d/repository/files/%s/raw?%s", projectID, escapedPath, q.Encode())

	body, hdr, err := c.send(ctx, apiPath, token, "")
	if err != nil {
		if errors.Is(err, errNotFound) {
			return nil, ErrFileNotFound
		}
		return nil, err
	}
	fc := &FileContent{
		Content:  body,
		BlobSHA:  hdr.Get("X-Gitlab-Blob-Id"),
		CommitID: hdr.Get("X-Gitlab-Last-Commit-Id"),
		Ref:      hdr.Get("X-Gitlab-Ref"),
	}
	if sz := hdr.Get("X-Gitlab-Size"); sz != "" {
		if n, perr := strconv.ParseInt(sz, 10, 64); perr == nil {
			fc.Size = n
		}
	}
	return fc, nil
}

// ─── 内部 HTTP 辅助 ─────────────────────────────────────────────────────────

// errNotFound 内部 sentinel,send() 用来标 404,上层语义化成 ErrProjectNotFound / ErrFileNotFound。
// 不对外暴露 —— 接口层只暴露业务含义的 not-found,不让调用方直接判 HTTP 404。
var errNotFound = errors.New("gitlab: not found")

// getJSON 共用的 JSON GET。成功时 Unmarshal 到 out,返 response header 供分页解析。
// 404 返 errNotFound sentinel;401 返 ErrInvalidToken;其他非 2xx 返带 body 片段的 error。
func (c *httpClient) getJSON(ctx context.Context, path, token string, out any) (http.Header, error) {
	body, hdr, err := c.send(ctx, path, token, "application/json")
	if err != nil {
		return nil, err
	}
	if out != nil && len(body) > 0 {
		if err := json.Unmarshal(body, out); err != nil {
			return nil, fmt.Errorf("gitlab: decode %s: %w", path, err)
		}
	}
	return hdr, nil
}

// send 统一 GET 执行层。accept 空 = 不设 Accept header(raw endpoint 不需要强制 json)。
// 返 (body, response header, error)。
func (c *httpClient) send(ctx context.Context, path, token, accept string) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.BaseURL+path, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("gitlab: http %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("gitlab: read %s: %w", path, err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		return body, resp.Header, nil
	case http.StatusUnauthorized:
		return nil, nil, ErrInvalidToken
	case http.StatusNotFound:
		return nil, nil, errNotFound
	default:
		return nil, nil, fmt.Errorf("gitlab: %s returned HTTP %d: %s", path, resp.StatusCode, truncate(body, 256))
	}
}

// parseNextPage 读 GitLab 分页 header。X-Next-Page 非空+非"0" = 还有下一页;否则返 0。
// 注意:GitLab 在最后一页会把 X-Next-Page 设为空字符串(不是数字 0),也有老版本返 "0",两种都视为终止。
func parseNextPage(h http.Header) int {
	v := strings.TrimSpace(h.Get("X-Next-Page"))
	if v == "" || v == "0" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// normalizePage 给分页参数上护栏。page<1 → 1;perPage 范围 [1, 100](GitLab 上限 100)。
func normalizePage(page, perPage int) (int, int) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 50
	}
	if perPage > 100 {
		perPage = 100
	}
	return page, perPage
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
