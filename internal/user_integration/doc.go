// Package user_integration 持久化"用户 × 第三方平台 × 外部账号"的 OAuth / PAT 凭据。
//
// 当前形态(2026-04-21):**只有持久层**。
//
//   - const.go / errors.go       模块常量 + sentinel 错误
//   - migration.go               表 DDL + 索引
//   - model/integration.go       gorm 映射
//   - repository/                CRUD 接口 + gorm 实现
//
// **刻意缺失**的 handler/ + service/ 子目录:两者将随第一个 OAuth sync provider
// (Layer 4+ 飞书 / Notion / GitLab 等)一起落地,届时会有:
//
//   - service/:token 刷新 / provider OAuth 客户端 / OAuth state 生成校验
//   - handler/:GET /me/integrations、POST /me/integrations/:provider/connect、
//     OAuth 回调端点、DELETE 断开连接
//
// 现阶段保持持久层单独可用,让业务模块(document sync runner)能提前依赖凭据 schema。
// sayso-lint 的 required-file(handler/、service/)告警是**有意保留**的信号,
// 提醒维护者"本模块还差一层"。上 provider sync 时删除此段说明、补齐子目录。
package user_integration
