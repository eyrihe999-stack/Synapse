package service

import (
	"context"

	"github.com/eyrihe999-stack/Synapse/internal/common/eventbus"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
)

// pm 模块事件类型常量。channel 模块的 pm event consumer 按 event_type 分发处理。
//
// 字段约定(全部字符串):
//
//	event_type:         事件名,字符串
//	org_id:             所属 org id
//	project_id:         所属 project id(必填)
//	actor_user_id:      触发动作的 user id(为兼容 channel 老路径,部分场景为 0)
//	actor_principal_id: 触发动作的 principal id(可选)
//
// 不同 event_type 还有专属字段,见各发布点。
const (
	EventProjectCreated     = "project.created"
	EventProjectArchived    = "project.archived"
	EventInitiativeCreated  = "initiative.created"
	EventInitiativeArchived = "initiative.archived"
	EventVersionCreated     = "version.created"
	EventVersionUpdated     = "version.updated"
	EventVersionReleased    = "version.released"
	EventWorkstreamCreated  = "workstream.created"
	EventWorkstreamUpdated  = "workstream.updated"
)

// publishPMEvent XADD 到 synapse:pm:events,失败仅 warn(对齐 channel.publishChannelEvent
// 的 best-effort 语义)。publisher 或 streamKey 为零值时 no-op。
//
// 把 publisher / streamKey / log 参数化是为了让所有 sub-service 共用同一个 helper,
// 避免每个 service 都复制一份 publish 方法。
func publishPMEvent(
	ctx context.Context,
	publisher eventbus.Publisher,
	streamKey string,
	log logger.LoggerInterface,
	fields map[string]any,
) {
	if publisher == nil || streamKey == "" {
		return
	}
	id, err := publisher.Publish(ctx, streamKey, fields)
	if err != nil {
		log.WarnCtx(ctx, "pm: publish event failed", map[string]any{
			"event_type": fields["event_type"], "err": err.Error(),
		})
		return
	}
	log.DebugCtx(ctx, "pm: published event", map[string]any{
		"event_type": fields["event_type"], "stream_id": id,
	})
}
