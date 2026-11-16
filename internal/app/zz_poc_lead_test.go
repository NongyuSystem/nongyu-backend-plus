package app

// 0-day 回归测试（lead 独占写入文件）。
//
// 用最小 harness 重建**真实装配里的中间件链**，不依赖数据库 / Redis / 学校网络：
//
//	engine := server.New(trustedProxies...)   // V2 修复：默认不信任 XFF
//	engine.Use(RateLimit(RevocableAuth(ScopeApp)))
//	→ identityuser.RegisterPublic(engine, ...)        // 本人端点
//	→ adminGroup := engine.Group("/", RevocableAuth(ScopeAdmin), AdminOnly(RoleAdmin))
//	  identityuser.RegisterAdmin(adminGroup, ...)     // V1 修复：管理端点
//
// 这两个测试在修复前是 RED（漏洞存在），修复后转为 GREEN（断言「必须被拒绝」）。
//   - V1：普通学生（role=0, scope=app）不得读 /identity/users/:studentNo；
//          只有 admin 会话 + role>=1 才可读（规格 spec-身份域与平台底座:72）。
//   - V2：伪造 X-Forwarded-For 不得让同一 IP 绕过未认证限流。

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/identity/user"
	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/server"
)

// ---------- 最小 fake（只为把真实中间件链跑起来） ----------

type pocClock struct{ t time.Time }

func (c pocClock) Now() time.Time { return c.t }

type pocUserRepo struct{ users map[int64]platform.User }

func (r pocUserRepo) Create(_ context.Context, _ string, _ platform.Role) (platform.User, error) {
	return platform.User{}, nil
}
func (r pocUserRepo) ByStudentNo(_ context.Context, studentNo string) (platform.User, error) {
	for _, u := range r.users {
		if u.StudentNo == studentNo {
			return u, nil
		}
	}
	return platform.User{}, platform.ErrNotFound
}
func (r pocUserRepo) ByID(_ context.Context, id int64) (platform.User, error) {
	if u, ok := r.users[id]; ok {
		return u, nil
	}
	return platform.User{}, platform.ErrNotFound
}
func (r pocUserRepo) SetStatus(context.Context, int64, platform.UserStatus) error { return nil }
func (r pocUserRepo) TouchLogin(context.Context, int64, string, string) error     { return nil }

type pocProfileRepo struct{ profiles map[int64]platform.Profile }

func (r pocProfileRepo) Upsert(_ context.Context, p platform.Profile) error {
	r.profiles[p.UserID] = p
	return nil
}
func (r pocProfileRepo) ByUserID(_ context.Context, id int64) (platform.Profile, error) {
	if p, ok := r.profiles[id]; ok {
		return p, nil
	}
	return platform.Profile{}, platform.ErrNotFound
}

type pocSessionStore struct{ sessions map[string]platform.Session }

func (s pocSessionStore) CreateSession(context.Context, platform.Session) error { return nil }
func (s pocSessionStore) SessionByID(_ context.Context, id string) (platform.Session, error) {
	if v, ok := s.sessions[id]; ok {
		return v, nil
	}
	return platform.Session{}, platform.ErrNotFound
}
func (s pocSessionStore) Revoke(context.Context, string) error { return nil }
func (s pocSessionStore) RevokeByUserScope(context.Context, int64, platform.SessionScope) (int, error) {
	return 0, nil
}

