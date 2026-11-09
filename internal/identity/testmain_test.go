package identity_test

// TestMain 在包启动时把 DATABASE_URL 指向**本包专属 schema**。
//
// 为什么需要它：多个测试包并行运行时共用同一个 Postgres，会互相看到/删除
// 对方的数据，表现为「单独跑通过、连跑失败」。实测踩过：identity 包与
// flow 包并行时，一方的清理把另一方正在用的用户删掉。
//
// 隔离后各包测试代码里的 os.Getenv("DATABASE_URL") 自动拿到独立 schema 的 DSN，
// 无需改动任何测试主体。

import (
	"context"
	"os"
	"testing"
	"time"

	"nongyu-backend-plus/internal/platform"
)

func TestMain(m *testing.M) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if isolated, err := platform.EnsureTestSchema(ctx, dsn, "nongyu-backend-plus/internal/identity"); err == nil {
			_ = os.Setenv("DATABASE_URL", isolated)
		}
		cancel()
	}
	os.Exit(m.Run())
}
