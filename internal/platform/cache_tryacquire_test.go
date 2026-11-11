package platform

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestRedisCache_TryAcquire 用真实 Redis 验证 SET NX 语义。
//
// 内存假实现能覆盖业务分支，但覆盖不到「SET NX 真的是原子的」以及
// 「到期自动放开」这两点——那正是 TryAcquire 存在的理由。
//
// 没有 REDIS_TEST_ADDR 时跳过：集成测试不该让本地默认跑不动。
func TestRedisCache_TryAcquire(t *testing.T) {
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("未设置 REDIS_TEST_ADDR，跳过真实 Redis 集成测试")
	}

	ctx := context.Background()
	cache, err := NewRedisCache(
		RedisOptions{
			Addr:     addr,
			Password: os.Getenv("REDIS_TEST_PASSWORD"),
			// 用独立 DB 5 隔离，避免误删开发数据。
			DB:     5,
			Prefix: "nongyutest:tryacquire:",
		},
		CacheOptions{DefaultTTL: time.Minute},
	)
	if err != nil {
		t.Fatalf("构造 RedisCache 失败: %v", err)
	}
	defer func() { _ = cache.Close() }()

	if err := cache.Ping(ctx); err != nil {
		t.Fatalf("Redis 不可达: %v", err)
	}

	// 先清掉可能残留的键，保证用例可重复跑。
	_ = cache.Delete(ctx, "probe")
	t.Cleanup(func() { _ = cache.Delete(ctx, "probe") })

	// 第一次应当占到。
	ok, err := cache.TryAcquire(ctx, "probe", 2*time.Second)
	if err != nil {
		t.Fatalf("TryAcquire 报错: %v", err)
	}
	if !ok {
		t.Fatal("首次 TryAcquire 应成功")
	}

	// 紧接着第二次必须失败——这就是「窗口内只允许一次」。
	ok, err = cache.TryAcquire(ctx, "probe", 2*time.Second)
	if err != nil {
		t.Fatalf("二次 TryAcquire 报错: %v", err)
	}
	if ok {
		t.Fatal("窗口内二次 TryAcquire 应失败（SET NX 未生效）")
	}

	// 等 TTL 过期后应重新可用。
	time.Sleep(2200 * time.Millisecond)
	ok, err = cache.TryAcquire(ctx, "probe", 2*time.Second)
	if err != nil {
		t.Fatalf("过期后 TryAcquire 报错: %v", err)
	}
	if !ok {
		t.Fatal("TTL 过期后 TryAcquire 应恢复可用")
	}
}
