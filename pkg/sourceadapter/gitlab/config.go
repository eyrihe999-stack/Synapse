// Package gitlab GitLab PAT 模式的最小 HTTP 客户端。
//
// 设计选择:
//   - 不拉 github.com/xanzy/go-gitlab(生成代码体量大,我们只用 4-5 个接口,不值)
//   - ClientAPI 抽象出 Adapter 真正依赖的方法集 —— HTTP 实现走 httpClient,测试走 fake
//   - v1 只做 PAT(Personal Access Token),不支持 OAuth。Token 由调用方传入,Client 不管生命周期
//     (PAT 要么长期有效,要么被用户/管理员撤销,撤销后调用会 401)
//
// Slice 1 只实现 GetCurrentUser —— 足够验证"贴 PAT → 后端确认有效 → 前端显示用户名"的闭环。
// 后续 Slice 会补 ListProjects / ListTree / GetFile / CompareRefs 等。
package gitlab

import (
	"errors"
	"strings"
	"time"
)

// Config GitLab 客户端配置。BaseURL 必须以 /api/v4 结尾,PAT 由调用方在构造时传入。
type Config struct {
	// BaseURL GitLab API 根。必须以 /api/v4 结尾。
	// SaaS:https://gitlab.com/api/v4。自建:https://gitlab.mycompany.com/api/v4。
	BaseURL string

	// InsecureSkipVerify 仅为自签名证书的内网自托管场景使用。生产环境必须 false。
	InsecureSkipVerify bool

	// RequestTimeout 单次 HTTP 请求超时。零值默认 30s。
	RequestTimeout time.Duration
}

// Validate 启动期 Config 合法性检查。BaseURL 必填,且必须包含 /api/v 路径(防止用户误填 https://gitlab.com)。
func (c *Config) Validate() error {
	if strings.TrimSpace(c.BaseURL) == "" {
		return errors.New("gitlab: base_url required")
	}
	if !strings.Contains(c.BaseURL, "/api/v") {
		return errors.New("gitlab: base_url must include API version path (e.g. https://gitlab.com/api/v4)")
	}
	return nil
}

// applyDefaults 就地填零值兜底,并清洗 BaseURL 尾部斜杠。Validate 必须已通过。
func (c *Config) applyDefaults() {
	if c.RequestTimeout == 0 {
		c.RequestTimeout = 30 * time.Second
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
}
