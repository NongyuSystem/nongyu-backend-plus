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

// ---- from auth_test.go ----
// 认证中间件：身份只能来自已验证的凭证，不能来自请求体/query。

func newAuthEngine(t *testing.T, clock platform.Clock) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// 用固定密钥构造 verifier；测试用其签发的 token。
	verifier := server.NewTokenVerifier("test-secret-key", clock)
	r.Use(server.AuthMiddleware(verifier))
	r.GET("/whoami", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"userID": c.GetString(server.CtxUserID)})
	})
	return r
}

func TestAuthMiddlewareRejectsMissingToken(t *testing.T) {
	clock := platform.NewMockClock(time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC))
	r := newAuthEngine(t, clock)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/whoami", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("缺凭证应 401，实际 %d", w.Code)
	}
}

func TestAuthMiddlewareAcceptsSignedTokenAndIgnoresBodyUserID(t *testing.T) {
	clock := platform.NewMockClock(time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC))
	verifier := server.NewTokenVerifier("test-secret-key", clock)
	token := verifier.Issue("alice", clock.Now().Add(time.Hour))
	r := newAuthEngine(t, clock)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/whoami?userID=attacker", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("合法 token 应 200，实际 %d，body=%s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != `{"userID":"alice"}` {
		t.Fatalf("身份应来自 token（alice），不得被 query 的 attacker 覆盖，实际 %s", got)
	}
}

func TestAuthMiddlewareRejectsTamperedAndExpiredToken(t *testing.T) {
	clock := platform.NewMockClock(time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC))
	verifier := server.NewTokenVerifier("test-secret-key", clock)
	r := newAuthEngine(t, clock)

	good := verifier.Issue("alice", clock.Now().Add(time.Hour))
	tampered := good[:len(good)-1] + "x"
	expired := verifier.Issue("alice", clock.Now().Add(-time.Hour))
	for name, tok := range map[string]string{"tampered": tampered, "expired": expired} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/whoami", nil)
			req.Header.Set("Authorization", "Bearer "+tok)
			r.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("%s token 应 401，实际 %d", name, w.Code)
			}
		})
	}
}

func TestRateLimiterBlocksAfterLimit(t *testing.T) {
	clock := platform.NewMockClock(time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC))
	limiter := server.NewRateLimiter(clock, 2, time.Minute)
	if !limiter.Allow("alice") {
		t.Fatal("第 1 次应允许")
	}
	if !limiter.Allow("alice") {
		t.Fatal("第 2 次应允许")
	}
	if limiter.Allow("alice") {
		t.Fatal("超过上限应拒绝")
	}
	if !limiter.Allow("bob") {
		t.Fatal("不同用户独立计数")
	}
	clock.Set(clock.Now().Add(2 * time.Minute))
	if !limiter.Allow("alice") {
		t.Fatal("窗口过期后应重新允许")
	}
}

// ---- from ratelimit_test.go ----
// 限流器的两条关键行为：
//  1. 未认证请求**不能**被一律拒绝（否则登录请求自己就被挡掉）；
//  2. 仍然要能限流（按 IP 计数），防登录爆破。

func TestRateLimit_UnauthenticatedIsCountedNotRejected(t *testing.T) {
	clock := platform.NewMockClock(time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC))
	limiter := server.NewRateLimiter(clock, 3, time.Minute)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(server.RateLimitMiddleware(limiter))
	r.POST("/identity/login/password", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// 前 3 次应放行：未认证不等于被拒绝。
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/identity/login/password", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("第 %d 次未认证请求应放行（否则登录本身进不来），实际 %d", i+1, w.Code)
		}
	}

	// 超过配额后仍要限流（防爆破）。
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/identity/login/password", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	r.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("超过配额应 429，实际 %d", w.Code)
	}

	// 另一个 IP 不受影响（按 IP 独立计数）。
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/identity/login/password", nil)
	req2.RemoteAddr = "10.0.0.2:1234"
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("不同 IP 应独立计数，实际 %d", w2.Code)
	}
}

// 健康探针不计入限流。
func TestRateLimit_HealthzNotCounted(t *testing.T) {
	clock := platform.NewMockClock(time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC))
	limiter := server.NewRateLimiter(clock, 1, time.Minute)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(server.RateLimitMiddleware(limiter))
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })

	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("健康探针不应被限流，第 %d 次返回 %d", i+1, w.Code)
		}
	}
}

// ---- from revocable_auth_test.go ----
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

// ---- from testmain_test.go ----
// TestMain 把 DATABASE_URL 指向本包专属 schema，避免与其它包的集成测试互相干扰。
// 详见 internal/identity/testmain_test.go 的说明。

func TestMain(m *testing.M) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if isolated, err := platform.EnsureTestSchema(ctx, dsn, "nongyu-backend-plus/internal/server"); err == nil {
			_ = os.Setenv("DATABASE_URL", isolated)
		}
		cancel()
	}
	os.Exit(m.Run())
}
