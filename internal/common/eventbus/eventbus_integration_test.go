//go:build integration

// 跑法:go test -tags=integration ./internal/common/eventbus -v
// 前提:本地 Redis 在 127.0.0.1:6379(docker-compose 起的 synapse-redis 即可)。
package eventbus_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"

	"github.com/eyrihe999-stack/Synapse/internal/common/eventbus"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
)

const (
	redisAddr = "127.0.0.1:6379"
	redisDB   = 15 // 用相对冷门的 DB 编号,避免撞到真实业务数据
)

func testClient(t *testing.T) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{
		Addr: redisAddr,
		DB:   redisDB,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Skipf("skipping: redis not reachable at %s: %v", redisAddr, err)
	}
	return client
}

// uniqueStream 每个 case 用独立 stream key,避免互相干扰。
func uniqueStream(t *testing.T) string {
	t.Helper()
	//nolint:gosec // 测试用随机数
	return fmt.Sprintf("test:eventbus:%d:%d", time.Now().UnixNano(), rand.Int63())
}

func cleanup(t *testing.T, client *redis.Client, stream string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client.Del(ctx, stream)
}

// TestPublishConsumeAck 正常路径:Publish 3 条 → Consume 全部处理 → XACK 后 PEL 空。
func TestPublishConsumeAck(t *testing.T) {
	client := testClient(t)
	defer client.Close()
	log := logger.NewSimpleLogger()
	stream := uniqueStream(t)
	defer cleanup(t, client, stream)

	pub := eventbus.NewRedisPublisher(client, 1000)
	cg := eventbus.NewRedisConsumerGroup(client, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	group := "test-group"
	consumer := "test-consumer"

	if err := cg.EnsureGroup(ctx, stream, group); err != nil {
		t.Fatalf("ensure group: %v", err)
	}

	// 发 3 条
	for i := 0; i < 3; i++ {
		if _, err := pub.Publish(ctx, stream, map[string]any{
			"seq":     strconv.Itoa(i),
			"payload": fmt.Sprintf("hello-%d", i),
		}); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	var received int32
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = cg.Consume(ctx, stream, group, consumer, func(_ context.Context, msg eventbus.Message) error {
			if msg.Fields["payload"] == "" {
				t.Errorf("unexpected empty payload: %+v", msg.Fields)
			}
			atomic.AddInt32(&received, 1)
			return nil
		})
	}()

	// 等 3 条都收到
	if !waitFor(5*time.Second, func() bool { return atomic.LoadInt32(&received) == 3 }) {
		t.Fatalf("expected 3 messages received, got %d", atomic.LoadInt32(&received))
	}
	cancel()
	wg.Wait()

	// 确认 PEL 里没有残留(全都 XACK 了)
	pending, err := client.XPending(context.Background(), stream, group).Result()
	if err != nil {
		t.Fatalf("xpending: %v", err)
	}
	if pending.Count != 0 {
		t.Fatalf("expected empty PEL, got %d pending", pending.Count)
	}
}

// TestEnsureGroupIdempotent 重复 EnsureGroup 不应报错(BUSYGROUP 静默)。
func TestEnsureGroupIdempotent(t *testing.T) {
	client := testClient(t)
	defer client.Close()
	log := logger.NewSimpleLogger()
	stream := uniqueStream(t)
	defer cleanup(t, client, stream)

	cg := eventbus.NewRedisConsumerGroup(client, log)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := cg.EnsureGroup(ctx, stream, "g1"); err != nil {
			t.Fatalf("ensure group iter %d: %v", i, err)
		}
	}
}

