// Package sourceadapter 外部数据源摄取的统一接口。每种源(markdown 上传 / git 仓库 / jira issue /
// confluence page / 图片 VLM caption / DB schema 等)实现一个 Adapter,都把原始字节转成统一的
// RawDocument 形态,走同一条 ingestion pipeline(dedup → chunker → embedder → repo)。
//
// 设计分两类场景:
//
//   **Pull-based**(Adapter 主导):适配器从外部系统拉数据。典型:定时扫 git / 周期 poll jira /
//     webhook 来了之后 Sync。用 Sync(since) 取变更列表,再 Fetch(ref) 取单篇原文。
//
//   **Push-based**(调用方主导):用户 / agent 直接把内容送进来。典型:HTTP Upload handler /
//     Agent 生成笔记后写回。这种场景不需要 Sync —— 调用方直接构造 RawDocument,喂给 pipeline。
//     Adapter 可选实现 Sync 返回空,或 Registry 里根本不登记 push 源。
//
// 当前 Tier 1 只定义接口与 Registry,无具体实现 —— 第一个 pull 实现会随 T2.1(git 仓库接入)落地,
// 届时 Adapter 的形态可能还会按真实需求微调。现在先锁住"形状的边界",让 schema / pipeline 两侧
// 代码能按约定拼接。
package sourceadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// SourceRef 源内定位符。类型随 Adapter 而异,统一用 json.RawMessage 在 Go 侧保持透明,
// 存到 documents.source_ref jsonb 列时字面写入。
//
// 例:
//   git_file:     {"repo":"git@github.com/org/repo","path":"src/main.go","commit":"abc123"}
//   jira_issue:   {"project":"INGEST","issue_id":"INGEST-42"}
//   markdown_upload: null(或 {"uploader_channel":"web"} —— HTTP 上传时无外部 ref)
//
// 具体 Adapter 实现自定义这个 jsonb 的 shape,在同一 Adapter 内保持稳定,消费方按 Type 区分解析。
type SourceRef json.RawMessage

// ChangeAction Sync 返回变更的动作类型。
type ChangeAction string

const (
	ChangeCreate ChangeAction = "create"
	ChangeUpdate ChangeAction = "update"
	ChangeDelete ChangeAction = "delete"
)

// Change Sync 返回的单个变更条目。
//
// ContentHash 可选 —— 如果 Adapter 能在 list 阶段廉价拿到(如 git blob hash),填上让
// pipeline 跳过"哈希没变不重索引"的 Fetch,省一次网络 + 切分。拿不到就留空,pipeline 自己 hash。
type Change struct {
	Action      ChangeAction
	Ref         SourceRef
	ContentHash string // 可选
}

// RawDocument 摄取 pipeline 的标准输入 —— 无论来源是什么,到 pipeline 里都是这张结构体。
//
// Metadata 存放 source-specific 但又不属于 SourceRef 的信息(例如 git commit 里的作者邮箱、
// jira issue 的状态/优先级)。pipeline 把它并进 documents.source_ref 或 chunks.metadata 里,
// 给检索 filter 和 agent 引用用。
type RawDocument struct {
	Title    string
	FileName string
	MIMEType string
	Content  []byte
	Metadata map[string]any
}

// Adapter 一种源类型的接入实现。方法集按 pull-based 语义定义;push-only 的 Adapter
// Sync 可以永远返回 (nil, nil)。
type Adapter interface {
	// Type 源类型标识,和 documents.source_type 列值一一对应,也是 Registry 的查找键。
	// 取值约定小写 + 下划线,如 "git_file" / "jira_issue"。
	Type() string

	// Sync 返回 since 之后本 org 范围内所有变更。实现自己决定是批量 poll 还是消费 webhook 队列。
	// 空 since(零值 time.Time)语义 = "全量列一次"(首次接入走这路径)。
	Sync(ctx context.Context, orgID uint64, since time.Time) ([]Change, error)

	// Fetch 按 ref 拉单篇原文。调用频率:ingestion pipeline 处理 Sync 返回的每个 Change 时调一次。
	// 实现需做自己的限流(git API rate limit / jira throttle 等),不由 pipeline 负责。
	Fetch(ctx context.Context, orgID uint64, ref SourceRef) (*RawDocument, error)
}

// Registry 按 source_type 字符串路由到 Adapter。并发安全,启动期注册 + 运行期查询。
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
}

// NewRegistry 构造空 Registry。启动期由 main 装配注册各 Adapter。
func NewRegistry() *Registry {
	return &Registry{adapters: map[string]Adapter{}}
}

// Register 登记一个 Adapter。重复注册同 Type 视为错误 —— 显式暴露配置冲突,避免"后者静默覆盖前者"的惊喜。
func (r *Registry) Register(a Adapter) error {
	if a == nil {
		return fmt.Errorf("sourceadapter: cannot register nil adapter")
	}
	t := a.Type()
	if t == "" {
		return fmt.Errorf("sourceadapter: adapter returns empty Type()")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.adapters[t]; exists {
		return fmt.Errorf("sourceadapter: type %q already registered", t)
	}
	r.adapters[t] = a
	return nil
}

// Get 按 source_type 查 Adapter。找不到返 (nil, false),调用方需 fallback 或报错。
func (r *Registry) Get(sourceType string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[sourceType]
	return a, ok
}

// Types 返回所有已注册的 source type(无序)。给 ops / 诊断日志用。
func (r *Registry) Types() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.adapters))
	for t := range r.adapters {
		out = append(out, t)
	}
	return out
}
