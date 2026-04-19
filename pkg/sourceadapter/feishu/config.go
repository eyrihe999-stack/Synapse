// Package feishu 飞书(Lark)云文档 SourceAdapter 实现。
//
// 对接飞书 OpenAPI 的三类文件源:
//   - Drive:企业云空间的文档、文件夹(drive/v1/files)
//   - Wiki: 知识库树(wiki/v2/spaces/...)
//   - Docx: 新版文档内容(docx/v1/documents/...)
//
// 映射到 SourceAdapter 的形式:
//   Type() = "feishu_doc"
//   SourceRef 例:{"file_token":"doxcnXXX","type":"docx","space_id":"wikcnYYY"}
//
// ─── Auth 模型(MVP 纯 user token)─────────────────────────────────────────────
//
// 每个 Adapter 实例绑定一个用户 —— 用该 user 的 user_access_token 调飞书 API,
// Sync 扫到的文档范围 = 这位用户实际能看到的文档范围。与 Synapse 的 ACL-first 定位一致:
// user 本来看不到的 agent 也拉不到。
//
// 调用方需要:
//   1. 引导用户走飞书 OAuth 授权,换到 refresh_token
//   2. 持久化 refresh_token(users 表或单独 integrations 表)
//   3. 后台 worker 按用户逐个构造 Adapter 并调 Sync/Fetch
//   4. 用户撤销授权时删 refresh_token,下次 Adapter 自动失效
//
// 未来想扩 tenant-token(全企业扫),可在 client.go 加一个 TokenProvider 实现,
// Adapter 结构无需大改(现在刻意把 Client 构造点做成 interface + 内部 tokener)。
//
// 本文件提供 Config / 常量,真实 API 调用见 client.go,Adapter 编排见 adapter.go。
package feishu

import (
	"fmt"
	"strings"
	"time"
)

// 飞书 OpenAPI 域名常量。中国区 (open.feishu.cn) 和海外版 (open.larksuite.com) 两套环境。
const (
	BaseURLFeishu    = "https://open.feishu.cn"
	BaseURLLarkSuite = "https://open.larksuite.com"
)

// AdapterType 本 adapter 的 source_type 标识,写入 documents.source_type 列。
// 和 pkg/sourceadapter.Registry 的 Get/Register 一致。
const AdapterType = "feishu_doc"

// 支持的飞书文件类型。MVP 只做 docx(新版文档),其他类型 Sync 扫到但 Fetch 时跳过或单独处理。
const (
	FileTypeDocx      = "docx"      // 新版文档,主力支持
	FileTypeSheet     = "sheet"     // 电子表格,未来扩
	FileTypeBitable   = "bitable"   // 多维表格,未来扩
	FileTypeMindnote  = "mindnote"  // 思维笔记,未来扩
	FileTypeSlides    = "slides"    // 幻灯片,未来扩
	FileTypeDoc       = "doc"       // 旧版文档(已废弃),转 docx
	FileTypeFile      = "file"      // 普通上传文件(pdf/docx 二进制),走 Extractor 路径
	FileTypeFolder    = "folder"    // 目录,Sync 时递归,Fetch 不应被调用
)

// Config 飞书 Adapter 配置(app 级配置,所有 Adapter 实例共用)。
//
// AppID + AppSecret 来自飞书开发者后台的 "自建应用" 详情页。
// BaseURL 选择中国区还是海外版:默认 BaseURLFeishu,国际化企业填 BaseURLLarkSuite。
//
// **User 级配置(每 Adapter 实例不同)**:refresh_token 由构造函数 NewAdapter 的参数传入,
// 不进 Config —— 避免把用户级密钥和应用级配置混在同一 struct,生命周期也不同。
//
// Scope 字段限定 Sync 扫描范围(在用户权限之内再筛一次):
//   - 全部留空 = 扫用户所有可见 drive + wiki
//   - 填 RootFolderToken = 仅扫该目录及子树
//   - 填 WikiSpaceIDs = 仅扫指定 wiki 空间
//
// RequestTimeout 单次 HTTP 请求上限;默认 10s。
// RateLimit 每秒请求上限,飞书 OpenAPI 典型 50-100 QPS/app,保守给 30 避免 429。
type Config struct {
	AppID     string
	AppSecret string
	BaseURL   string

	// Scope 过滤,为空不限。
	RootFolderToken string
	WikiSpaceIDs    []string

	RequestTimeout time.Duration
	RateLimit      int

	// OnRefreshTokenRotated 当 userTokener 刷 access_token 时飞书返回了新 refresh_token
	// 会触发此回调。调用方负责把新 refresh_token(及过期时间)写回持久层。
	//
	// 为什么必须回写:飞书 refresh_token 是"使用即轮换"——旧 token 一旦用过就失效。
	// 如果只保留在内存,进程下一次启动读 DB 拿到的就是已失效的旧值,refresh 直接返 code=20026。
	//
	// 回调在 tokener 内部锁内同步调用,调用方避免做重 IO;写 DB 足够快。失败不会阻塞本次
	// access_token 的使用,但会 log(调用方负责)——否则会陷入"本次 OK 下次挂"的沉默 bug。
	//
	// 可为 nil(测试 / 一次性脚本);生产路径必填。
	OnRefreshTokenRotated func(newRefreshToken string, refreshExpiresIn int)
}

// Validate 启动期校验。AppID / AppSecret 必填,别的字段能容忍空(走默认)。
func (c *Config) Validate() error {
	if strings.TrimSpace(c.AppID) == "" {
		return fmt.Errorf("feishu: AppID required")
	}
	if strings.TrimSpace(c.AppSecret) == "" {
		return fmt.Errorf("feishu: AppSecret required")
	}
	if c.BaseURL == "" {
		c.BaseURL = BaseURLFeishu
	}
	if c.RequestTimeout == 0 {
		c.RequestTimeout = 10 * time.Second
	}
	if c.RateLimit == 0 {
		c.RateLimit = 30
	}
	return nil
}
