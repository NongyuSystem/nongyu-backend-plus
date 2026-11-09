package platform_test

// TestMain 把 DATABASE_URL 指向本包专属 schema，避免与其它包的集成测试互相干扰。
// 详见 internal/identity/testmain_test.go 的说明。

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
		if isolated, err := platform.EnsureTestSchema(ctx, dsn, "nongyu-backend-plus/internal/platform"); err == nil {
			_ = os.Setenv("DATABASE_URL", isolated)
		}
		cancel()
	}
	os.Exit(m.Run())
}
