package app_test

// code-review M5/N6：公开路径必须是单一来源，三处中间件口径一致。
// 特别覆盖：装配后 /status 免认证可达、POST /identity/login 无尾斜杠不被 401 拦。

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nongyu-backend-plus/internal/app"
	"nongyu-backend-plus/internal/config"
	"nongyu-backend-plus/internal/server"
)

func TestPublicPaths_SingleSourceCoversStatusAndLogin(t *testing.T) {
	// 单一来源必须认得这几个路径
	for _, p := range []string{"/healthz", "/status", "/identity/login", "/identity/login/admin", "/identity/mailcode/send", "/identity/register"} {
		if !server.IsPublicPath(p) {
			t.Fatalf("公开路径 %q 应被 IsPublicPath 认得", p)
		}
	}
	if server.IsPublicPath("/identity/me") {
		t.Fatal("受保护端点 /identity/me 不得被认作公开路径")
	}
}

func TestAppNew_StatusReachableWithoutAuth(t *testing.T) {
	// 退化装配：AUTH_KEY 非空但无 DATABASE_URL → AuthMiddleware 路径，
	// 之前只放行 /healthz，/status 会被 401。
	cfg := config.Config{
		Addr: ":0", AppEnv: "test", ShutdownTimeoutSec: 1,
		TicketKey: "test-ticket-key-32-bytes-min!!", HTTPTimeoutSec: 3,
		AuthKey: "test-auth-key", RateLimitPerMinute: 60,
		HTTPEnforce: false,
	}
	a, err := app.New(cfg, quietLogger())
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Shutdown(time.Second)

	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/status", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/status 应免认证可达（200），实际 %d", w.Code)
	}
}

func TestAppNew_LoginEndpointNotBlockedByAuth(t *testing.T) {
	cfg := config.Config{
		Addr: ":0", AppEnv: "test", ShutdownTimeoutSec: 1,
		TicketKey: "test-ticket-key-32-bytes-min!!", HTTPTimeoutSec: 3,
		AuthKey: "test-auth-key", RateLimitPerMinute: 60,
		HTTPEnforce: false,
	}
	a, err := app.New(cfg, quietLogger())
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Shutdown(time.Second)

	// POST /identity/login 不带令牌：不应被鉴权拦成 401
	// （真正的校验在 handler 里，这里只看中间件是否误拦）
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/identity/login", nil)
	req.Header.Set("Content-Type", "application/json")
	a.Handler().ServeHTTP(w, req)
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("登录端点不得被全局鉴权拦成 401（公开路径漏判无尾斜杠）")
	}
}
