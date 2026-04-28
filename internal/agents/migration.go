// Package agents agent 注册管理 + DBAuthenticator。
// 详见 internal/agents/const.go。
package agents

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/agents/model"
	"github.com/eyrihe999-stack/Synapse/internal/common/database"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/principal"
	principalmodel "github.com/eyrihe999-stack/Synapse/internal/principal/model"
)

// RunMigrations 执行 agents 模块数据库迁移。
//
// 步骤:
//  1. (一次性) RENAME TABLE agent_registry TO agents —— 若老表存在且新表不存在
//  2. AutoMigrate(&Agent{}) 增量建表 / 加 principal_id 列
//  3. backfillAgentPrincipals:为 principal_id=0 的存量 agent 建 principal 并回写
//  4. EnsureIndex uk_agents_principal —— 在全部非 0 之后建唯一索引
//
// 幂等性:
//   - RENAME 有存在性检查,二次启动跳过
//   - AutoMigrate / backfill / EnsureIndex 本身幂等
//
// 必须在 principal.RunMigrations 之后调用。
func RunMigrations(ctx context.Context, db *gorm.DB, log logger.LoggerInterface, onReady func()) error {
	log.InfoCtx(ctx, "agents: running MySQL migrations", nil)

	if err := renameAgentRegistryToAgents(ctx, db, log); err != nil {
		return fmt.Errorf("agents rename: %w", err)
	}

	if err := db.WithContext(ctx).AutoMigrate(&model.Agent{}); err != nil {
		return fmt.Errorf("agents auto-migrate: %w", err)
	}

	if err := backfillAgentPrincipals(ctx, db, log); err != nil {
		return fmt.Errorf("agents backfill principals: %w", err)
	}

	if err := database.EnsureIndex(db, database.IndexSpec{
		Table:   "agents",
		Name:    "uk_agents_principal",
		Columns: []string{"principal_id"},
		Unique:  true,
	}); err != nil {
		log.ErrorCtx(ctx, "agents EnsureIndex uk_agents_principal 失败", err, nil)
		return fmt.Errorf("ensure agents principal unique index: %w", err)
	}

	if err := seedTopOrchestrator(ctx, db, log); err != nil {
		return fmt.Errorf("agents seed top orchestrator: %w", err)
	}

	if err := seedProjectArchitect(ctx, db, log); err != nil {
		return fmt.Errorf("agents seed project architect: %w", err)
	}

	log.InfoCtx(ctx, "agents: migrations completed", nil)
	if onReady != nil {
		onReady()
	}
	return nil
}

// seedTopOrchestrator 幂等种入全局顶级系统 agent。
//
// 这是 Synapse 产品的一部分,所有 org 共享:
//
//	agent_id = TopOrchestratorAgentID (固定)
//	org_id   = GlobalAgentOrgID (0, sentinel)
//	kind     = 'system'
//	auto_include_in_new_channels = true
//	owner_user_id = NULL (系统 agent 无归属 user)
//
// 幂等性靠 agents.agent_id UNIQUE 约束 + 存在性先查。二次启动跳过。
//
// APIKey 生成但通常不用 —— 顶级 agent 由 Synapse 进程内直接调 service,不走
// MCP handshake;这里存一个随机值满足 NOT NULL 约束,也为未来可能的自检通路留退路。
func seedTopOrchestrator(ctx context.Context, db *gorm.DB, log logger.LoggerInterface) error {
	// 先查是否已存在。用 agent_id 唯一索引快速定位。
	var existing model.Agent
	err := db.WithContext(ctx).
		Where("agent_id = ?", TopOrchestratorAgentID).
		Take(&existing).Error
	if err == nil {
		// 已存在 —— 幂等跳过。
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("check top orchestrator existence: %w", err)
	}

	apikey, err := genSeedAPIKey()
	if err != nil {
		return fmt.Errorf("generate seed apikey: %w", err)
	}

	// INSERT 依赖 Agent.BeforeCreate hook 自动建 principal + 回填 PrincipalID。
	// 走 DB 事务以保证 "principal 建好 + agent 插入" 原子。
	top := &model.Agent{
		AgentID:                  TopOrchestratorAgentID,
		OrgID:                    GlobalAgentOrgID,
		Kind:                     KindSystem,
		AutoIncludeInNewChannels: true,
		APIKey:                   apikey,
		DisplayName:              TopOrchestratorDisplayName,
		Enabled:                  true,
		CreatedByUID:             0, // 系统 seed,无人类创建者
	}
	if err := db.WithContext(ctx).Create(top).Error; err != nil {
		// 如果另一个进程正好并发 seed 撞 UNIQUE,回来再查一次拿现有行
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil
		}
		// MySQL driver 可能返回别的 error type,简单再查一次兜底
		var again model.Agent
		if err2 := db.WithContext(ctx).Where("agent_id = ?", TopOrchestratorAgentID).Take(&again).Error; err2 == nil {
			return nil
		}
		return fmt.Errorf("insert top orchestrator: %w", err)
	}
	log.InfoCtx(ctx, "agents: seeded top orchestrator", map[string]any{
		"agent_id":     top.AgentID,
		"principal_id": top.PrincipalID,
	})
	return nil
}

