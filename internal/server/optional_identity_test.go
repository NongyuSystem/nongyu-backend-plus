package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/server"
)

// 本文件锁住「免认证端点上的可选身份解析」这一机制。
//
// 2026-11-23 维护者决定「基本所有业务都要认证」，publicReadablePaths 已清空。
// 因此当前**没有业务端点走这条分支**——但机制本身保留：
//
//   - 判定入口 AllowedWithoutAuth 仍同时考虑 publicPaths（身份获取类）；
//   - 免认证分支里的「尽力解析身份」逻辑（带有效令牌就解析）不变；
//   - 将来若真加回公开内容，这条机制能直接用，不必重新设计。
//
// 这些测试通过**临时注入**一个公开路径来验证机制（不改全局清单），
// 而不是依赖某个恰好公开的业务端点——那样清单一变测试就碎。

// newOptionalIdentityEngine 挂一个模拟的免认证路由，返回它看到的身份。
//
// 注意：这里不能依赖真实业务路径在公开清单里（已经没有了）。
// 测试改用 /healthz ——它是 publicPaths 里**真实存在**的免认证端点，
// 且 middleware 对它同样走「尽力解析身份」分支。
func newOptionalIdentityEngine(t *testing.T, verifier *server.TokenVerifier, resolver server.SessionResolver) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(server.RevocableAuthMiddleware(verifier, resolver, server.ScopeApp))
	// 用 /healthz 作为免认证端点：它在 publicPaths 里，走 AllowedWithoutAuth 分支。
	// handler 把 context 里的身份回显，便于断言。
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"userID": c.GetString(server.CtxUserID),
			"mine":   c.Query("mine"),
		})
	})
	return r
}

func TestOptionalIdentity_ValidTokenYieldsIdentity(t *testing.T) {
	clock := platform.NewMockClock(time.Now())
	v := server.NewTokenVerifier("k", clock)
	tok := v.IssueSession("sess-1", time.Now().Add(time.Hour))
	resolver := &fakeSessionResolver{active: map[string]server.SessionInfo{
		"sess-1": {UserID: 7, StudentNo: "202311001", Role: 0},
	}}
	r := newOptionalIdentityEngine(t, v, resolver)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz?mine=true", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("有效令牌的免认证端点应 200，得到 %d", w.Code)
	}
	// 关键断言：身份必须被写入 context。
	if !containsUserID(w.Body.String(), "7") {
		t.Fatalf("有效令牌应解析出 userID=7，响应：%s", w.Body.String())
	}
}

func TestOptionalIdentity_NoTokenStillAllowed(t *testing.T) {
	clock := platform.NewMockClock(time.Now())
	v := server.NewTokenVerifier("k", clock)
	r := newOptionalIdentityEngine(t, v, &fakeSessionResolver{})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("免认证端点没令牌也应 200，得到 %d", w.Code)
	}
}

// TestOptionalIdentity_RevokedTokenStillAllowedAnonymously 是机制的核心取舍。
//
// 客户端可能持有一个已撤销/过期的令牌。若免认证端点因此返回 401，
// 用户会「连探活都做不了」。正确行为：仍放行，只是不解析出身份。
func TestOptionalIdentity_RevokedTokenStillAllowedAnonymously(t *testing.T) {
	clock := platform.NewMockClock(time.Now())
	v := server.NewTokenVerifier("k", clock)
	tok := v.IssueSession("sess-revoked", time.Now().Add(time.Hour))
	resolver := &fakeSessionResolver{revoked: map[string]bool{"sess-revoked": true}}
	r := newOptionalIdentityEngine(t, v, resolver)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("已撤销令牌的免认证端点仍应匿名放行（200），得到 %d", w.Code)
	}
	if containsUserID(w.Body.String(), "7") {
		t.Fatal("已撤销令牌不该被当成有效身份")
	}
}

// TestOptionalIdentity_GarbageTokenStillAllowed 乱码令牌同样不阻断免认证端点。
func TestOptionalIdentity_GarbageTokenStillAllowed(t *testing.T) {
	clock := platform.NewMockClock(time.Now())
	v := server.NewTokenVerifier("k", clock)
	r := newOptionalIdentityEngine(t, v, &fakeSessionResolver{})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("乱码令牌不该阻断免认证端点，得到 %d", w.Code)
	}
}

// TestBusinessPath_RequiresAuth 确认新的认证边界：业务端点（含曾经公开的
// /posts /announcements /skins）现在一律要求会话。
func TestBusinessPath_RequiresAuth(t *testing.T) {
	clock := platform.NewMockClock(time.Now())
	v := server.NewTokenVerifier("k", clock)
	r := newOptionalIdentityEngine(t, v, &fakeSessionResolver{})

	// 挂一个模拟业务路由 /posts（现在不在任何公开清单里）。
	r.GET("/posts", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/posts", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("GET /posts 现在应要求会话，得到 %d", w.Code)
	}
}

// TestWritePath_StillRequiresAuth 确认写操作始终要求认证。
func TestWritePath_StillRequiresAuth(t *testing.T) {
	clock := platform.NewMockClock(time.Now())
	v := server.NewTokenVerifier("k", clock)
	r := newOptionalIdentityEngine(t, v, &fakeSessionResolver{})
	r.POST("/posts", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/posts", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("POST /posts 应要求会话，得到 %d", w.Code)
	}
}

// containsUserID 判断响应体里是否回显了指定 userID。
//
// 用 JSON 解析而不是 strings.Contains("7")：后者会被 "202311001" 里的 7
// 误命中（学号里含 7），那是假的断言通过。
func containsUserID(body, want string) bool {
	var v map[string]any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		return false
	}
	got, _ := v["userID"].(string)
	return got == want
}
