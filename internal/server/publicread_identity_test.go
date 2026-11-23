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

// 本文件锁住「公开读端点上的可选身份解析」这一行为。
//
// 背景：GET /posts 是公开读（免认证），但它支持 ?mine=true（我的帖子），
// 那必须有身份。中间件原先在公开读分支直接 c.Next()，令牌根本不被解析，
// mine 永远拿不到人。
//
// 现在改成「尽力而为」：带有效令牌就解析出身份；没带/无效/已撤销
// **仍匿名放行**——公开列表不能因为客户端揣着一个过期令牌就整页打不开。

// newPublicReadEngine 挂一个模拟的公开读路由，返回它看到身份。
func newPublicReadEngine(t *testing.T, verifier *server.TokenVerifier, resolver server.SessionResolver) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(server.RevocableAuthMiddleware(verifier, resolver, server.ScopeApp))
	// /posts 在 publicReadablePaths 里，所以走的是公开读分支。
	r.GET("/posts", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"userID": c.GetString(server.CtxUserID),
			"mine":   c.Query("mine"),
		})
	})
	return r
}

func TestPublicRead_ValidTokenYieldsIdentity(t *testing.T) {
	clock := platform.NewMockClock(time.Now())
	v := server.NewTokenVerifier("k", clock)
	tok := v.IssueSession("sess-1", time.Now().Add(time.Hour))
	resolver := &fakeSessionResolver{active: map[string]server.SessionInfo{
		"sess-1": {UserID: 7, StudentNo: "202311001", Role: 0},
	}}
	r := newPublicReadEngine(t, v, resolver)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/posts?mine=true", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("有效令牌的公开读应 200，得到 %d", w.Code)
	}
	// 关键断言：身份必须被写入 context，否则 mine 拿不到人。
	if !containsUserID(w.Body.String(), "7") {
		t.Fatalf("有效令牌应解析出 userID=7，响应：%s", w.Body.String())
	}
}

func TestPublicRead_NoTokenStillAllowed(t *testing.T) {
	clock := platform.NewMockClock(time.Now())
	v := server.NewTokenVerifier("k", clock)
	r := newPublicReadEngine(t, v, &fakeSessionResolver{})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/posts", nil)
	r.ServeHTTP(w, req)

	// 公开读：没令牌也要放行（匿名看列表）。
	if w.Code != http.StatusOK {
		t.Fatalf("匿名公开读应 200，得到 %d", w.Code)
	}
}

// TestPublicRead_RevokedTokenStillAllowedAnonymously 是这条设计的核心取舍。
//
// 客户端可能持有一个已撤销/过期的令牌。若公开列表因此返回 401，
// 用户会「连帖子列表都打不开」——而列表本身是公开的。
// 正确行为：匿名放行，只是 mine 分支会自己 401。
func TestPublicRead_RevokedTokenStillAllowedAnonymously(t *testing.T) {
	clock := platform.NewMockClock(time.Now())
	v := server.NewTokenVerifier("k", clock)
	tok := v.IssueSession("sess-revoked", time.Now().Add(time.Hour))
	resolver := &fakeSessionResolver{revoked: map[string]bool{"sess-revoked": true}}
	r := newPublicReadEngine(t, v, resolver)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/posts", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("已撤销令牌的公开读仍应匿名放行（200），得到 %d", w.Code)
	}
	// 且不应解析出身份
	if containsUserID(w.Body.String(), "7") {
		t.Fatal("已撤销令牌不该被当成有效身份")
	}
}

// TestPublicRead_GarbageTokenStillAllowed 乱码令牌同样不阻断公开读。
func TestPublicRead_GarbageTokenStillAllowed(t *testing.T) {
	clock := platform.NewMockClock(time.Now())
	v := server.NewTokenVerifier("k", clock)
	r := newPublicReadEngine(t, v, &fakeSessionResolver{})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/posts", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("乱码令牌不该阻断公开读，得到 %d", w.Code)
	}
}

// TestWritePath_StillRequiresAuth 确认「公开读放宽」没有波及写操作。
func TestWritePath_StillRequiresAuth(t *testing.T) {
	clock := platform.NewMockClock(time.Now())
	v := server.NewTokenVerifier("k", clock)
	r := newPublicReadEngine(t, v, &fakeSessionResolver{})
	// POST /posts 不在公开读清单里（方法维度挡住）。
	r.POST("/posts", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/posts", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("写操作仍应要求认证（401），得到 %d", w.Code)
	}
}

// containsUserID 判断响应体里是否出现了指定的 userID 值。
func containsUserID(body, want string) bool {
	return len(body) > 0 && (indexOf(body, `"userID":"`+want+`"`) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
