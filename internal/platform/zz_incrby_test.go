package platform_test

// IncrBy / IncrGet 的真实 Redis 集成测试。
//
// 这两个方法为「按账号的登录失败计数」提供跨实例共享能力，
// 语义必须满足：原子自增、首次设 TTL 且不刷新、读不到当 0。
//
// 需要真实 Redis（REDIS_ADDR）。未配置时 Skip。

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"nongyu-backend-plus/internal/platform"
)

func newTestRedisCache(t *testing.T) *platform.RedisCache {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("未设置 REDIS_ADDR，跳过 Redis 集成测试")
	}
	// 每次运行用唯一的 prefix：同一 Redis 上重复跑测试时，
	// 上一次遗留的计数器键会让「第 1 次自增应返回 1」这类断言失败。
	prefix := "incrtest:" + t.Name() + ":" + strconv.FormatInt(time.Now().UnixNano(), 36) + ":"
	c, err := platform.NewRedisCache(platform.RedisOptions{
		Addr:     addr,
		Password: os.Getenv("REDIS_PASSWORD"),
		DB:       envIntOrZero("REDIS_DB"),
		Prefix:   prefix,
	}, platform.CacheOptions{DefaultTTL: time.Minute})
	if err != nil {
		t.Fatalf("构造 RedisCache 失败：%v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func envIntOrZero(k string) int {
	if os.Getenv(k) == "" {
		return 0
	}
	n := 0
	for _, ch := range os.Getenv(k) {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

func TestIncrBy_IncrementsAndExposesViaIncrGet(t *testing.T) {
	c := newTestRedisCache(t)
	ctx := context.Background()
	const key = "idcard:202600001"

	// 不存在的键 → 读 0（不是错误）。
	if n, err := c.IncrGet(ctx, key); err != nil || n != 0 {
		t.Fatalf("不存在的计数器应读 0，实际 n=%d err=%v", n, err)
	}

	for want := int64(1); want <= 3; want++ {
		got, err := c.IncrBy(ctx, key, 1, 15*time.Minute)
		if err != nil {
			t.Fatalf("第 %d 次自增失败：%v", want, err)
		}
		if got != want {
			t.Fatalf("第 %d 次自增应返回 %d，实际 %d", want, want, got)
		}
	}

	n, err := c.IncrGet(ctx, key)
	if err != nil {
		t.Fatalf("IncrGet 失败：%v", err)
	}
	if n != 3 {
		t.Fatalf("读回应为 3，实际 %d", n)
	}
}

// TestIncrBy_DoesNotRefreshTTL 保证窗口是"从第一次失败开始算"的固定滑窗：
// 反复尝试不会让计数器永不过期（否则攻击者会被永久锁住，正常用户也可能被卡死）。
func TestIncrBy_DoesNotRefreshTTL(t *testing.T) {
	c := newTestRedisCache(t)
	ctx := context.Background()
	const key = "password:202600002"
	const ttl = 2 * time.Second

	if _, err := c.IncrBy(ctx, key, 1, ttl); err != nil {
		t.Fatalf("首次自增失败：%v", err)
	}
	time.Sleep(600 * time.Millisecond)
	if _, err := c.IncrBy(ctx, key, 1, ttl); err != nil {
		t.Fatalf("再次自增失败：%v", err)
	}

	// 若 TTL 被刷新，剩余会接近 ttl；未被刷新则应明显小于 ttl。
	remain, err := c.TTL(ctx, key)
	if err != nil {
		t.Fatalf("读 TTL 失败：%v", err)
	}
	if remain > ttl-400*time.Millisecond {
		t.Fatalf("再次自增后 TTL 被刷新了（剩余 %v，接近初始 %v）：窗口会变成"+
			"\"一直被尝试就一直不过期\"，应保持首次设置的固定窗口", remain, ttl)
	}
}
