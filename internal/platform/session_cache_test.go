package platform_test

import (
	"context"
	"os"
	"testing"
	"time"

	"nongyu-backend-plus/internal/platform"
)

// 会话的 Redis 副本：PG 是权威，Redis 用于热路径读取与快速吊销。
// 未配置 REDIS_ADDR 时跳过。

func newSessionCache(t *testing.T) *platform.RedisSessionCache {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("未设置 REDIS_ADDR，跳过 Redis 集成测试")
	}
	c, err := platform.NewRedisSessionCache(platform.RedisOptions{
		Addr:     addr,
		Password: os.Getenv("REDIS_PASSWORD"),
		Prefix:   "nongyu-sess-test:",
	})
	if err != nil {
		t.Fatalf("NewRedisSessionCache: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Ping(ctx); err != nil {
		t.Skipf("Redis 不可达，跳过：%v", err)
	}
	return c
}

func TestSessionCache_PutGetRevoke(t *testing.T) {
	c := newSessionCache(t)
	ctx := context.Background()
	id := "sess-cache-" + time.Now().Format("150405.000000")
	_ = c.Delete(ctx, id)
	defer c.Delete(ctx, id)

	sess := platform.Session{
		ID:        id,
		UserID:    42,
		Scope:     platform.ScopeApp,
		DeviceID:  "dev-1",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := c.PutSession(ctx, sess); err != nil {
		t.Fatalf("PutSession: %v", err)
	}

	got, err := c.CachedSession(ctx, id)
	if err != nil {
		t.Fatalf("CachedSession: %v", err)
	}
	if got == nil {
		t.Fatal("应命中缓存")
	}
	if got.UserID != 42 || got.Scope != platform.ScopeApp || got.DeviceID != "dev-1" {
		t.Fatalf("缓存内容不符：%+v", got)
	}

	// 吊销后应立即读不到（缓存里删掉，且 PG 侧由调用方负责）
	if err := c.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got2, err := c.CachedSession(ctx, id)
	if err != nil {
		t.Fatalf("CachedSession #2: %v", err)
	}
	if got2 != nil {
		t.Fatalf("删除后不应命中，实际 %+v", got2)
	}
}

// 缓存里必须保留吊销标记：吊销后的会话即使被 PG 查到，也要能快速判定失效。
func TestSessionCache_RevokedIsNotServed(t *testing.T) {
	c := newSessionCache(t)
	ctx := context.Background()
	id := "sess-rev-" + time.Now().Format("150405.000000")
	_ = c.Delete(ctx, id)
	defer c.Delete(ctx, id)

	now := time.Now()
	sess := platform.Session{
		ID:        id,
		UserID:    7,
		Scope:     platform.ScopeApp,
		DeviceID:  "dev-1",
		ExpiresAt: now.Add(time.Hour),
	}
	if err := c.PutSession(ctx, sess); err != nil {
		t.Fatalf("PutSession: %v", err)
	}
	if err := c.MarkRevoked(ctx, id, now); err != nil {
		t.Fatalf("MarkRevoked: %v", err)
	}
	got, err := c.CachedSession(ctx, id)
	if err != nil {
		t.Fatalf("CachedSession: %v", err)
	}
	// 仍能读到记录，但必须带吊销时间，且判定为不活跃。
	if got == nil {
		t.Fatal("吊销标记应保留，而不是直接消失")
	}
	if got.RevokedAt == nil {
		t.Fatal("吊销后应带 revokedAt")
	}
	if got.Active(now) {
		t.Fatal("已吊销的会话不得判定为活跃")
	}
}
