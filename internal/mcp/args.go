package mcp

import (
	"github.com/mark3labs/mcp-go/mcp"
)

// args.go 精简 helper:从 mcp.CallToolRequest 里拎参数。
// mcp-go 原生的 GetArguments() 返回 map[string]any,需要自己做类型断言 + 默认值。

// intArg 拿整数参数;缺省 / 类型错返 def。
func intArg(req mcp.CallToolRequest, name string, def int) int {
	v, ok := req.GetArguments()[name]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	default:
		return def
	}
}

// uint64Arg 拿 uint64;缺省 / 类型错返 def。
func uint64Arg(req mcp.CallToolRequest, name string, def uint64) uint64 {
	v, ok := req.GetArguments()[name]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		if n < 0 {
			return def
		}
		return uint64(n)
	case int:
		if n < 0 {
			return def
		}
		return uint64(n)
	case int64:
		if n < 0 {
			return def
		}
		return uint64(n)
	default:
		return def
	}
}

// boolArg 拿 bool;缺省 / 类型错返 def。
func boolArg(req mcp.CallToolRequest, name string, def bool) bool {
	v, ok := req.GetArguments()[name]
	if !ok {
		return def
	}
	b, ok := v.(bool)
	if !ok {
		return def
	}
	return b
}

// stringArg 拿 string;缺省 / 类型错返 def。
func stringArg(req mcp.CallToolRequest, name string, def string) string {
	v, ok := req.GetArguments()[name]
	if !ok {
		return def
	}
	s, ok := v.(string)
	if !ok {
		return def
	}
	return s
}

// uint64ArrayArg 拿 []uint64;mcp-go 的 array 参数会是 []any。
func uint64ArrayArg(req mcp.CallToolRequest, name string) []uint64 {
	v, ok := req.GetArguments()[name]
	if !ok {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]uint64, 0, len(arr))
	for _, it := range arr {
		switch n := it.(type) {
		case float64:
			if n >= 0 {
				out = append(out, uint64(n))
			}
		case int:
			if n >= 0 {
				out = append(out, uint64(n))
			}
		case int64:
			if n >= 0 {
				out = append(out, uint64(n))
			}
		}
	}
	return out
}

// stringArrayArg 拿 []string。
func stringArrayArg(req mcp.CallToolRequest, name string) []string {
	v, ok := req.GetArguments()[name]
	if !ok {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, it := range arr {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
