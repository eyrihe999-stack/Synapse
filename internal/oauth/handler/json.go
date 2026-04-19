// json.go 小工具:集中一处 JSON encode/decode 以便未来统一替换(如换成 jsoniter、
// 加 unknown field 拒绝等)。当前透传 encoding/json。
package handler

import "encoding/json"

func jsonUnmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
