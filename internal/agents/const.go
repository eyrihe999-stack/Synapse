// const.go agents 模块常量定义。
//
// 模块职责:agent 档案(agent_registry 表) + CRUD 管理 API + 为 transport 提供
// DBAuthenticator 替换 StaticAPIKeyAuthenticator。
//
// 边界:本模块不负责业务 method(如 kb.*)的处理 —— 那是 knowledge 模块的事。
// 本模块只管"agent 这个身份"本身及其凭证。
package agents

import "time"

// ─── 生成 key / agent_id 的随机字节长度 ──────────────────────────────────────

const (
	// AgentIDRandomBytes agent_id 随机部分的字节数(base64url 编码后约 22 字符)。
	// 16 字节 = 128bit 熵,足够避免冲突。
	AgentIDRandomBytes = 16

	// APIKeyRandomBytes apikey 随机部分的字节数(base64url 编码后约 43 字符)。
	// 32 字节 = 256bit 熵,防止暴力枚举。
	APIKeyRandomBytes = 32

	// AgentIDPrefix / APIKeyPrefix 可读前缀,方便在日志 / 配置里一眼区分类型。
	AgentIDPrefix = "agt_"
	APIKeyPrefix  = "sk_"
)

// ─── 默认值 ──────────────────────────────────────────────────────────────────

const (
	// ListDefaultLimit 列表接口默认每页条数。
	ListDefaultLimit = 50
	// ListMaxLimit 列表接口每页上限。
	ListMaxLimit = 200
)

// ─── 其它 ────────────────────────────────────────────────────────────────────

const (
	// LastSeenUpdateTimeout last_seen_at 同步更新的单次操作超时。
	LastSeenUpdateTimeout = 2 * time.Second
)

// ─── Agent Kind ───────────────────────────────────────────────────────────────
//
// Agent 分类维度。V1 只存在 system 一种,枚举保留是为将来无痛扩展:
//
//   - KindSystem:系统级 agent,代表服务 / 自动化流程接入。仅 owner/admin 创建,
//                apikey 鉴权,on-behalf-of=0(不代表任何 user)。
//   - KindUser  :user-scoped agent(未实装),代表某个 user 发起调用。成员可自建,
//                agent 行为权限以 row.owner_user_id 对应的 user 为准(→ AgentMeta.OnBehalfOfUserID)。
//                凭据同样是 apikey —— 不走 JWT,避免把 web session 生命周期耦合进来。
//
// 不同 kind 的创建权限 / 关联字段不同,加新类型时 Create / Authenticator 按 kind
// 分支即可,不改 wire 协议也不改 transport。

const (
	KindSystem = "system"
	KindUser   = "user"
)

// ─── 全局内嵌顶级系统 Agent ──────────────────────────────────────────────────
//
// Synapse 自带一个 all-org 共享的顶级系统 agent(协作 / 任务拆分 / @响应)。
// 它是 agents 表里的一行 **全局记录**(`org_id=0` sentinel),由 PR #4' migration
// seed,不是每 org 建一次。详见 docs/collaboration-design.md §3.3.1。

const (
	// TopOrchestratorAgentID 全局顶级 agent 的 agent_id(固定字面量,不走 agt_ 前缀)。
	// 跨 org 共享同一个 principal;所有新 channel 通过 auto_include_in_new_channels=TRUE
	// 自动把它加为 member。
	TopOrchestratorAgentID = "synapse-top-orchestrator"

	// TopOrchestratorDisplayName channel / 审计 UI 展示名。
	TopOrchestratorDisplayName = "Synapse"

	// ProjectArchitectAgentID 全局项目编排 agent。和 top-orchestrator 平级,
	// 但职责不同:负责"项目级编排"(分析需求、拆 initiative/workstream/task、
	// 组织成员)。和 top-orchestrator 跑在不同 LLM 上下文(不同 prompt + 不同
	// 工具集),由 PR-B 引入。
	//
	// 不开启 auto_include_in_new_channels —— Architect 只加入 Project Console
	// channel(由 pm 事件 consumer 在 project.created 时显式 INSERT channel_members),
	// 不进 workstream / regular channel,避免每条消息都给它发触发。
	ProjectArchitectAgentID = "synapse-project-architect"

	// ProjectArchitectDisplayName channel / 审计 UI 展示名。
	ProjectArchitectDisplayName = "Synapse Architect"

	// GlobalAgentOrgID 全局 agent 的 org_id sentinel 值。orgs.id 从 1 开始 autoincrement,
	// 0 永远不是合法 org;代码层按 0 判断"全局 scope",业务层不产生对 orgs(0) 的 JOIN。
	//
	// ⚠️ 查询护栏:任何"列 org X 可见的 agent"的 SQL 必须写
	//     WHERE org_id = <X> OR org_id = 0
	// —— 遗漏 `OR org_id = 0` 会让全局 agent 从结果里消失(auto-include hook /
	// @mention 路由等都会静默失效)。**禁止裸写 WHERE org_id = ?**,一律走
	// repository.ListAutoIncludeVisibleToOrg / 未来的 ListVisibleToOrg helper。
	// 长期若出现第二个全局 agent,再开独立 PR 把 org_id 切成 NULLABLE。
	GlobalAgentOrgID uint64 = 0
)

// IsValidKind 校验字符串是否是已知 kind。创建时 server 端硬写 system;
// 未来开放 kind 入参时此函数做白名单。
func IsValidKind(k string) bool {
	switch k {
	case KindSystem, KindUser:
		return true
	default:
		return false
	}
}
