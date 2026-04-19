// ref.go 飞书文件在 documents.source_ref jsonb 列里的 shape,以及和 sourceadapter.SourceRef 的互转。
//
// 设计:存进 DB 的是精简必要字段(file_token + type),够定位回飞书 API 就行。
// 其他元信息(owner / modified_time / URL)走 documents 表的其他列或 metadata,不塞 source_ref,
// 让 ref 稳定 —— 即使飞书侧换了作者、ref 也不变。
package feishu

import (
	"encoding/json"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/pkg/sourceadapter"
)

// Ref 飞书文件定位符。从 documents.source_ref jsonb 反序列化得到。
type Ref struct {
	// FileToken 飞书文件的唯一标识,docx 形如 "doxcnXXX",wiki node 形如 "wikcnYYY"。
	FileToken string `json:"file_token"`
	// Type 飞书文件类型,取 FileType* 常量之一。Fetch 按此分派到不同的 API 调用。
	Type string `json:"type"`
	// SpaceID 仅 wiki 节点需要,drive 文件留空。
	SpaceID string `json:"space_id,omitempty"`
}

// Validate 防御性校验 —— DB 里的 jsonb 结构畸形时 Fetch 早失败比深入调用后才爆好排查。
func (r *Ref) Validate() error {
	if r.FileToken == "" {
		return fmt.Errorf("feishu ref: file_token required")
	}
	if r.Type == "" {
		return fmt.Errorf("feishu ref: type required")
	}
	return nil
}

// ToSourceRef 序列化成 sourceadapter.SourceRef(json.RawMessage),让 Change.Ref 可以直通返回。
func (r *Ref) ToSourceRef() (sourceadapter.SourceRef, error) {
	buf, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("feishu ref marshal: %w", err)
	}
	return sourceadapter.SourceRef(buf), nil
}

// RefFromSourceRef 反向解析。严格校验 —— 缺字段立即报错,不 silently fallback。
func RefFromSourceRef(src sourceadapter.SourceRef) (*Ref, error) {
	if len(src) == 0 {
		return nil, fmt.Errorf("feishu ref: empty source_ref")
	}
	var r Ref
	if err := json.Unmarshal(src, &r); err != nil {
		return nil, fmt.Errorf("feishu ref unmarshal: %w", err)
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return &r, nil
}
