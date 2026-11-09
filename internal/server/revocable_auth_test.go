package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/server"
)

// 可吊销鉴权中间件的测试：验签 → 查会话 → 卸载身份。
// 需要真实 Redis + PG 的部分未配置时跳过；纯内存假实现的部分始终运行。

// fakeSessionResolver 是 SessionResolver 的假实现。
type fakeSessionResolver struct {
	active  map[string]server.SessionInfo
	revoked map[string]bool
	err     error
}

func (f *fakeSessionResolver) ResolveSession(_ context.Context, sessionID string) (server.SessionInfo, error) {
	if f.err != nil {
		return server.SessionInfo{}, f.err
	}
	if f.revoked[sessionID] {
		return server.SessionInfo{}, server.ErrSessionInactive
	}
	info, ok := f.active[sessionID]
	if !ok {
		return server.SessionInfo{}, server.ErrSessionInactive
	}
	return info, nil
}

func newRevocableEngine(t *testing.T, verifier *server.TokenVerifier, resolver server.SessionResolver) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(server.RevocableAuthMiddleware(verifier, resolver, server.ScopeApp))
	r.GET("/whoami", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"userID":    c.GetString(server.CtxUserID),
			"studentNo": c.GetString(server.CtxStudentNo),
			"role":      c.GetInt(server.CtxRole),
		})
	})
	return r
}

func TestRevocableAuth_ValidSessionPasses(t *testing.T) {
	clock := platform.NewMockClock(time.Now())
	v := server.NewTokenVerifier("k", clock)
	tok := v.IssueSession("sess-1", time.Now().Add(time.Hour))
	resolver := &fakeSessionResolver{active: map[string]server.SessionInfo{
		"sess-1": {UserID: 7, StudentNo: "202311001", Role: 1, DeviceID: "dev-1"},
	}}
	r := newRevocableEngine(t, v, resolver)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("合法会话应通过，实际 %d body=%s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != `{"role":1,"studentNo":"202311001","userID":"7"}` {
		t.Fatalf("身份未正确卸载到 context：%s", got)
	}
}

// 这一步是旧中间件做不到的：令牌签名有效，但会话被吊销 → 必须拒绝。
func TestRevocableAuth_RevokedSessionRejected(t *testing.T) {
	clock := platform.NewMockClock(time.Now())
	v := server.NewTokenVerifier("k", clock)
	tok := v.IssueSession("sess-revoked", time.Now().Add(time.Hour))
	resolver := &fakeSessionResolver{
		active:  map[string]server.SessionInfo{},
		revoked: map[string]bool{"sess-revoked": true},
	}
	r := newRevocableEngine(t, v, resolver)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("已吊销会话必须 401，实际 %d", w.Code)
	}
}

// 会话查询故障必须 fail-closed（401/503），绝不能放行。
func TestRevocableAuth_ResolverErrorFailsClosed(t *testing.T) {
	clock := platform.NewMockClock(time.Now())
	v := server.NewTokenVerifier("k", clock)
	tok := v.IssueSession("sess-x", time.Now().Add(time.Hour))
	resolver := &fakeSessionResolver{err: context.DeadlineExceeded}
	r := newRevocableEngine(t, v, resolver)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	r.ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Fatal("会话查询故障时绝不能放行（fail-closed）")
	}
}

func TestRevocableAuth_MissingOrBadToken(t *testing.T) {
	clock := platform.NewMockClock(time.Now())
	v := server.NewTokenVerifier("k", clock)
	resolver := &fakeSessionResolver{active: map[string]server.SessionInfo{}}
	r := newRevocableEngine(t, v, resolver)

	// 无头
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/whoami", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("缺凭证应 401，实际 %d", w.Code)
	}

	// 签名被篡改
	tok := v.IssueSession("s", time.Now().Add(time.Hour))
	w2 := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+tok+"x")
	r.ServeHTTP(w2, req)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("篡改签名应 401，实际 %d", w2.Code)
	}
}

