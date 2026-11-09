package app_test

// code-review M1/M2：环境判定必须覆盖 staging，且默认无 .env 能起健康检查服务。
// seam = config.Load / app.New 公共入口。

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nongyu-backend-plus/internal/app"
	"nongyu-backend-plus/internal/config"
)

func serveGet(h http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// M1：staging + AUTH_KEY 空且未开开关 → 必须拒绝（不能 fail-open）。
func TestAppNew_StagingWithoutAuthKeyIsRejected(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	t.Setenv("AUTH_KEY", "")
	t.Setenv("ALLOW_INSECURE_DEV", "false")
	t.Setenv("TICKET_KEY", "test-ticket-key-32-bytes-min!!")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	_, err = app.New(cfg, quietLogger())
	if err == nil {
		t.Fatal("staging 下 AUTH_KEY 为空且未开 ALLOW_INSECURE_DEV 时，app.New 必须拒绝（fail-closed）")
	}
}

// M2：默认无 .env（APP_ENV=dev、AUTH_KEY 空）时，app.New 必须能起健康检查服务，
// 与 config.go:19 / README 的契约一致。
func TestAppNew_DefaultDevCanStartHealthService(t *testing.T) {
	// 清掉可能污染的环境变量，模拟「无 .env 直接 go run」
	for _, k := range []string{"APP_ENV", "AUTH_KEY", "ALLOW_INSECURE_DEV", "DATABASE_URL", "REDIS_ADDR", "HTTP_ALLOWLIST"} {
		t.Setenv(k, "")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if cfg.AppEnv != "dev" {
		t.Fatalf("默认 AppEnv 应为 dev，实际 %q", cfg.AppEnv)
	}
	a, err := app.New(cfg, quietLogger())
	if err != nil {
		t.Fatalf("默认无 .env 应能起健康检查服务，实际 %v", err)
	}
	defer a.Shutdown(time.Second)

	// /healthz 可达
	w := serveGet(a.Handler(), "/healthz")
	if w.Code != 200 {
		t.Fatalf("/healthz 应 200，实际 %d", w.Code)
	}
}
