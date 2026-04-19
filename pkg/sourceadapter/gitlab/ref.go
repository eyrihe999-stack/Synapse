// ref.go GitLab 文件在 documents.source_ref jsonb 列里的 shape,以及和 sourceadapter.SourceRef 的互转。
//
// 设计:ref 字段只存定位用的必要项(project_id + path + ref 分支名),blob_sha/commit_id 走
// Change.ContentHash 和 RawDocument.Metadata。这样"同一文件在不同 commit"的 source_ref 保持稳定,
// Upload 按 (source_type, source_ref) upsert 就能原地覆盖旧 chunks,不会每次 commit 新起一行。
package gitlab

import (
	"encoding/json"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/pkg/sourceadapter"
)

// Ref GitLab 文件定位符。从 documents.source_ref jsonb 反序列化得到。
type Ref struct {
	// ProjectID GitLab 内部数字 ID。比 path_with_namespace 稳定(后者会随 rename/transfer 变)。
	ProjectID int64 `json:"project_id"`
	// Path 相对 repo root 的完整路径,如 "src/internal/foo.go"。
	Path string `json:"path"`
	// Ref 分支或 tag 名。当前 MVP 固定用 project 的 default_branch。
	// 未来如果支持"按分支同步"要把 Ref 纳入 source_ref,让不同分支的同名文件能独立存在。
	Ref string `json:"ref"`
	// PathWithNamespace 冗余字段,方便人工查 documents 表时能直接看到 repo 归属。
	// 不参与 Validate,缺省不影响定位。
	PathWithNamespace string `json:"path_with_namespace,omitempty"`
}

// AdapterType 本 adapter 的 source_type 标识,写入 documents.source_type 列。
const AdapterType = "gitlab_file"

// Validate 防御性校验 —— DB 里的 jsonb 结构畸形时 Fetch 早失败比深入调用后才爆好排查。
func (r *Ref) Validate() error {
	if r.ProjectID == 0 {
		return fmt.Errorf("gitlab ref: project_id required")
	}
	if r.Path == "" {
		return fmt.Errorf("gitlab ref: path required")
	}
	if r.Ref == "" {
		return fmt.Errorf("gitlab ref: ref required")
	}
	return nil
}

// ToSourceRef 序列化成 sourceadapter.SourceRef(json.RawMessage),让 Change.Ref 可以直通返回。
func (r *Ref) ToSourceRef() (sourceadapter.SourceRef, error) {
	buf, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("gitlab ref marshal: %w", err)
	}
	return sourceadapter.SourceRef(buf), nil
}

// RefFromSourceRef 反向解析。严格校验 —— 缺字段立即报错,不 silently fallback。
func RefFromSourceRef(src sourceadapter.SourceRef) (*Ref, error) {
	if len(src) == 0 {
		return nil, fmt.Errorf("gitlab ref: empty source_ref")
	}
	var r Ref
	if err := json.Unmarshal(src, &r); err != nil {
		return nil, fmt.Errorf("gitlab ref unmarshal: %w", err)
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return &r, nil
}