// 健康探针不需要鉴权。
func TestRevocableAuth_HealthzBypass(t *testing.T) {
	clock := platform.NewMockClock(time.Now())
	v := server.NewTokenVerifier("k", clock)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(server.RevocableAuthMiddleware(v, &fakeSessionResolver{}, server.ScopeApp))
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/healthz 应免鉴权，实际 %d", w.Code)
	}
}

// 登录端点必须免鉴权，否则会形成「没有身份就不能登录」的死锁。
// 这一条是实测出来的：全局中间件曾把 /identity/login/password 拦成 401。
func TestRevocableAuth_LoginEndpointsArePublic(t *testing.T) {
	clock := platform.NewMockClock(time.Now())
	v := server.NewTokenVerifier("k", clock)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(server.RevocableAuthMiddleware(v, &fakeSessionResolver{}, server.ScopeApp))
	reached := false
	r.POST("/identity/login/password", func(c *gin.Context) {
		reached = true
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	r.POST("/identity/mailcode/send", func(c *gin.Context) {
		reached = true
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	for _, path := range []string{"/identity/login/password", "/identity/mailcode/send"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, nil)
		r.ServeHTTP(w, req)
		if w.Code == http.StatusUnauthorized {
			t.Fatalf("%s 不应要求已认证身份（否则永远无法登录），实际 401", path)
		}
	}
	if !reached {
		t.Fatal("登录 handler 应被真正执行")
	}

	// 反面：业务端点仍然必须鉴权。
	gin.SetMode(gin.TestMode)
	r2 := gin.New()
	r2.Use(server.RevocableAuthMiddleware(v, &fakeSessionResolver{}, server.ScopeApp))
	r2.GET("/identity/me", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	w := httptest.NewRecorder()
	r2.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/identity/me", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("业务端点必须鉴权，实际 %d", w.Code)
	}
}

// 真实链路：用真实 Postgres 会话存储做 Resolver，验证吊销即时生效。
func TestRevocableAuth_RealStoreRevocation(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过真实存储集成测试")
	}
	store, err := platform.NewPGIdentityStore(platform.PGOptions{DSN: dsn})
	if err != nil {
		t.Fatalf("NewPGIdentityStore: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := store.Ping(ctx); err != nil {
		t.Skipf("Postgres 不可达，跳过：%v", err)
	}
	defer store.Close()

	u, err := store.Create(ctx, "999000777", platform.RoleAdmin)
	if err != nil && !platform.IsAlreadyExists(err) {
		t.Fatalf("Create: %v", err)
	}
	if u.ID == 0 {
		u, _ = store.ByStudentNo(ctx, "999000777")
	}
	// 该学号可能被上次运行置为禁用：先恢复为可用，否则鉴权会被「账号禁用」拦下。
	if err := store.SetStatus(ctx, u.ID, platform.UserActive); err != nil {
		t.Fatalf("SetStatus(active): %v", err)
	}
	defer func() { _ = store.SetStatus(ctx, u.ID, platform.UserActive) }()

	sid := "sess-real-" + time.Now().Format("150405.000000")
	sess := platform.Session{ID: sid, UserID: u.ID, Scope: platform.ScopeApp, DeviceID: "d1", ExpiresAt: time.Now().Add(time.Hour)}
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	clock := platform.NewMockClock(time.Now())
	v := server.NewTokenVerifier("k", clock)
	tok := v.IssueSession(sid, time.Now().Add(time.Hour))
	resolver := server.NewSessionStoreResolver(store, store, platform.NewRealClock())

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(server.RevocableAuthMiddleware(v, resolver, server.ScopeApp))
	r.GET("/ok", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	do := func() int {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/ok", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		r.ServeHTTP(w, req)
		return w.Code
	}
	if code := do(); code != http.StatusOK {
		t.Fatalf("吊销前应 200，实际 %d", code)
	}
	if err := store.Revoke(ctx, sid); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if code := do(); code != http.StatusUnauthorized {
		t.Fatalf("吊销后同令牌必须 401，实际 %d", code)
	}
}
