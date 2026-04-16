# Admin 代码精简策略

> Admin 端点是内部管理工具，代码和测试标准均低于用户侧。本文件定义 Admin 代码的统一策略。

## 识别规则

以下任一条件成立即视为 Admin 代码：
- 路由含 `/admin/`
- handler/service 名称含 `Admin`
- 文件名含 `admin_`

## 代码侧

### 只保证 happy path

- Admin 代码只保证 happy path 正确，只处理大概率触发的竞态条件或数据一致性问题
- 其余防御性逻辑一律视为冗余
- 极端场景的完整清单见 [extreme-scenarios.md](extreme-scenarios.md)，Admin 代码中这些模式**无条件删除**

### 错误处理

- Admin service 层可以直接返回 `gorm.ErrRecordNotFound` 而不翻译为业务 sentinel，只要 handler 的 `handleAdminServiceError` 有兜底匹配即可
- Admin service 层不需要每个错误分支单独打日志，由 handler 层统一兜底

### 注释

- 导出函数仍需 godoc 注释，但内容可简化为 1-2 行
- 未导出函数和行内注释不强制
- 注释语言仍必须为中文

## 测试侧

### 只保留的测试类型

- Happy path
- 参数校验失败
- not found
- 重复创建（duplicate key）
- 状态机非法转换（如有状态机）

### 必须删除的测试类型

- DB 错误路径测试
- 边界值测试
- 并发竞态测试
- 返回值字段完整性测试
- 空结果集测试
- 幂等测试
- sentinel error 包装验证测试