// seedProjectArchitect 幂等种入全局项目编排 agent(PR-B)。
//
//	agent_id = ProjectArchitectAgentID (固定)
//	org_id   = GlobalAgentOrgID (0, sentinel,与 top-orchestrator 共用全局 scope)
//	kind     = 'system'
//	auto_include_in_new_channels = false (决策 4:只加 Console,不进所有 channel)
//	owner_user_id = NULL (系统 agent 无归属 user)
//
// 加 channel 的路径:pm 事件 consumer 在 project.created 时显式 INSERT
// channel_members 把 Architect 加进 Console channel。
//
// 幂等性靠 agents.agent_id UNIQUE 约束 + 存在性先查。二次启动跳过。
func seedProjectArchitect(ctx context.Context, db *gorm.DB, log logger.LoggerInterface) error {
	var existing model.Agent
	err := db.WithContext(ctx).
		Where("agent_id = ?", ProjectArchitectAgentID).
		Take(&existing).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("check project architect existence: %w", err)
	}

	apikey, err := genSeedAPIKey()
	if err != nil {
		return fmt.Errorf("generate seed apikey: %w", err)
	}

	architect := &model.Agent{
		AgentID:                  ProjectArchitectAgentID,
		OrgID:                    GlobalAgentOrgID,
		Kind:                     KindSystem,
		AutoIncludeInNewChannels: false, // 决策 4:不自动加所有 channel
		APIKey:                   apikey,
		DisplayName:              ProjectArchitectDisplayName,
		Enabled:                  true,
		CreatedByUID:             0,
	}
	if err := db.WithContext(ctx).Create(architect).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil
		}
		var again model.Agent
		if err2 := db.WithContext(ctx).Where("agent_id = ?", ProjectArchitectAgentID).Take(&again).Error; err2 == nil {
			return nil
		}
		return fmt.Errorf("insert project architect: %w", err)
	}
	log.InfoCtx(ctx, "agents: seeded project architect", map[string]any{
		"agent_id":     architect.AgentID,
		"principal_id": architect.PrincipalID,
	})
	return nil
}

// genSeedAPIKey 生成 seed 用 apikey(sk_<base64url>,32 字节熵)。
// 和 service.genAPIKey 独立是为了避免 migration 反向依赖 service 层。
func genSeedAPIKey() (string, error) {
	buf := make([]byte, APIKeyRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return APIKeyPrefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// renameAgentRegistryToAgents 一次性把老表 agent_registry 重命名为 agents。
//
// 安全性:只在"老表存在 且 新表不存在"时执行;其他三种组合(都不存在 / 只有新表 / 都存在)
// 都是合法状态,直接返回 nil(AutoMigrate 会接管后续列变更)。
func renameAgentRegistryToAgents(ctx context.Context, db *gorm.DB, log logger.LoggerInterface) error {
	oldExists, err := tableExists(db, "agent_registry")
	if err != nil {
		return fmt.Errorf("check agent_registry: %w", err)
	}
	newExists, err := tableExists(db, "agents")
	if err != nil {
		return fmt.Errorf("check agents: %w", err)
	}

	switch {
	case !oldExists && !newExists:
		// 全新环境 —— AutoMigrate 会按 TableName() "agents" 建新表
		return nil
	case !oldExists && newExists:
		// 已迁移过 —— noop
		return nil
	case oldExists && newExists:
		// 异常:两张都在。不做破坏性操作,抛错让人工介入
		return errors.New("both agent_registry and agents exist — manual cleanup required")
	case oldExists && !newExists:
		// 正常迁移路径
		log.InfoCtx(ctx, "agents: renaming agent_registry -> agents", nil)
		if err := db.WithContext(ctx).Exec("RENAME TABLE `agent_registry` TO `agents`").Error; err != nil {
			return fmt.Errorf("rename table: %w", err)
		}
		return nil
	}
	return nil
}

// tableExists 通过 information_schema 判断表是否存在于当前 schema。
func tableExists(db *gorm.DB, name string) (bool, error) {
	var n int
	err := db.Raw(
		"SELECT 1 FROM information_schema.TABLES WHERE table_schema = DATABASE() AND table_name = ? LIMIT 1",
		name,
	).Scan(&n).Error
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// backfillAgentPrincipals 为 principal_id=0 的存量 agent 建 principal 并回写。
//
// 分页扫描 + 逐行事务(insert principal + update agent);重启可续。
func backfillAgentPrincipals(ctx context.Context, db *gorm.DB, log logger.LoggerInterface) error {
	const batchSize = 500
	backfilled := 0
	for {
		var agents []model.Agent
		if err := db.WithContext(ctx).
			Where("principal_id = ?", 0).
			Order("id ASC").
			Limit(batchSize).
			Find(&agents).Error; err != nil {
			return fmt.Errorf("scan agents to backfill: %w", err)
		}
		if len(agents) == 0 {
			break
		}
		for _, a := range agents {
			if err := backfillOneAgent(ctx, db, &a); err != nil {
				log.ErrorCtx(ctx, "agent principal backfill 单行失败", err, map[string]any{"agent_id": a.AgentID})
				return err
			}
			backfilled++
		}
	}
	if backfilled > 0 {
		log.InfoCtx(ctx, "agent principal backfill 完成", map[string]any{"backfilled": backfilled})
	}
	return nil
}

func backfillOneAgent(ctx context.Context, db *gorm.DB, a *model.Agent) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		status := principalmodel.StatusActive
		if !a.Enabled {
			status = principalmodel.StatusDisabled
		}
		p := &principalmodel.Principal{
			Kind:        principalmodel.KindAgent,
			DisplayName: a.DisplayName,
			Status:      status,
		}
		if err := principal.Create(ctx, tx, p); err != nil {
			return err
		}
		res := tx.Model(&model.Agent{}).
			Where("id = ? AND principal_id = ?", a.ID, 0).
			Update("principal_id", p.ID)
		if res.Error != nil {
			return fmt.Errorf("update agent.principal_id: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return errors.New("agent already backfilled by another worker")
		}
		return nil
	})
}
