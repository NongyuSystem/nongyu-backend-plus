package app_test

// #25 任务 3 / #21 判据：受保护端点的 401/200 必须经**真实装配**验证。
//
// seam：app.New(cfg) 装配出的 http.Handler + HTTP 状态码。
// 期望值来自票正文：「无令牌访问受保护端点 → 401；有有效令牌 → 200」。
// 不是只测中间件函数本身——那样测不出装配顺序（Use 与路由注册的先后）。

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"nongyu-backend-plus/internal/app"
	"nongyu-backend-plus/internal/config"
	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/server"
)

// newAuthedApp 用独立 schema + 真实鉴权密钥装配一个 App，并返回同一个
// schema 下的身份存储（用于造会话），保证 App 与测试看到同一份数据。
func newAuthedApp(t *testing.T, schema string) (*app.App, *platform.PGIdentityStore) {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("未设置 DATABASE_URL，跳过装配层鉴权集成测试")
	}
	dsn, _ := appTestDSN(t, schema)
	cfg := config.Config{
		Addr: ":0", AppEnv: "test", ShutdownTimeoutSec: 1,
		TicketKey: "test-ticket-key-32-bytes-min!!", HTTPTimeoutSec: 3,
		AuthKey: testAuthKey, RateLimitPerMinute: 600,
		DatabaseURL: dsn,
		HTTPEnforce: false,
	}
	a, err := app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(func() { _ = a.Shutdown(time.Second) })

	store, err := platform.NewPGIdentityStore(platform.PGOptions{DSN: dsn})
	if err != nil {
		t.Fatalf("NewPGIdentityStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return a, store
}

const testAuthKey = "test-auth-key-for-401-200-check"

// 无令牌访问受保护端点 → 401。
func TestProtectedEndpoint_NoTokenIs401(t *testing.T) {
	a, _ := newAuthedApp(t, "app_auth_no_token")

	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/identity/me", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("无令牌访问 /identity/me 应 401，实际 %d body=%s", w.Code, w.Body.String())
	}
}

// 有效令牌访问受保护端点 → 200（不是 401，也不是 5xx）。
func TestProtectedEndpoint_ValidTokenIs200(t *testing.T) {
	a, store := newAuthedApp(t, "app_auth_valid_token")
	ctx := context.Background()

	// 建用户 + 画像 + 一条有效会话（全部用同一个 schema）
	u, err := store.Create(ctx, "888100031", platform.RoleUser)
	if err != nil {
		t.Fatalf("Create user: %v", err)
	}
	if err := store.Upsert(ctx, platform.Profile{
		UserID: u.ID, Name: "鉴权检查用户", College: "信息工程学院",
	}); err != nil {
		t.Fatalf("Upsert profile: %v", err)
	}
	now := time.Now()
	sess := platform.Session{
		ID: "app-auth-valid-token-session", UserID: u.ID,
		Scope: platform.ScopeApp, DeviceID: "dev-auth-check",
		ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// 用装配层同一把密钥签发令牌
	verifier := server.NewTokenVerifier(testAuthKey, platform.NewRealClock())
	token := verifier.IssueSession(sess.ID, sess.ExpiresAt)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/identity/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	a.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("有效令牌访问 /identity/me 应 200，实际 %d body=%s", w.Code, w.Body.String())
	}
	// 体里应是该用户的画像（不能是空/别人的）
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应：%v", err)
	}
	if got, _ := body["name"].(string); got != "鉴权检查用户" {
		t.Fatalf("响应应为该用户画像，实际 %v", body)
	}
}
