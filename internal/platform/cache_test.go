package platform_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nongyu-backend-plus/internal/platform"
)

// 通用缓存层的验收测试：穿透 / 雪崩 / 击穿 / 降级回灌。
// 需要真实 Redis 的用例在未配置 REDIS_ADDR 时跳过。

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

// ── ①缓存穿透：不存在的 key 也要缓存「空」，避免每次打到下游 ──

func TestCachePenetration_CachesNotFound(t *testing.T) {
	c := newCache(t)
	ctx := context.Background()
	key := "penetration-missing"
	_ = c.Delete(ctx, key)

	var loaderCalls int32
	loader := func(context.Context) ([]byte, bool, error) {
		atomic.AddInt32(&loaderCalls, 1)
		return nil, false, nil // 下游明确「查无此物」
	}

	// 连打 5 次：只有第一次应真正查下游，其余命中「空值缓存」。
	for i := 0; i < 5; i++ {
		got, err := c.GetOrLoad(ctx, key, loader)
		if err != nil {
			t.Fatalf("第 %d 次 GetOrLoad: %v", i, err)
		}
		if got != nil {
			t.Fatalf("第 %d 次应返回空，实际 %q", i, got)
		}
	}
	if n := atomic.LoadInt32(&loaderCalls); n != 1 {
		t.Fatalf("穿透防护失效：下游应只被查 1 次，实际 %d 次", n)
	}

	// 空值缓存必须有较短 TTL，不能被当成长期真相。
	ttl, err := c.TTL(ctx, key)
	if err != nil {
		t.Fatalf("TTL: %v", err)
	}
	if ttl <= 0 || ttl > 11*time.Second {
		t.Fatalf("空值缓存 TTL 应较短（≤ NotFoundTTL），实际 %v", ttl)
	}
	_ = c.Delete(ctx, key)
}

// ── ②缓存雪崩：同一批写入的 TTL 必须有抖动，不能同时过期 ──

func TestCacheAvalanche_TTLJitter(t *testing.T) {
	c := newCache(t)
	ctx := context.Background()

	ttls := make(map[time.Duration]bool)
	for i := 0; i < 20; i++ {
		key := "avalanche-" + string(rune('a'+i))
		if _, err := c.GetOrLoad(ctx, key, func(context.Context) ([]byte, bool, error) {
			return []byte("v"), true, nil
		}); err != nil {
			t.Fatalf("GetOrLoad: %v", err)
		}
		ttl, err := c.TTL(ctx, key)
		if err != nil {
			t.Fatalf("TTL: %v", err)
		}
		ttls[ttl] = true
		_ = c.Delete(ctx, key)
	}
	if len(ttls) < 5 {
		t.Fatalf("雪崩防护失效：20 个 key 的 TTL 应被抖动成多种取值，实际只有 %d 种：%v", len(ttls), ttls)
	}
}

// ── ③缓存击穿：热点 key 失效瞬间，并发请求只允许一个去重建 ──

func TestCacheBreakdown_SingleFlight(t *testing.T) {
	c := newCache(t)
	ctx := context.Background()
	key := "breakdown-hot"
	_ = c.Delete(ctx, key)

	var loads int32
	loader := func(context.Context) ([]byte, bool, error) {
		atomic.AddInt32(&loads, 1)
		time.Sleep(100 * time.Millisecond) // 模拟慢下游
		return []byte("hot-value"), true, nil
	}

	const workers = 10
	var wg sync.WaitGroup
	results := make([][]byte, workers)
	errs := make([]error, workers)
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = c.GetOrLoad(ctx, key, loader)
		}(i)
	}
	wg.Wait()

	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("worker %d 出错：%v", i, errs[i])
		}
		if string(results[i]) != "hot-value" {
			t.Fatalf("worker %d 结果不符：%q", i, results[i])
		}
	}
	if n := atomic.LoadInt32(&loads); n != 1 {
		t.Fatalf("击穿防护失效：10 个并发请求应只重建 1 次，实际 %d 次", n)
	}
	_ = c.Delete(ctx, key)
}

// ── ④降级与回灌：Redis 不可达时用内存兜底，恢复后限速回灌 ──

