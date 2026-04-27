// sse_handler.go GET /api/v2/users/me/events —— 把 channel 事件流通过 SSE 推给客户端。
//
// 当前 caller 两种典型:
//   - 用户本机 daemon(agent-bridge):订阅 "我被 @ 的 mention",fork claude 自动响应
//   - 浏览器(Synapse Web):订阅"我所属 channel 的所有活动",实时刷消息列表 / 时间线卡片
//
// 用 ?filter= 区分:
//   - filter=mentions(默认,daemon 用):
//       事件类型 = mention.received
//       条件 = event_type=message.posted ∧ mentioned_principal_ids 含 caller user.principal_id
//             ∧ author ≠ caller agent.principal_id(自指过滤,防 daemon 死循环)
//   - filter=channel_activity(浏览器用):
//       事件类型 = channel.activity
//       条件 = event_type=message.posted ∧ channel_id ∈ caller 所属 channel 集合
//             (包含自己发的消息 + system_event 卡片;不做自指过滤,Web 想看自己的消息)
//
// 实现要点:
//   - 每个 SSE 连接独立 XRead synapse:channel:events,起始 ID="$"(只看新事件)
//   - 不走 consumer group(无 PEL、无 XACK)—— SSE 断 = goroutine 退 = 自动清理
//   - 鉴权:支持 PAT/OAuth(BearerAuth) 或 JWT cookie(JWTAuthWithSession)二选一,
//     daemon 用前者、浏览器用后者
//
// 帧格式:
//
//	event: <event-name>
//	data: <json payload>
//	\n
//
// (心跳用 ":\n\n" SSE comment,client 不触发 onmessage,但保活 TCP)
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"

	commonmw "github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	oauthmw "github.com/eyrihe999-stack/Synapse/internal/oauth/middleware"
	usersvc "github.com/eyrihe999-stack/Synapse/internal/user/service"
)

// SSEUserLookup 反查 user 的 principal_id —— 用闭包/userService.GetProfile 适配,
// SSE handler 不直接 import user/service 包。
type SSEUserLookup func(ctx context.Context, userID uint64) (*usersvc.UserProfile, error)

// SSEChannelLookup 列 caller 所属 channel id 集 —— 用于 channel_activity filter 过滤。
// 实际实现走 channel.service.ListByPrincipal(返 model.Channel 列表),适配层只保留 id。
type SSEChannelLookup func(ctx context.Context, principalID uint64) ([]uint64, error)

// SSEHandler 持有 SSE endpoint 所需的全部依赖。
type SSEHandler struct {
	rdb           *redis.Client
	streamKey     string
	userLookup    SSEUserLookup
	channelLookup SSEChannelLookup
	log           logger.LoggerInterface
}

// NewSSEHandler 构造。streamKey 通常是 cfg.EventBus.ChannelStream("synapse:channel:events");
// rdb 复用全局 client。channelLookup 可以是 nil —— 此时 channel_activity filter 直接返 503。
func NewSSEHandler(
	rdb *redis.Client,
	streamKey string,
	userLookup SSEUserLookup,
	channelLookup SSEChannelLookup,
	log logger.LoggerInterface,
) *SSEHandler {
	return &SSEHandler{
		rdb:           rdb,
		streamKey:     streamKey,
		userLookup:    userLookup,
		channelLookup: channelLookup,
		log:           log,
	}
}

