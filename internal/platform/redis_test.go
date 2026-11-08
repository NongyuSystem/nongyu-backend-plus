package platform_test

import (
	"context"
	"os"
	"testing"
	"time"

	"nongyu-backend-plus/internal/platform"
)

// RedisTicketStore 的集成测试：契约必须与 MockTicketStore 完全一致。
// 未配置 REDIS_ADDR 或连不上时跳过，保证 `go test ./...` 不依赖本机环境。

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
