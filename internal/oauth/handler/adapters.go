// adapters.go 把 oauth handler 对 user / organization 的依赖抽象成接口。
//
// 和 document 模块的 OrgPort 同模式:oauth 只关心"用户能否登录"和"用户有哪些 org",
// 不直接 import user / organization 的 service —— 未来换实现(比如联邦身份)零改动。
package handler

import "context"

// LoginAdapter /oauth/login 提交时调用。包装现有 user service 的凭证校验。
// 成功返 userID;失败返 error(handler 层不区分具体原因,统一显示"登录失败")。
type LoginAdapter interface {
	VerifyCredentials(ctx context.Context, email, password string) (userID uint64, err error)
}

// OrgAdapter consent 页列 org 选项 + 提交时校验用户对 org 的成员资格。
type OrgAdapter interface {
	// ListUserOrgs 按用户 ID 列他所属的活跃 org。空结果对应用户没任何 org 的边界情况。
	ListUserOrgs(ctx context.Context, userID uint64) ([]OrgSummary, error)

	// IsMember 校验 userID 是否是 orgID 的活跃成员。
	// 防"用户在 consent 页篡改 hidden org_id 指向自己没有权限的 org"。
	IsMember(ctx context.Context, userID, orgID uint64) (bool, error)
}

// OrgSummary consent 页下拉选项用的最小信息。
type OrgSummary struct {
	ID          uint64
	Slug        string
	DisplayName string
}