// TestPELReplayOnRestart handler 故意不 ACK → 关闭 consumer → 用相同 consumer 名重开,
// 0-0 阶段应能重新拿到所有未 ACK 事件。验证断点续传语义。
func TestPELReplayOnRestart(t *testing.T) {
	client := testClient(t)
	defer client.Close()
	log := logger.NewSimpleLogger()
	stream := uniqueStream(t)
	defer cleanup(t, client, stream)

	pub := eventbus.NewRedisPublisher(client, 1000)
	cg := eventbus.NewRedisConsumerGroup(client, log)

	group := "test-group"
	consumer := "stable-consumer-name" // 关键:两次 Consume 用同名

	if err := cg.EnsureGroup(context.Background(), stream, group); err != nil {
		t.Fatalf("ensure group: %v", err)
	}

	// 发 3 条
	for i := 0; i < 3; i++ {
		if _, err := pub.Publish(context.Background(), stream, map[string]any{
			"seq": strconv.Itoa(i),
		}); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	// 第 1 轮消费:handler 全部返 error 不 ACK
	var firstPass int32
	ctx1, cancel1 := context.WithCancel(context.Background())
	var wg1 sync.WaitGroup
	wg1.Add(1)
	go func() {
		defer wg1.Done()
		_ = cg.Consume(ctx1, stream, group, consumer, func(_ context.Context, _ eventbus.Message) error {
			atomic.AddInt32(&firstPass, 1)
			return errors.New("deliberate failure: leave in PEL")
		})
	}()
	// 等 3 条都处理过
	if !waitFor(5*time.Second, func() bool { return atomic.LoadInt32(&firstPass) >= 3 }) {
		t.Fatalf("first pass expected 3 handled, got %d", atomic.LoadInt32(&firstPass))
	}
	cancel1()
	wg1.Wait()

	// PEL 里应该还有 3 条
	pending, err := client.XPending(context.Background(), stream, group).Result()
	if err != nil {
		t.Fatalf("xpending: %v", err)
	}
	if pending.Count != 3 {
		t.Fatalf("expected 3 in PEL after first pass, got %d", pending.Count)
	}

	// 第 2 轮:用相同 consumer 名重开,PEL 重放阶段应拿回这 3 条并成功 ACK
	var secondPass int32
	ctx2, cancel2 := context.WithCancel(context.Background())
	var wg2 sync.WaitGroup
	wg2.Add(1)
	go func() {
		defer wg2.Done()
		_ = cg.Consume(ctx2, stream, group, consumer, func(_ context.Context, _ eventbus.Message) error {
			atomic.AddInt32(&secondPass, 1)
			return nil
		})
	}()
	if !waitFor(5*time.Second, func() bool { return atomic.LoadInt32(&secondPass) == 3 }) {
		t.Fatalf("second pass expected 3 replayed, got %d", atomic.LoadInt32(&secondPass))
	}
	cancel2()
	wg2.Wait()

	// PEL 此时应为空
	pending2, err := client.XPending(context.Background(), stream, group).Result()
	if err != nil {
		t.Fatalf("xpending after replay: %v", err)
	}
	if pending2.Count != 0 {
		t.Fatalf("expected empty PEL after replay, got %d", pending2.Count)
	}
}

// TestPublishMaxLenTrim MAXLEN 近似裁剪不爆内存。发 N 条,XLEN 应不超过 N(可能少于,
// 因为近似裁剪容许略超再压回;此测试只验证"不是线性暴涨")。
func TestPublishMaxLenTrim(t *testing.T) {
	client := testClient(t)
	defer client.Close()
	stream := uniqueStream(t)
	defer cleanup(t, client, stream)

	const maxLen = 50
	const publishN = 500
	pub := eventbus.NewRedisPublisher(client, maxLen)

	ctx := context.Background()
	for i := 0; i < publishN; i++ {
		if _, err := pub.Publish(ctx, stream, map[string]any{"seq": strconv.Itoa(i)}); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	xlen, err := client.XLen(ctx, stream).Result()
	if err != nil {
		t.Fatalf("xlen: %v", err)
	}
	// 近似裁剪:允许超一点(通常小几倍 maxLen 以内)。只要不是 publishN 全留就行。
	if xlen >= int64(publishN) {
		t.Fatalf("MAXLEN ~ did not trim: xlen=%d publishN=%d", xlen, publishN)
	}
	if xlen < int64(maxLen) {
		t.Fatalf("xlen unexpectedly below maxLen: xlen=%d maxLen=%d", xlen, maxLen)
	}
}

// waitFor 轮询 fn 最多 timeout,满足即返 true。
func waitFor(timeout time.Duration, fn func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fn()
}
