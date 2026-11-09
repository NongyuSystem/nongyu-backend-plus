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
