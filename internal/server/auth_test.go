package server_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/server"
)

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
