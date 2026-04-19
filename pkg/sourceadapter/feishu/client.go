// client.go 飞书 OpenAPI 最小 HTTP 客户端。
//
// 设计选择:
//   - 不拉 github.com/larksuite/oapi-sdk-go(~50MB 生成代码,我们只用 5-6 个接口,不值)
//   - ClientAPI 抽象出 Adapter 真正需要的方法集 —— HTTP 实现走 httpClient,测试走 fake
//   - tokener 内部接口隔离 token 生命周期(access_token 2h 过期,refresh_token 30 天过期)
//
// 当前实现的方法仅覆盖 MVP 需要的: drive list / wiki spaces+nodes / docx blocks+meta。
// 其他(sheet / bitable / export task)扩展时沿同一模式加即可。
//
// **骨架阶段**:HTTP 路径 + 请求/响应字段结构已按飞书开放平台文档对齐,但实际端到端联调
// 要等拿到真实 app + 测试账号。每个方法保留一个 TODO 标注尚未经测试的 edge case。
package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ClientAPI Adapter 真正依赖的 HTTP 调用集合。Adapter 依赖接口而非具体 *httpClient,
// 单测可以注入 fakeClient 验证 Sync/Fetch 的编排逻辑,不用 spin up 假 HTTP server。
type ClientAPI interface {
	// ListDriveFiles 列出 folder 下的所有文件与子目录。folderToken = "" 等价用户云空间根目录。
	// 返回 page,调用方按 HasMore + NextPageToken 分页。
	ListDriveFiles(ctx context.Context, folderToken, pageToken string) (*DriveFilesPage, error)

	// ListWikiSpaces 列出当前 user 有访问权的 wiki 空间。
	ListWikiSpaces(ctx context.Context, pageToken string) (*WikiSpacesPage, error)

	// ListWikiNodes 列出 spaceID 下 parentNodeToken 的子节点。parentNodeToken = "" = 空间根。
	ListWikiNodes(ctx context.Context, spaceID, parentNodeToken, pageToken string) (*WikiNodesPage, error)

	// GetDocxMetadata 拿文档元信息(title、owner、modified_time 等)。
	// FileToken 必须是 docx 类型;其他类型(sheet/bitable)有独立端点,本 MVP 不做。
	GetDocxMetadata(ctx context.Context, fileToken string) (*DocxMetadata, error)

	// GetDocxBlocks 拿文档的全部结构化 blocks,分页拉到 HasMore=false 为止。
	// 调用方直接拿到扁平 []DocxBlock(按飞书顺序),内部分页被吞掉。
	GetDocxBlocks(ctx context.Context, fileToken string) ([]DocxBlock, error)
}

// ─── DTO(仅覆盖 MVP 需要的字段,饱和再扩)────────────────────────────────────

// DriveFile drive/v1/files 返回的单个文件条目。飞书其他字段(permission / owner_id 等)用时再加。
type DriveFile struct {
	Token        string `json:"token"`
	Name         string `json:"name"`
	Type         string `json:"type"` // "docx" / "folder" / "sheet" / ...
	URL          string `json:"url"`
	CreatedTime  string `json:"created_time"`  // unix seconds 字符串
	ModifiedTime string `json:"modified_time"` // unix seconds 字符串
	OwnerID      string `json:"owner_id"`
}

type DriveFilesPage struct {
	Files         []DriveFile `json:"files"`
	HasMore       bool        `json:"has_more"`
	NextPageToken string      `json:"next_page_token"`
}

// WikiSpace wiki/v2/spaces 的单个空间。
type WikiSpace struct {
	SpaceID   string `json:"space_id"`
	Name      string `json:"name"`
	SpaceType string `json:"space_type"`
}

type WikiSpacesPage struct {
	Spaces        []WikiSpace `json:"items"`
	HasMore       bool        `json:"has_more"`
	NextPageToken string      `json:"page_token"`
}