// pocApp 重建装配层的鉴权链，返回 engine 与「普通学生」的合法 Bearer 令牌。
func pocApp(t *testing.T, rateLimit int) (*gin.Engine, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	clock := pocClock{t: time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)}
	const authKey = "poc-auth-key-0123456789abcdef0123456789" // 测试专用，非真实凭据
	const victimStudentNo = "202600001"
	const attackerStudentNo = "202699999"

	users := pocUserRepo{users: map[int64]platform.User{
		1: {ID: 1, StudentNo: victimStudentNo, Role: platform.RoleUser, Status: platform.UserActive},
		2: {ID: 2, StudentNo: attackerStudentNo, Role: platform.RoleUser, Status: platform.UserActive},
	}}
	profiles := pocProfileRepo{profiles: map[int64]platform.Profile{
		1: {UserID: 1, Name: "受害者姓名", College: "受害者学院", Major: "受害者专业", ClassName: "受害者班级", Grade: "2026"},
		2: {UserID: 2, Name: "攻击者本人", College: "攻击者学院"},
	}}
	sessions := pocSessionStore{sessions: map[string]platform.Session{
		"attacker-session": {ID: "attacker-session", UserID: 2, Scope: platform.ScopeApp, ExpiresAt: clock.t.Add(time.Hour)},
	}}

	// 与 app.New 一致：server.New() 不传 trustedProxies → 不信任任何 XFF。
	engine := server.New()
	verifier := server.NewTokenVerifier(authKey, clock)
	resolver := server.NewSessionStoreResolver(sessions, users, clock)
	limiter := server.NewRateLimiter(clock, rateLimit, time.Minute)

	// 与 app.New 一致：全局先挂 ScopeApp 可吊销鉴权 + 限流。
	engine.Use(server.RevocableAuthMiddleware(verifier, resolver, server.ScopeApp),
		server.RateLimitMiddleware(limiter))

	userSvc := user.New(users, profiles)
	// 与修复后的装配一致：本人端点挂普通 engine。
	user.RegisterPublic(engine, userSvc, server.CurrentUserID)

	// 让 /identity/login 真实存在：请求穿过限流后返回 200「已处理」，
	// 与被 429 拦下形成锐利对比（否则 404 只是「路由不存在」，信号不够干净）。
	engine.POST("/identity/login", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"processed": true})
	})

	// 与修复后的装配一致：管理端点统一挂在 adminGroup（admin 会话 + role>=1）。
	adminGroup := engine.Group("/",
		server.RevocableAuthMiddleware(verifier, resolver, server.ScopeAdmin),
		server.AdminOnly(platform.RoleAdmin))
	user.RegisterAdmin(adminGroup, userSvc, profiles)

	token := verifier.IssueSession("attacker-session", clock.t.Add(time.Hour))
	return engine, token
}

// ---------------------------------------------------------------
// V1 回归：普通学生的 App 会话**不得**读任意学号画像
// ---------------------------------------------------------------

func TestZZPocLead1_AppSessionCannotReadAnyStudentProfile(t *testing.T) {
	engine, token := pocApp(t, 1000)

	// 攻击者（role=0）用自己的 scope=app 会话，查**受害者**的学号。
	req := httptest.NewRequest(http.MethodGet, "/identity/users/202600001", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	// 修复后必须被拒（401/403），且响应体不得泄露受害者画像字段。
	if w.Code == http.StatusOK {
		t.Fatalf("越权仍成立：普通 app 会话读到了管理端点，body=%s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "受害者姓名") || strings.Contains(w.Body.String(), "受害者专业") {
		t.Fatalf("响应体泄露了受害者画像：%s", w.Body.String())
	}

	// 对照组：本人端点仍可用，证明不是「把整个 user 域都关掉」。
	meW := httptest.NewRecorder()
	meReq := httptest.NewRequest(http.MethodGet, "/identity/me", nil)
	meReq.Header.Set("Authorization", "Bearer "+token)
	engine.ServeHTTP(meW, meReq)
	if meW.Code != http.StatusOK {
		t.Fatalf("本人端点不应受影响，实际 %d body=%s", meW.Code, meW.Body.String())
	}
}

// ---------------------------------------------------------------
// V2 回归：伪造 X-Forwarded-For **不得**绕过未认证限流
// ---------------------------------------------------------------

func TestZZPocLead2_XForwardedForCannotBypassUnauthenticatedRateLimit(t *testing.T) {
	engine, _ := pocApp(t, 3) // 同一 IP 每分钟最多 3 次
	url := "/identity/login"

	// ① 不带 XFF：第 4 次必须 429（证明限流本身生效）。
	var baseline []int
	for i := 0; i < 4; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "203.0.113.7:12345"
		engine.ServeHTTP(w, req)
		baseline = append(baseline, w.Code)
	}
	if baseline[3] != http.StatusTooManyRequests {
		t.Fatalf("前置条件失败：不限 XFF 时第 4 次应 429，实际 %v", baseline)
	}

	// ② 每次换一个伪造 X-Forwarded-For，同一真实 IP 连打 8 次。
	//    修复后 XFF 不被采信，key 仍按 RemoteAddr 计，第 4 次起必须 429。
	var spoofed []int
	for i := 0; i < 8; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "203.0.113.7:12345" // 真实 IP 完全不变
		req.Header.Set("X-Forwarded-For", "198.51.100."+strconv.Itoa(i+1))
		engine.ServeHTTP(w, req)
		spoofed = append(spoofed, w.Code)
	}
	for i := 3; i < len(spoofed); i++ {
		if spoofed[i] != http.StatusTooManyRequests {
			t.Fatalf("伪造 XFF 仍绕过了限流：第 %d 次应为 429，实际序列 %v", i+1, spoofed)
		}
	}
}

var _ = slog.New // 保持 import 稳定（harness 日志留空）
var _ = context.Background
