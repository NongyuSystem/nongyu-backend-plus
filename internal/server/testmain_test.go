package server_test

// TestMain 把 DATABASE_URL 指向本包专属 schema，避免与其它包的集成测试互相干扰。
// 详见 internal/identity/testmain_test.go 的说明。
//
// 注意：一个 package 只能有一个 TestMain。本包（server）的 TestMain 只留在这里，
// 其它任何 *_test.go 都不得再定义 TestMain。

import (
	"context"
	"os"
	"testing"
	"time"

	pkgx "nongyu-backend-plus/internal/platform/pgx"
)

func TestMain(m *testing.M) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if isolated, err := pkgx.EnsureTestSchema(ctx, dsn, "nongyu-backend-plus/internal/server"); err == nil {
			_ = os.Setenv("DATABASE_URL", isolated)
		}
		cancel()
	}
	os.Exit(m.Run())
}
