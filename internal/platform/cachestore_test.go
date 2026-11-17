// 缓存存储契约的验收缝：证明平台层之外的消费方可以只依赖 platform.CacheStore，
// 完全不 import 教务域（jiaowu）——二课域日后按同一份契约接入（issue #72）。
package platform_test

import (
	"context"
	"testing"
	"time"

	"nongyu-backend-plus/internal/platform"
)

// contractFake 是 platform.CacheStore 的最小进程内实现。
// 它只 import platform 与标准库，用来证明这份契约不牵连任何业务域。
type contractFake struct {
	data   map[string][]byte
	locked map[string]struct{}
}

func newContractFake() *contractFake {
	return &contractFake{
		data:   make(map[string][]byte),
		locked: make(map[string]struct{}),
	}
}

func (f *contractFake) Get(_ context.Context, key string) ([]byte, error) {
	return f.data[key], nil
}

func (f *contractFake) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	f.data[key] = append([]byte(nil), value...)
	return nil
}

func (f *contractFake) Delete(_ context.Context, key string) error {
	delete(f.data, key)
	return nil
}

func (f *contractFake) TryAcquire(_ context.Context, key string, _ time.Duration) (bool, error) {
	if _, ok := f.locked[key]; ok {
		return false, nil
	}
	f.locked[key] = struct{}{}
	return true, nil
}

// 编译期断言：平台层的具体实现与外部 fake 都满足同一份契约；
// 这些断言所在的包完全没有 import jiaowu。
var (
	_ platform.CacheStore = (*platform.RedisCache)(nil)
	_ platform.CacheStore = (*contractFake)(nil)
)

// TestCacheStore_IndependentContract 锁住「契约可独立依赖」这条缝：
// 一个既不属于平台层、也不 import 教务域的消费方，能声明该接口类型、
// 注入任意实现，并通过接口跑通全部四个方法。
func TestCacheStore_IndependentContract(t *testing.T) {
	ctx := context.Background()
	var store platform.CacheStore = newContractFake()

	if err := store.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := store.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "v" {
		t.Fatalf("Get = %q, want %q", got, "v")
	}

	first, err := store.TryAcquire(ctx, "lock", time.Minute)
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	if !first {
		t.Fatal("首次 TryAcquire 应为 true")
	}
	second, err := store.TryAcquire(ctx, "lock", time.Minute)
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	if second {
		t.Fatal("同一 key 重复 TryAcquire 应为 false")
	}

	if err := store.Delete(ctx, "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got, err := store.Get(ctx, "k"); err != nil || got != nil {
		t.Fatalf("Delete 后 Get = (%q, %v), want (nil, nil)", got, err)
	}
}
