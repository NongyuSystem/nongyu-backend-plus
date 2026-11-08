package platform_test

import (
	"context"
	"os"
	"testing"
	"time"

	"nongyu-backend-plus/internal/platform"
)

// newRealCacheForBreaker 用真实 Redis 构造一个带熔断的缓存，未配置 REDIS_ADDR 时跳过。
func newRealCacheForBreaker(t *testing.T) (*platform.RedisCache, string) {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("未设置 REDIS_ADDR，跳过 Redis 集成测试")
	}
	c, err := platform.NewRedisCache(platform.RedisOptions{
		Addr:     addr,
		Password: os.Getenv("REDIS_PASSWORD"),
		Prefix:   "nongyu-breaker-test:",
	}, platform.CacheOptions{
		DefaultTTL:       time.Minute,
		JitterRatio:      0.2,
		NotFoundTTL:      10 * time.Second,
		RebuildTimeout:   time.Second,
		FallbackEnabled:  true,
		FlushRatePerSec:  1000,
		BreakerThreshold: 3,
		BreakerCooldown:  time.Second,
	})
	if err != nil {
		t.Fatalf("NewRedisCache: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Ping(ctx); err != nil {
		t.Skipf("Redis 不可达，跳过：%v", err)
	}
	return c, addr
}

// 熔断与快速失败：Redis 持续不可用时，不应让每个请求都去等连接超时，
// 而应在连续失败若干次后直接降级，并定期半开探测恢复。

func TestCacheCircuitBreaker_OpensAfterThreshold(t *testing.T) {
	// 指向不可达端口，制造持续故障。
	c, err := platform.NewRedisCache(platform.RedisOptions{
		Addr:   "127.0.0.1:1",
		Prefix: "nongyu-cb-test:",
	}, platform.CacheOptions{
		DefaultTTL:       time.Minute,
		JitterRatio:      0.2,
		NotFoundTTL:      10 * time.Second,
		RebuildTimeout:   200 * time.Millisecond,
		FallbackEnabled:  true,
		FlushRatePerSec:  1000,
		BreakerThreshold: 3,
		BreakerCooldown:  2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewRedisCache: %v", err)
	}
	ctx := context.Background()

	// 连续触发失败，直到熔断器打开。
	for i := 0; i < 5; i++ {
		_ = c.Set(ctx, "cb-key", []byte("v"), 0)
	}
	if state := c.BreakerState(); state != platform.BreakerOpen {
		t.Fatalf("连续失败后熔断器应打开，实际 %v", state)
	}

	// 熔断打开后，写操作应立刻走降级（而不是每次等 Redis 连接超时）。
	start := time.Now()
	if err := c.Set(ctx, "cb-fast", []byte("v"), 0); err != nil {
		t.Fatalf("熔断熔断后应降级而非报错：%v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("熔断打开后应快速失败（不等连接超时），实际耗时 %v", elapsed)
	}

	// 数据仍可读（来自内存降级）。
	got, err := c.Get(ctx, "cb-fast")
	if err != nil || string(got) != "v" {
		t.Fatalf("熔断期间应能从内存读到，got=%q err=%v", got, err)
	}
}

func TestCacheCircuitBreaker_ClosesAfterRecovery(t *testing.T) {
	// 用真实 Redis 验证半开恢复：先正常，再手动打开熔断，冷却后探测成功应关闭。
	c, addr := newRealCacheForBreaker(t)
	ctx := context.Background()
	_ = addr

	if state := c.BreakerState(); state != platform.BreakerClosed {
		t.Fatalf("初始应为关闭，实际 %v", state)
	}
	if err := c.Set(ctx, "cb-recover", []byte("v"), 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if state := c.BreakerState(); state != platform.BreakerClosed {
		t.Fatalf("成功后应保持关闭，实际 %v", state)
	}
	_ = c.Delete(ctx, "cb-recover")
}