// WikiNode wiki 树上的一个节点。ObjType 标识挂载的实际文件类型(docx / sheet 等),
// ObjToken 是真实文件的 token —— 拉内容时用 ObjToken(不是 NodeToken)。
type WikiNode struct {
	SpaceID       string `json:"space_id"`
	NodeToken     string `json:"node_token"`
	ObjToken      string `json:"obj_token"`
	ObjType       string `json:"obj_type"` // "docx" / "sheet" / "bitable" / ...
	Title         string `json:"title"`
	HasChild      bool   `json:"has_child"`
	ParentToken   string `json:"parent_node_token"`
	NodeCreateTime string `json:"node_create_time"`
	OriginNodeCreateTime string `json:"origin_node_create_time"`
}

type WikiNodesPage struct {
	Nodes         []WikiNode `json:"items"`
	HasMore       bool       `json:"has_more"`
	NextPageToken string     `json:"page_token"`
}

// DocxMetadata 文档元信息。Display title、owner、最近修改时间。
type DocxMetadata struct {
	DocumentID   string `json:"document_id"`
	RevisionID   int    `json:"revision_id"`
	Title        string `json:"title"`
	OwnerID      string `json:"owner_id"`
	CreatedTime  int64  `json:"create_time"`
	ModifiedTime int64  `json:"update_time"`
}

// DocxBlock 飞书新版文档的结构化块。一个文档由一个 block 树组成,根节点 BlockType=1(Page)。
//
// 字段级选择:只收 MVP 需要的 —— BlockType / Text / Heading1~6 / Bullet / Ordered / Code / Quote。
// 表格、图片、嵌入 app 等复杂 block 在 blocks_to_md.go 里按类型分派,不是每种都单独建 struct。
type DocxBlock struct {
	BlockID   string `json:"block_id"`
	ParentID  string `json:"parent_id"`
	BlockType int    `json:"block_type"` // 见 feishu 文档 https://open.feishu.cn/document/server-docs/docs/docs/docx-v1/data-structure/block

	// 每种 block 实际内容在对应字段里,其余 nil。为保持 struct 扁平,用 json.RawMessage 暂存,
	// blocks_to_md.go 里按 block_type 反序列化到具体形态。
	Text      json.RawMessage `json:"text,omitempty"`
	Heading1  json.RawMessage `json:"heading1,omitempty"`
	Heading2  json.RawMessage `json:"heading2,omitempty"`
	Heading3  json.RawMessage `json:"heading3,omitempty"`
	Heading4  json.RawMessage `json:"heading4,omitempty"`
	Heading5  json.RawMessage `json:"heading5,omitempty"`
	Heading6  json.RawMessage `json:"heading6,omitempty"`
	Bullet    json.RawMessage `json:"bullet,omitempty"`
	Ordered   json.RawMessage `json:"ordered,omitempty"`
	Code      json.RawMessage `json:"code,omitempty"`
	Quote     json.RawMessage `json:"quote,omitempty"`
	Todo      json.RawMessage `json:"todo,omitempty"`
	Divider   json.RawMessage `json:"divider,omitempty"`
	Callout   json.RawMessage `json:"callout,omitempty"`
	Table     json.RawMessage `json:"table,omitempty"`
	Image     json.RawMessage `json:"image,omitempty"`
	// 其他 block type(equation / chat_card / 飞书独特的 widget)暂留原 JSON,converter 里识别不到就跳过。
}

// blockListResponse docx/v1/documents/{id}/blocks 的分页响应。
type blockListResponse struct {
	Items         []DocxBlock `json:"items"`
	HasMore       bool        `json:"has_more"`
	PageToken     string      `json:"page_token"`
}

// ─── HTTP 实现 ────────────────────────────────────────────────────────────────

// httpClient 对飞书 OpenAPI 的一层薄包装。Authorization 由 tokener 动态提供。
type httpClient struct {
	cfg     Config
	http    *http.Client
	tokener tokener
}