func TestCacheFallback_ToMemoryAndBackfill(t *testing.T) {
	// 指向一个不存在的端口，制造 Redis 不可用。
	dead, err := platform.NewRedisCache(platform.RedisOptions{
		Addr:   "127.0.0.1:1",
		Prefix: "nongyu-cache-dead:",
	}, platform.CacheOptions{
		DefaultTTL:      time.Minute,
		JitterRatio:     0.2,
		NotFoundTTL:     10 * time.Second,
		RebuildTimeout:  200 * time.Millisecond,
		FallbackEnabled: true,
		FlushRatePerSec: 1000,
	})
	if err != nil {
		t.Fatalf("NewRedisCache: %v", err)
	}
	ctx := context.Background()

	// Redis 写不进去时，必须仍然「可用」——落到内存，行为对调用方透明。
	if err := dead.Set(ctx, "fallback-key", []byte("fallback-value"), 0); err != nil {
		t.Fatalf("Redis 不可用时应降级到内存而不是报错：%v", err)
	}
	got, err := dead.Get(ctx, "fallback-key")
	if err != nil || string(got) != "fallback-value" {
		t.Fatalf("降级后应能从内存读到，got=%q err=%v", got, err)
	}
	// 降级期间写入的数据应被记为「待回灌」。
	if pending := dead.PendingBackfill(); pending == 0 {
		t.Fatal("降级期间写入应进入待回灌队列")
	}
}

// ── ⑤幂等锁：同一 key 同时只允许一个持有者 ──

func TestIdempotentLock_MutualExclusion(t *testing.T) {
	lock := platform.NewIdempotentLock(newLockBackend(t), "nongyu-lock-test:")
	ctx := context.Background()
	key := "order-123"
	_ = lock.Unlock(ctx, key)

	var acquired int32
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := lock.TryLock(ctx, key, 5*time.Second)
			if err != nil {
				return
			}
			if ok {
				atomic.AddInt32(&acquired, 1)
			}
		}()
	}
	wg.Wait()
	if n := atomic.LoadInt32(&acquired); n != 1 {
		t.Fatalf("幂等锁失效：5 个并发抢占应只有 1 个成功，实际 %d 个", n)
	}
	// 释放后可再次获取。
	if err := lock.Unlock(ctx, key); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	ok, err := lock.TryLock(ctx, key, 5*time.Second)
	if err != nil || !ok {
		t.Fatalf("释放后应能再次获取，ok=%v err=%v", ok, err)
	}
	_ = lock.Unlock(ctx, key)
}

// ── ⑥事务：多步操作要么全成功要么全回滚 ──

func TestCacheTransaction_Atomic(t *testing.T) {
	c := newCache(t)
	ctx := context.Background()
	k1, k2 := "tx-a", "tx-b"
	_ = c.Delete(ctx, k1)
	_ = c.Delete(ctx, k2)

	if err := c.Set(ctx, k1, []byte("1"), 0); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// 事务里第二步故意报错 → 第一步的写入必须不生效。
	err := c.Transaction(ctx, func(tx platform.CacheTx) error {
		if err := tx.Set(ctx, k1, []byte("changed"), 0); err != nil {
			return err
		}
		if err := tx.Set(ctx, k2, []byte("new"), 0); err != nil {
			return err
		}
		return errors.New("boom")
	})
	if err == nil {
		t.Fatal("事务应返回业务错误")
	}
	got, _ := c.Get(ctx, k1)
	if string(got) != "1" {
		t.Fatalf("事务失败应回滚：k1 应仍为 \"1\"，实际 %q", got)
	}
	if v, _ := c.Get(ctx, k2); v != nil {
		t.Fatalf("事务失败应回滚：k2 不应被写入，实际 %q", v)
	}

	// 成功事务应全部生效。
	if err := c.Transaction(ctx, func(tx platform.CacheTx) error {
		if err := tx.Set(ctx, k1, []byte("2"), 0); err != nil {
			return err
		}
		return tx.Set(ctx, k2, []byte("3"), 0)
	}); err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if v, _ := c.Get(ctx, k1); string(v) != "2" {
		t.Fatalf("k1 应为 \"2\"，实际 %q", v)
	}
	if v, _ := c.Get(ctx, k2); string(v) != "3" {
		t.Fatalf("k2 应为 \"3\"，实际 %q", v)
	}
	_ = c.Delete(ctx, k1)
	_ = c.Delete(ctx, k2)
}
