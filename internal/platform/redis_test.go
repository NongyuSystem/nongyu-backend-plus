package platform_test

// RedisTicketStore 的集成测试：契约必须与 MockTicketStore 完全一致。
// 未配置 REDIS_ADDR 或连不上时跳过，保证 `go test ./...` 不依赖本机环境。
//
// 本文件同时承载两个被多个测试文件段共用的 Redis 脚手架：
//   - newCache：cache_test.go 的穿透/雪崩/击穿/事务四组用例都调用它；
//   - newLockBackend：cache_test.go 的幂等锁用例调用它。
// 它们原本就写在 redis_test.go 段里，故随该段落在此处，不另建文件。

import (
	"context"
	"os"
	"testing"
	"time"

	"nongyu-backend-plus/internal/platform"
)

// newCache 按测试配置构造一个带抖动 TTL、空值缓存、降级与回灌的真实 RedisCache。
func newCache(t *testing.T) *platform.RedisCache {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("未设置 REDIS_ADDR，跳过 Redis 集成测试")
	}
	c, err := platform.NewRedisCache(platform.RedisOptions{
		Addr:     addr,
		Password: os.Getenv("REDIS_PASSWORD"),
		Prefix:   "nongyu-cache-test:",
	}, platform.CacheOptions{
		DefaultTTL:      time.Minute,
		JitterRatio:     0.2, // 雪崩防护：TTL 抖动 ±20%
		NotFoundTTL:     10 * time.Second,
		RebuildTimeout:  2 * time.Second,
		FallbackEnabled: true,
		FlushRatePerSec: 50,
	})
	if err != nil {
		t.Fatalf("NewRedisCache: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Ping(ctx); err != nil {
		t.Skipf("Redis 不可达，跳过：%v", err)
	}
	return c
}

// newLockBackend 返回一把 Redis 客户端的薄封装，供幂等锁使用。
func newLockBackend(t *testing.T) platform.LockBackend {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("未设置 REDIS_ADDR，跳过 Redis 集成测试")
	}
	client, err := platform.NewRedisClient(platform.RedisOptions{
		Addr:     addr,
		Password: os.Getenv("REDIS_PASSWORD"),
	})
	if err != nil {
		t.Fatalf("NewRedisClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Ping(ctx); err != nil {
		t.Skipf("Redis 不可达，跳过：%v", err)
	}
	return client
}

func newRedisStore(t *testing.T) *platform.RedisTicketStore {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("未设置 REDIS_ADDR，跳过 Redis 集成测试")
	}
	store, err := platform.NewRedisTicketStore(platform.RedisOptions{
		Addr:     addr,
		Password: os.Getenv("REDIS_PASSWORD"),
		Prefix:   "nongyu-test:",
	})
	if err != nil {
		t.Fatalf("NewRedisTicketStore: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := store.Ping(ctx); err != nil {
		t.Skipf("Redis 不可达，跳过：%v", err)
	}
	return store
}

func TestRedisTicketStore_Contract(t *testing.T) {
	store := newRedisStore(t)
	ctx := context.Background()
	uid := "redis-test-user"
	_ = store.Delete(ctx, uid)
	defer func() { _ = store.Delete(ctx, uid) }()

	// 不存在 → ErrNotFound
	if _, _, err := store.Get(ctx, uid); !platform.IsNotFound(err) {
		t.Fatalf("缺记录应返回 ErrNotFound，实际 %v", err)
	}

	expires := time.Now().Add(time.Hour).Truncate(time.Second)
	if err := store.Put(ctx, uid, []byte("sealed-bytes"), expires); err != nil {
		t.Fatalf("Put: %v", err)
	}

	sealed, gotExp, err := store.Get(ctx, uid)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(sealed) != "sealed-bytes" {
		t.Fatalf("读回内容不符：%q", sealed)
	}
	if !gotExp.Equal(expires) {
		t.Fatalf("过期时刻不符：want %v got %v", expires, gotExp)
	}

	// ListExpiringSoon 应能挑到它
	refs, err := store.ListExpiringSoon(ctx, time.Now().Add(2*time.Hour), 10)
	if err != nil {
		t.Fatalf("ListExpiringSoon: %v", err)
	}
	found := false
	for _, r := range refs {
		if r.UserID == uid {
			found = true
		}
	}
	if !found {
		t.Fatalf("应挑出该候选，实际 %+v", refs)
	}

	// Delete 幂等
	if err := store.Delete(ctx, uid); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := store.Delete(ctx, uid); err != nil {
		t.Fatalf("Delete 应幂等：%v", err)
	}
	if _, _, err := store.Get(ctx, uid); !platform.IsNotFound(err) {
		t.Fatalf("删除后应 ErrNotFound，实际 %v", err)
	}
}