// HandleEvents GET /api/v2/users/me/events —— 升级 SSE 长连。
//
// 鉴权:同时接受 BearerAuth(PAT/OAuth)注入的身份 + JWTAuthWithSession 注入的 user_id;
// 装在外层路由的 BearerOrJWT middleware 已确保至少一种命中。
//
// 退出:client 断连(ctx done)/ Redis 永久不可用(目前只 warn 重试,不主动断连)。
func (h *SSEHandler) HandleEvents(c *gin.Context) {
	if h.rdb == nil || h.streamKey == "" {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"error": "events stream not configured",
		})
		return
	}

	userID, agentPrincipalID, ok := resolveCallerIdentity(c)
	if !ok {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	profile, err := h.userLookup(c.Request.Context(), userID)
	if err != nil || profile == nil {
		h.log.WarnCtx(c.Request.Context(), "sse: lookup user profile failed", map[string]any{
			"user_id": userID, "err": fmt.Sprint(err),
		})
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "lookup user failed"})
		return
	}
	if profile.PrincipalID == 0 {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "user has no principal"})
		return
	}
	myPrincipal := profile.PrincipalID
	myPrincipalStr := strconv.FormatUint(myPrincipal, 10)
	myAgentStr := strconv.FormatUint(agentPrincipalID, 10)

	// SSE 头。X-Accel-Buffering 给 nginx 看,关掉响应缓冲;Connection 让代理保持长连。
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)
	c.Writer.Flush()

	// 先发一帧 "ready",client 用 event 名识别"连上了"。
	if !writeSSEFrame(c.Writer, "ready", fmt.Sprintf(`{"user_id":%d,"principal_id":%d}`, userID, myPrincipal)) {
		return
	}

	filter := strings.ToLower(strings.TrimSpace(c.Query("filter")))
	if filter == "" {
		filter = "mentions" // 兼容旧 daemon 行为
	}

	h.log.InfoCtx(c.Request.Context(), "sse: stream opened", map[string]any{
		"user_id": userID, "principal_id": myPrincipal, "filter": filter,
	})

	switch filter {
	case "mentions":
		h.streamLoopMentions(c.Request.Context(), c.Writer, myPrincipalStr, myAgentStr)
	case "channel_activity":
		if h.channelLookup == nil {
			writeSSEFrame(c.Writer, "error", `{"message":"channel_activity not configured"}`)
			return
		}
		channelIDs, err := h.channelLookup(c.Request.Context(), myPrincipal)
		if err != nil {
			h.log.WarnCtx(c.Request.Context(), "sse: list channels failed", map[string]any{
				"principal_id": myPrincipal, "err": err.Error(),
			})
			writeSSEFrame(c.Writer, "error", `{"message":"list channels failed"}`)
			return
		}
		// channels set 启动时一次性拉到;中途加入新 channel 需要重连才能看到。
		// MVP 简化:用户切页通常会刷新连接,影响小。
		channelSet := make(map[string]struct{}, len(channelIDs))
		for _, id := range channelIDs {
			channelSet[strconv.FormatUint(id, 10)] = struct{}{}
		}
		h.streamLoopChannelActivity(c.Request.Context(), c.Writer, channelSet)
	default:
		writeSSEFrame(c.Writer, "error", fmt.Sprintf(`{"message":"unknown filter: %s"}`, filter))
	}

	h.log.InfoCtx(c.Request.Context(), "sse: stream closed", map[string]any{
		"user_id": userID, "filter": filter,
	})
}

// resolveCallerIdentity 兼容两种鉴权链路:
//   - oauthmw.BearerAuth 注入的 (UserID, AgentPrincipalID) —— PAT/OAuth caller(daemon 等)
//   - commonmw.JWTAuthWithSession 注入的 user_id —— 浏览器 cookie caller(Web)
//
// JWT 路径下没有 agent_principal,返 0 —— channel_activity filter 不依赖它,
// mentions filter 自指过滤会因 0 而永不命中(可接受,Web 不会用 mentions filter)。
func resolveCallerIdentity(c *gin.Context) (userID, agentPrincipalID uint64, ok bool) {
	if a, hit := oauthmw.GetAuth(c); hit && a.UserID != 0 {
		return a.UserID, a.AgentPrincipalID, true
	}
	if uid, hit := commonmw.GetUserID(c); hit && uid != 0 {
		return uid, 0, true
	}
	return 0, 0, false
}

// ─── streamLoop:mentions(daemon 用)────────────────────────────────────────

func (h *SSEHandler) streamLoopMentions(ctx context.Context, w http.ResponseWriter, myPrincipalStr, myAgentStr string) {
	h.streamLoopGeneric(ctx, w, "mention.received", func(m redis.XMessage) (string, bool) {
		return buildMentionPayload(m, myPrincipalStr, myAgentStr)
	})
}

// buildMentionPayload 过滤规则:
//   - event_type=message.posted
//   - author ≠ myAgentStr(防自指死循环)
//   - mentioned_principal_ids 含 myPrincipalStr
func buildMentionPayload(m redis.XMessage, myPrincipalStr, myAgentStr string) (string, bool) {
	eventType, _ := m.Values["event_type"].(string)
	if eventType != "message.posted" {
		return "", false
	}
	author, _ := m.Values["author_principal_id"].(string)
	if myAgentStr != "" && myAgentStr != "0" && author == myAgentStr {
		return "", false
	}
	mentionsRaw, _ := m.Values["mentioned_principal_ids"].(string)
	if mentionsRaw == "" {
		return "", false
	}
	hit := false
	for _, p := range strings.Split(mentionsRaw, ",") {
		if p == myPrincipalStr {
			hit = true
			break
		}
	}
	if !hit {
		return "", false
	}
	return marshalEventPayload(m), true
}

