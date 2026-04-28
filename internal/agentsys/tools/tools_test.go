package tools

import (
	"strings"
	"testing"

	"github.com/eyrihe999-stack/Synapse/internal/common/llm"
)

// TestSchema_NoScopeParams 静态断言:tool schema 不能含 org_id / channel_id 参数
// —— scope 由 ScopedServices 绑死,从 tool schema 泄露出去会让 LLM 误以为可跨 scope 操作。
func TestSchema_NoScopeParams(t *testing.T) {
	banned := []string{"org_id", "channel_id", "operating_org_id", "orgid", "channelid"}

	for _, tool := range Schema() {
		props, _ := tool.ParametersJSONSchema["properties"].(map[string]any)
		for propName := range props {
			for _, b := range banned {
				if strings.EqualFold(propName, b) {
					t.Errorf("tool %q exposes banned param %q", tool.Name, propName)
				}
			}
		}
	}
}

// TestSchema_AllToolsHaveProperties 预期每个 tool 都声明了 properties(即使空对象),
// 防止有人后面加 tool 时忘了填 schema 骨架。
func TestSchema_AllToolsHaveProperties(t *testing.T) {
	for _, tool := range Schema() {
		if _, ok := tool.ParametersJSONSchema["properties"]; !ok {
			t.Errorf("tool %q missing properties in schema", tool.Name)
		}
		if tool.ParametersJSONSchema["type"] != "object" {
			t.Errorf("tool %q schema type != object", tool.Name)
		}
	}
}

// TestIsErrorResult 验证 IsErrorResult 能正确区分 encodeOK / encodeError 输出。
func TestIsErrorResult(t *testing.T) {
	okJSON := encodeOK(map[string]any{"hello": "world"})
	if IsErrorResult(okJSON) {
		t.Errorf("IsErrorResult(ok) should be false, got true; raw=%s", okJSON)
	}
	errJSON := encodeError("something broke")
	if !IsErrorResult(errJSON) {
		t.Errorf("IsErrorResult(err) should be true, got false; raw=%s", errJSON)
	}
	// 不合法 JSON 也视为错误(防御性)
	if !IsErrorResult("not a json") {
		t.Error("IsErrorResult(garbage) should be true")
	}
}

// TestSchema_ExpectedToolNames 防回归:不小心把某个 tool 改名会被这里抓到。
// LLM 侧如果 tool 改名会让旧的 prompt 示例失效,需要刻意改。
func TestSchema_ExpectedToolNames(t *testing.T) {
	want := map[string]bool{
		ToolPostMessage:              true,
		ToolCreateTask:               true,
		ToolListRecentMessages:       true,
		ToolListChannelMembers:       true,
		ToolSearchKB:                 true,
		ToolGetKBDocument:            true,
		ToolCreateInitiative:         true,
		ToolCreateVersion:            true,
		ToolCreateWorkstream:         true,
		ToolSplitWorkstreamIntoTasks: true,
		ToolInviteToWorkstream:       true,
		ToolGetProjectRoadmap:        true,
	}
	for _, tool := range Schema() {
		if !want[tool.Name] {
			t.Errorf("unexpected tool %q (either rename intentionally + update test, or remove)", tool.Name)
		}
		delete(want, tool.Name)
	}
	if len(want) > 0 {
		t.Errorf("missing tools: %v", want)
	}
}

// 确保 llm 包引用不会因为重构丢失(保持 tools 与 llm 的依赖显式)。
var _ = llm.ToolDef{}