// NewClient 构造用户 token 路径的 Client。refreshToken 传入后,Client 自动按需刷新 access_token。
// baseURL 空 = 用 Config.BaseURL 默认。
//
// 失败场景:Config.Validate() 未过、refreshToken 空,直接返 error。网络 / token 刷新错误延迟到调用时报。
func NewClient(cfg Config, refreshToken string) (ClientAPI, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(refreshToken) == "" {
		return nil, errors.New("feishu: refresh_token required for user-token client")
	}
	return &httpClient{
		cfg:  cfg,
		http: &http.Client{Timeout: cfg.RequestTimeout},
		tokener: &userTokener{
			appID:        cfg.AppID,
			appSecret:    cfg.AppSecret,
			baseURL:      cfg.BaseURL,
			refreshToken: refreshToken,
			httpClient:   &http.Client{Timeout: cfg.RequestTimeout},
			onRotated:    cfg.OnRefreshTokenRotated,
		},
	}, nil
}

// get 是所有 API 调用的统一入口。自动:
//   - 拼 baseURL + path + query
//   - 带当前 user_access_token(失效自动刷新由 tokener 处理)
//   - 反序列化外层 {code, msg, data},code != 0 转 error
func (c *httpClient) get(ctx context.Context, path string, query url.Values, out any) error {
	token, err := c.tokener.Token(ctx)
	if err != nil {
		return fmt.Errorf("feishu: get token: %w", err)
	}
	endpoint := c.cfg.BaseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("feishu: http %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("feishu: read %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("feishu: %s returned HTTP %d: %s", path, resp.StatusCode, truncate(body, 256))
	}
	// 飞书标准响应外层:{"code":0,"msg":"success","data":{...}}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("feishu: decode envelope %s: %w", path, err)
	}
	if env.Code != 0 {
		return fmt.Errorf("feishu: %s code=%d msg=%s", path, env.Code, env.Msg)
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("feishu: decode data %s: %w", path, err)
		}
	}
	return nil
}

type envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// ─── ClientAPI 方法实现 ───────────────────────────────────────────────────────

