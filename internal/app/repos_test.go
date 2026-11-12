package app

import (
	"context"
	"os"
	"testing"
	"time"

	"nongyu-backend-plus/internal/content/post"
)

// TestPGRepoBundle_UsableAfterAssembly 锁住一个曾把真实依赖打挂的 bug。
//
// 历史：`app.New` 里曾写过 `defer pgPool.Close()`（本意是「装配失败时释放」），
// 但 defer 在**成功返回时也会执行**，于是装配刚完成池就被关了，
// 所有 PG 查询报 "closed pool"、HTTP 层只看到 500。
// 而 WriteGin 当时不记日志，排查时毫无线索（现已补日志）。
//
// 这条测试的要点：装配拿到的 bundle 在 New 返回**之后**仍然可用。
func TestPGRepoBundle_UsableAfterAssembly(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未配置 DATABASE_URL，跳过真实 PG 测试")
	}
	ctx := context.Background()
	pool, err := openPGPool(ctx, dsn)
	if err != nil {
		t.Fatalf("开池失败：%v", err)
	}
	if pool == nil {
		t.Fatal("配了 DSN 却拿到 nil 池")
	}
	defer pool.Close()

	bundle, err := newRepoBundle(pool, func() time.Time { return time.Now().UTC() })
	if err != nil {
		t.Fatalf("构造 bundle 失败：%v", err)
	}
	if bundle.Mode != storeModePostgres {
		t.Fatalf("配了 DSN 应走 postgres 模式，得到 %q", bundle.Mode)
	}

	// 关键断言：装配**之后**池仍然活着。
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("装配后池已被关闭（这正是历史 bug 的现象）：%v", err)
	}

	// 再验一次仓储真的能查（Ping 通不代表查询通）。
	if _, err := bundle.Posts.List(ctx, "", post.PageCursor{}, 10); err != nil {
		t.Fatalf("装配后仓储查询失败：%v", err)
	}
}

// TestRepoBundle_MemoryModeWhenNoDSN 保证未配库时不 panic、模式如实。
func TestRepoBundle_MemoryModeWhenNoDSN(t *testing.T) {
	bundle, err := newRepoBundle(nil, time.Now)
	if err != nil {
		t.Fatalf("无池时应正常回落到内存：%v", err)
	}
	if bundle.Mode != storeModeMemory {
		t.Fatalf("无池应为 memory 模式，得到 %q", bundle.Mode)
	}
	if bundle.Posts == nil || bundle.Announcements == nil ||
		bundle.Messages == nil || bundle.Track == nil {
		t.Fatal("内存模式下四个仓储都不能为 nil")
	}
}
