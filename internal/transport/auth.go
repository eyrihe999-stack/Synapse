// auth.go transport 握手鉴权抽象。
//
// Authenticator 接口把"HTTP 升级阶段如何确认这个 agent 身份"从 transport 核心中抽出,
// V1 只实现 apikey 版本(放在 cmd 层或 internal/agents/ 下注入)。
// 未来增加 JWT / mTLS 时不动 transport 本身,换实现就行。
//
// 接口设计原则:
//   - 只接 HTTP 原生 *http.Request —— 传输层不应依赖 gin / 任何 web 框架
//   - 返回的 AgentMeta 就是整个连接生命周期内的身份快照,不再变更
//   - 校验失败统一返 transport.ErrAuthFailed(或其包装)
package transport

import (
	"context"
	"net/http"
	"time"
)

// AgentID agent 逻辑标识。全局唯一,agent 进程启动时从配置 / 环境变量读取。
// 同 agent_id 同时只允许一个活跃连接(V1 策略:后来者被拒)。
type AgentID string

// AgentMeta handshake 成功后的 agent 身份信息。Hub / 业务层通过此结构
// 判断权限、路由、on-behalf-of 等,不再回查 DB。
type AgentMeta struct {
	// AgentID 逻辑 agent 标识
	AgentID AgentID

	// AuthMode 鉴权模式,取值见 const.go 中 AuthModeAPIKey(当前唯一值)。
	AuthMode string

	// OrgID agent 所属组织(apikey 预注册时绑定);跨 org 调用由业务层校验
	OrgID uint64

	// OnBehalfOfUserID 对 kind=user 的 agent 非零,值为该 agent 所代表的 user id。
	// 每次 RPC 的 on-behalf-of 身份由此字段提供,业务按这个 user 做权限校验。
	// kind=system 的 agent 恒为 0。V1 尚未实装 kind=user,字段预留。
	OnBehalfOfUserID uint64

	// Capabilities handshake 时 agent 声明的能力列表(method 前缀)。
	// 目前 transport 不使用,仅透传给业务层做 capability registry;
	// 实际值由 handshake 阶段的 Hello 消息协商后由 Hub 回填。
	Capabilities []string

	// ConnectedAt 连接建立时刻。用于超时清理 / 监控。
	ConnectedAt time.Time
}

// Authenticator HTTP upgrade 前置鉴权。
//
// 实现职责:
//   1. 从 *http.Request 读 AgentID / 凭证 header
//   2. 校验凭证;失败返 ErrAuthFailed(或包装)
//   3. 成功返回填充好的 AgentMeta(AgentID / AuthMode / OrgID 必填,其余按实际)
//
// 禁止在此接口里做 DB 建档 / 使用统计等副作用 —— 纯校验,保持幂等。
// 失败 / 成功的审计日志由 Authenticator 自己写(上下文最清楚)。
type Authenticator interface {
	Authenticate(ctx context.Context, r *http.Request) (*AgentMeta, error)
}

// AuthenticatorFunc 适配器,便于用闭包实现 Authenticator(测试 / dev allow-all)。
type AuthenticatorFunc func(ctx context.Context, r *http.Request) (*AgentMeta, error)

// Authenticate 实现 Authenticator 接口。
func (f AuthenticatorFunc) Authenticate(ctx context.Context, r *http.Request) (*AgentMeta, error) {
	return f(ctx, r)
}
