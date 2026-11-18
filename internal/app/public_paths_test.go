package app_test

// code-review M5/N6：公开路径必须是单一来源，三处中间件口径一致。
// 特别覆盖：装配后 /status 不再免认证可达（0-day 修复）、
// POST /identity/login 无尾斜杠不被 401 拦。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nongyu-backend-plus/internal/app"
	"nongyu-backend-plus/internal/config"
	"nongyu-backend-plus/internal/server"
)

func TestPublicPaths_SingleSourceCoversStatusAndLogin(t *testing.T) {
	// 单一来源必须认得这几个「取得身份」类路径；/status 刻意不在其中。
	for _, p := range []string{"/healthz", "/identity/login", "/identity/login/admin", "/identity/mailcode/send", "/identity/register"} {
		if !server.IsPublicPath(p) {
			t.Fatalf("公开路径 %q 应被 IsPublicPath 认得", p)
		}
	}
	if server.IsPublicPath("/status") {
		t.Fatal("/status 返回内部基础设施状态，不得再是免认证公开路径")
	}
	if server.IsPublicPath("/identity/me") {
		t.Fatal("受保护端点 /identity/me 不得被认作公开路径")
	}
}

func TestAppNew_StatusRequiresAuth(t *testing.T) {
	// 退化装配：AUTH_KEY 非空但无 DATABASE_URL → AuthMiddleware 路径。
	// /status 含内部字段，修复后必须要求有效会话；匿名请求应被 401 拦下。
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
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("/status 应要求已认证身份（401），实际 %d", w.Code)
	}
	// 唯一免认证存活探针仍可达。
	probe := httptest.NewRecorder()
	a.Handler().ServeHTTP(probe, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if probe.Code != http.StatusOK {
		t.Fatalf("/healthz 唯一免签探针应仍 200，实际 %d", probe.Code)
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

func TestAppNew_UnsignedLoginRejectedWhenAppSignEnabled(t *testing.T) {
	cfg := config.Config{
		Addr: ":0", AppEnv: "test", ShutdownTimeoutSec: 1,
		TicketKey: "test-ticket-key-32-bytes-min!!", HTTPTimeoutSec: 3,
		AuthKey: "test-auth-key", RateLimitPerMinute: 60,
		HTTPEnforce:    false,
		AppSigningKeys: map[string][]byte{"dev": []byte("nongyu-dev-app-sign-key-v1-32b!!")},
	}
	a, err := app.New(cfg, quietLogger())
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Shutdown(time.Second)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/identity/login", nil)
	a.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("配了签名密钥后，未签名登录应 401，实际 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "app_sign_invalid") {
		t.Fatalf("应是请求签名拒绝，不是用户鉴权：%s", w.Body.String())
	}

	probe := httptest.NewRecorder()
	a.Handler().ServeHTTP(probe, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if probe.Code != http.StatusOK {
		t.Fatalf("探针仍应免签名，实际 %d", probe.Code)
	}
}