// ─── streamLoop:channel_activity(浏览器用)─────────────────────────────

func (h *SSEHandler) streamLoopChannelActivity(ctx context.Context, w http.ResponseWriter, channelSet map[string]struct{}) {
	h.streamLoopGeneric(ctx, w, "channel.activity", func(m redis.XMessage) (string, bool) {
		return buildChannelActivityPayload(m, channelSet)
	})
}

// buildChannelActivityPayload 过滤规则:
//   - event_type=message.posted(包含 kind=text 用户消息 + kind=system_event 卡片)
//   - channel_id 在 caller 所属 channel 集合内
//   - 不做自指 / 不做 mentions 过滤(Web 要看所有人发的所有消息,包括自己)
func buildChannelActivityPayload(m redis.XMessage, channelSet map[string]struct{}) (string, bool) {
	eventType, _ := m.Values["event_type"].(string)
	if eventType != "message.posted" {
		return "", false
	}
	channelID, _ := m.Values["channel_id"].(string)
	if channelID == "" {
		return "", false
	}
	if _, ok := channelSet[channelID]; !ok {
		return "", false
	}
	return marshalEventPayload(m), true
}

// ─── streamLoop 通用骨架 ─────────────────────────────────────────────────

// streamLoopGeneric XRead 循环骨架。filter 闭包决定是否推 + payload 内容;
// eventName 就是 SSE 帧的 event: 字段。
func (h *SSEHandler) streamLoopGeneric(
	ctx context.Context,
	w http.ResponseWriter,
	eventName string,
	filter func(redis.XMessage) (payload string, push bool),
) {
	const (
		blockTimeout = 5 * time.Second
		errBackoff   = 1 * time.Second
		batchSize    = 32
	)
	lastID := "$"

	for {
		if ctx.Err() != nil {
			return
		}

		streams, err := h.rdb.XRead(ctx, &redis.XReadArgs{
			Streams: []string{h.streamKey, lastID},
			Count:   batchSize,
			Block:   blockTimeout,
		}).Result()

		switch {
		case errors.Is(err, redis.Nil):
			if !writeSSEHeartbeat(w) {
				return
			}
			continue
		case err != nil:
			if ctx.Err() != nil {
				return
			}
			h.log.WarnCtx(ctx, "sse: xread failed", map[string]any{"err": err.Error()})
			select {
			case <-ctx.Done():
				return
			case <-time.After(errBackoff):
			}
			continue
		}

		for _, s := range streams {
			for _, m := range s.Messages {
				lastID = m.ID
				payload, ok := filter(m)
				if !ok {
					continue
				}
				if !writeSSEFrame(w, eventName, payload) {
					return
				}
			}
		}
	}
}

// ─── payload 拼装 + SSE 帧写出 ─────────────────────────────────────────

// marshalEventPayload 平铺 stream 关键字段成 JSON。两个 filter 都能用同一份 shape ——
// 字段一致便于客户端复用 type;不必要字段(message_id 在 channel.archived 事件里没意义)就返空串。
func marshalEventPayload(m redis.XMessage) string {
	out := map[string]string{
		"event_id":            m.ID,
		"event_type":          stringField(m.Values, "event_type"),
		"channel_id":          stringField(m.Values, "channel_id"),
		"message_id":          stringField(m.Values, "message_id"),
		"author_principal_id": stringField(m.Values, "author_principal_id"),
		"org_id":              stringField(m.Values, "org_id"),
		"kind":                stringField(m.Values, "kind"),
		"created_at":          stringField(m.Values, "created_at"),
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func stringField(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// writeSSEFrame 写一帧 SSE 事件。返 false 表示连接已断,调用方退循环。
func writeSSEFrame(w http.ResponseWriter, event, data string) bool {
	if _, err := io.WriteString(w, fmt.Sprintf("event: %s\ndata: %s\n\n", event, data)); err != nil {
		return false
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		return false
	}
	flusher.Flush()
	return true
}

// writeSSEHeartbeat 写 SSE comment 心跳。client 不触发 onmessage,但保活 TCP。
func writeSSEHeartbeat(w http.ResponseWriter) bool {
	if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
		return false
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		return false
	}
	flusher.Flush()
	return true
}
