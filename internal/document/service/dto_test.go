// dto_test.go documentToDTO 的 Source 字段行为测试。
//
// 只测纯函数部分(不走 DB / LLM / OSS),不引入 mock 框架。
// 更深的 service 层测试依赖重型集成环境,暂不铺开。
package service

import (
	"testing"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/document"
	"github.com/eyrihe999-stack/Synapse/internal/document/model"
)

func TestDocumentToDTO_SourceDefaultsToUser(t *testing.T) {
	d := &model.Document{
		ID:        1,
		OrgID:     2,
		CreatedAt: time.Unix(1700000000, 0),
		UpdatedAt: time.Unix(1700000000, 0),
		// Source 故意留空,模拟迁移前/mock 写入的行
	}
	got := documentToDTO(d)
	if got.Source != document.DocSourceUser {
		t.Errorf("empty Source should map to %q, got %q", document.DocSourceUser, got.Source)
	}
}

func TestDocumentToDTO_SourcePreservedWhenSet(t *testing.T) {
	cases := []string{
		document.DocSourceUser,
		document.DocSourceAIGenerated,
	}
	for _, src := range cases {
		t.Run(src, func(t *testing.T) {
			d := &model.Document{
				ID:        1,
				OrgID:     2,
				Source:    src,
				CreatedAt: time.Unix(1700000000, 0),
				UpdatedAt: time.Unix(1700000000, 0),
			}
			got := documentToDTO(d)
			if got.Source != src {
				t.Errorf("Source = %q, want %q", got.Source, src)
			}
		})
	}
}

func TestEmptyChunksResponse_DefaultsWhenTopKInvalid(t *testing.T) {
	r := emptyChunksResponse(0)
	if r.TopK != document.DefaultSemanticTopK {
		t.Errorf("TopK = %d, want default %d", r.TopK, document.DefaultSemanticTopK)
	}
	if r.Total != 0 || len(r.Items) != 0 {
		t.Errorf("empty response should have zero items, got total=%d items=%d", r.Total, len(r.Items))
	}
}

func TestEmptyChunksResponse_PreservesExplicitTopK(t *testing.T) {
	r := emptyChunksResponse(7)
	if r.TopK != 7 {
		t.Errorf("TopK = %d, want 7", r.TopK)
	}
}
