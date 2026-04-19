package retrieval

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// Retriever 各模态检索实现的统一接口。
//
// 实现约束:
//   - Search 必须强制按 q.OrgID 隔离,任何从 Filter 解出的疑似 org_id 字段都不可信任;
//   - Modality() 返回值必须与 Registry 注册键一致;
//   - FilterSchema() 返回 JSON Schema 片段,用于生成 MCP tool input_schema + 请求前校验;
//   - FetchByID 支持 agent 按 Hit.ID / Hit.Parents 拉扩展上下文。
type Retriever interface {
	Modality() Modality
	FilterSchema() json.RawMessage
	Search(ctx context.Context, q Query) ([]Hit, error)
	FetchByID(ctx context.Context, orgID uint64, id string) (*Hit, error)
}

// Registry 进程启动期注册完毕、运行期只读的并发安全注册表。
type Registry struct {
	mu    sync.RWMutex
	store map[Modality]Retriever
}

func NewRegistry() *Registry { return &Registry{store: map[Modality]Retriever{}} }

// Register 重复注册会覆盖(启动期错配直接盖掉,不做历史保留)。
func (r *Registry) Register(rv Retriever) error {
	m := rv.Modality()
	if m == "" {
		return fmt.Errorf("retrieval: retriever has empty modality")
	}
	r.mu.Lock()
	r.store[m] = rv
	r.mu.Unlock()
	return nil
}

func (r *Registry) Get(m Modality) (Retriever, bool) {
	r.mu.RLock()
	rv, ok := r.store[m]
	r.mu.RUnlock()
	return rv, ok
}

func (r *Registry) List() []Modality {
	r.mu.RLock()
	out := make([]Modality, 0, len(r.store))
	for m := range r.store {
		out = append(out, m)
	}
	r.mu.RUnlock()
	return out
}
