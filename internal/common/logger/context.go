package logger

import "context"

// contextKey is a local type for log context keys.
// 后续接入 OpenTelemetry 时,addTraceContext 可在这些 key 之外再读 OTel span/trace id
// 并覆盖 trace_id 字段;middleware 无需改动。
type contextKey string

const (
	RequestIDKey contextKey = "request_id"
	SessionIDKey contextKey = "session_id"
	UserIDKey    contextKey = "user_id"
)

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, RequestIDKey, id)
}

func GetRequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(RequestIDKey).(string); ok {
		return id
	}
	return ""
}

func WithSessionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, SessionIDKey, id)
}

func GetSessionID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(SessionIDKey).(string); ok {
		return id
	}
	return ""
}

func WithUserID(ctx context.Context, id uint64) context.Context {
	return context.WithValue(ctx, UserIDKey, id)
}

func GetUserID(ctx context.Context) uint64 {
	if ctx == nil {
		return 0
	}
	if id, ok := ctx.Value(UserIDKey).(uint64); ok {
		return id
	}
	return 0
}