// drive/v1/files 路径参考:https://open.feishu.cn/document/server-docs/docs/drive-v1/folder/list
func (c *httpClient) ListDriveFiles(ctx context.Context, folderToken, pageToken string) (*DriveFilesPage, error) {
	q := url.Values{}
	if folderToken != "" {
		q.Set("folder_token", folderToken)
	}
	if pageToken != "" {
		q.Set("page_token", pageToken)
	}
	q.Set("page_size", "200")
	// TODO: 按 modified_time 过滤需要 order_by=EditedTime,飞书单页无 since 参数;
	//       MVP 先全量拉再 Go 侧过滤,规模大了换搜索接口或用 webhook。
	var out DriveFilesPage
	if err := c.get(ctx, "/open-apis/drive/v1/files", q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpClient) ListWikiSpaces(ctx context.Context, pageToken string) (*WikiSpacesPage, error) {
	q := url.Values{}
	if pageToken != "" {
		q.Set("page_token", pageToken)
	}
	q.Set("page_size", "50")
	var out WikiSpacesPage
	if err := c.get(ctx, "/open-apis/wiki/v2/spaces", q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpClient) ListWikiNodes(ctx context.Context, spaceID, parentNodeToken, pageToken string) (*WikiNodesPage, error) {
	if spaceID == "" {
		return nil, errors.New("feishu: spaceID required for ListWikiNodes")
	}
	q := url.Values{}
	if parentNodeToken != "" {
		q.Set("parent_node_token", parentNodeToken)
	}
	if pageToken != "" {
		q.Set("page_token", pageToken)
	}
	q.Set("page_size", "50")
	path := fmt.Sprintf("/open-apis/wiki/v2/spaces/%s/nodes", url.PathEscape(spaceID))
	var out WikiNodesPage
	if err := c.get(ctx, path, q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpClient) GetDocxMetadata(ctx context.Context, fileToken string) (*DocxMetadata, error) {
	path := fmt.Sprintf("/open-apis/docx/v1/documents/%s", url.PathEscape(fileToken))
	var wrap struct {
		Document DocxMetadata `json:"document"`
	}
	if err := c.get(ctx, path, nil, &wrap); err != nil {
		return nil, err
	}
	return &wrap.Document, nil
}

// GetDocxBlocks 翻页拉完整 block 列表。飞书 API 单页上限 500,大多数文档一页完。
// 内部循环直到 HasMore=false;调用方拿到扁平顺序的 []DocxBlock。
func (c *httpClient) GetDocxBlocks(ctx context.Context, fileToken string) ([]DocxBlock, error) {
	path := fmt.Sprintf("/open-apis/docx/v1/documents/%s/blocks", url.PathEscape(fileToken))
	var all []DocxBlock
	pageToken := ""
	for {
		q := url.Values{}
		q.Set("page_size", "500")
		if pageToken != "" {
			q.Set("page_token", pageToken)
		}
		var page blockListResponse
		if err := c.get(ctx, path, q, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		if !page.HasMore || page.PageToken == "" {
			break
		}
		pageToken = page.PageToken
	}
	return all, nil
}

// ─── tokener:user_access_token 生命周期管理 ──────────────────────────────────

type tokener interface {
	Token(ctx context.Context) (string, error)
}

// userTokener 持久化 refresh_token,缓存 access_token 直到接近过期。
// 并发安全:Token() 在并发调用时保证只刷新一次。
type userTokener struct {
	appID        string
	appSecret    string
	baseURL      string
	refreshToken string
	httpClient   *http.Client
	// onRotated 飞书返回新 refresh_token 时调用,让上层写库 —— 必须设,否则重启后失效。
	onRotated func(newRefreshToken string, refreshExpiresIn int)

	mu          sync.Mutex
	accessToken string
	expiresAt   time.Time // 留 60s 余量提前刷
}

func (t *userTokener) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.accessToken != "" && time.Now().Before(t.expiresAt.Add(-60*time.Second)) {
		return t.accessToken, nil
	}
	// TODO: 端到端联调前验证 refresh endpoint 返回字段名(飞书偶有 access_token / token 混用)。
	// refresh_token → access_token 路径见 https://open.feishu.cn/document/server-docs/authentication-management/access-token/refresh-user-access-token
	if err := t.refresh(ctx); err != nil {
		return "", err
	}
	return t.accessToken, nil
}

// refresh 用 refresh_token 换新 access_token + 新 refresh_token(飞书会轮换)。
// 轮换后的新 refresh_token 要回写到调用方持久层 —— 否则下次启动用旧 refresh_token 会失败。
// MVP 骨架先 panic 标注,上层后面接持久化回调时改成 callback。
func (t *userTokener) refresh(ctx context.Context) error {
	payload := map[string]string{
		"grant_type":    "refresh_token",
		"app_id":        t.appID,
		"app_secret":    t.appSecret,
		"refresh_token": t.refreshToken,
	}
	body, _ := json.Marshal(payload)
	endpoint := t.baseURL + "/open-apis/authen/v1/refresh_access_token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("feishu refresh: http: %w", err)
	}
	defer resp.Body.Close()
	var env struct {
		Code int `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			AccessToken      string `json:"access_token"`
			ExpiresIn        int    `json:"expires_in"`
			RefreshToken     string `json:"refresh_token"`
			RefreshExpiresIn int    `json:"refresh_expires_in"`
		} `json:"data"`
	}
	raw, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("feishu refresh: decode: %w (body=%s)", err, truncate(raw, 200))
	}
	if env.Code != 0 {
		return fmt.Errorf("feishu refresh: code=%d msg=%s", env.Code, env.Msg)
	}
	t.accessToken = env.Data.AccessToken
	t.expiresAt = time.Now().Add(time.Duration(env.Data.ExpiresIn) * time.Second)
	// refresh_token 轮换 —— 先更新内存 再回调上层持久化。
	// 飞书 refresh_token 是"用一次就作废"的,没回写 DB 的话下次启动读老值会直接 20026。
	if env.Data.RefreshToken != "" && env.Data.RefreshToken != t.refreshToken {
		t.refreshToken = env.Data.RefreshToken
		if t.onRotated != nil {
			// 回调在锁内调用 —— 期望实现写库是毫秒级的,别做网络 / 长 IO。
			// 回调若失败由调用方自己记日志,tokener 不管(本次 access_token 还是有效,不阻塞请求)。
			t.onRotated(env.Data.RefreshToken, env.Data.RefreshExpiresIn)
		}
	}
	return nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
