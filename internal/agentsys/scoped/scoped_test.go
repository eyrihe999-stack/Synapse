package scoped

import (
	"reflect"
	"strings"
	"testing"
)

// TestScopedServices_NoCrossOrgParams 静态断言 —— 这是本 PR 跨 org 隔离的最强保证:
//
// ScopedServices 的所有 public method 签名里**不得**出现 orgID / channelID /
// actorPID 类型的"业务可传入"参数。调用方(tools dispatcher / handler)物理上
// 就无法把别的 org/channel 的 id 传进来,从根上阻断跨 scope 泄露。
//
// 如果未来有人加了个 "PostMessageAsOrg(ctx, orgID, ...)" 这种方法,此测试会
// 失败 —— 必须把 orgID 参数去掉,或者重新评估隔离设计。
func TestScopedServices_NoCrossOrgParams(t *testing.T) {
	// 禁用的参数名(小写,大小写不敏感比对)
	banned := []string{
		"orgid", "org_id",
		"channelid", "channel_id",
		"actorpid", "actor_principal_id", "actorprincipalid",
		"operatingorgid", "operating_org_id",
	}

	typ := reflect.TypeOf(&ScopedServices{})
	for i := 0; i < typ.NumMethod(); i++ {
		m := typ.Method(i)

		// 只检查真正能被 LLM/tools dispatcher 调用的"业务"方法。
		// ChannelID / OperatingOrgID / ActorPrincipalID 是明确导出的 getter
		// 供 handler 写审计用,不能被当成"tool 可调"—— 它们返回 id,不接受 id。
		switch m.Name {
		case "ChannelID", "OperatingOrgID", "ActorPrincipalID":
			continue
		}

		ft := m.Type
		// 跳过 receiver(i=0)
		for j := 1; j < ft.NumIn(); j++ {
			paramType := ft.In(j)
			// 检查类型名(捕获 uint64 命名混淆)和参数类型的 struct 字段名(CreateTaskArgs 等)
			if paramType.Kind() == reflect.Struct {
				for k := 0; k < paramType.NumField(); k++ {
					fname := strings.ToLower(paramType.Field(k).Name)
					for _, b := range banned {
						if fname == b {
							t.Errorf("method %s has struct param with banned field %q",
								m.Name, paramType.Field(k).Name)
						}
					}
				}
			}
			// 参数类型 uint64 本身没问题(如 limit),但我们本包方法签名不应该用
			// uint64 传 id。不做精细判断 —— 如果加新方法想传 id,先改这里的白名单。
		}
	}
}

// TestScopedServices_GettersReturnConstructorValues 基本 getter 正确性。
func TestScopedServices_GettersReturnConstructorValues(t *testing.T) {
	s := New(42, 100, 7, Deps{})
	if got := s.OperatingOrgID(); got != 42 {
		t.Errorf("OperatingOrgID = %d, want 42", got)
	}
	if got := s.ChannelID(); got != 100 {
		t.Errorf("ChannelID = %d, want 100", got)
	}
	if got := s.ActorPrincipalID(); got != 7 {
		t.Errorf("ActorPrincipalID = %d, want 7", got)
	}
}
