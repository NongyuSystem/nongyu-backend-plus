package flow_test

// #25 任务 2 / #21 M11 端点层：POST /identity/logout/all 的路由可达性、401、越权防护。
//
// 与 logoutall_test.go 的分工：那个测 service 层 LogoutAll 的行为；
// 本文件测 **HTTP 端点层**——路由是否挂上、未认证是否 401、
// 以及请求体里伪造 userID 是否会越权踢掉别人。
//
// seam：gin 路由 + HTTP 状态码（公共行为），不测 handler 内部。

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/identity/flow"
	"nongyu-backend-plus/internal/identity/login"
	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/server"
)

const (
	noLogoutAllHTTP = "888100021"
	noLogoutOther   = "888100022"
)

// newLogoutAllEngine 构造一个只挂 flow 路由、并用假中间件注入身份的 gin 引擎。
//
// 假中间件模拟 RevocableAuthMiddleware 的产物：把 userID 写进 CtxUserID。
// uid 为 0 表示「未认证」——不写 context，模拟无令牌请求。
func newLogoutAllEngine(t *testing.T, svc *flow.Service, uid int64) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if uid != 0 {
			c.Set(server.CtxUserID, formatInt(uid))
		}
		c.Next()
	})
	// issuer 传 nil：本文件不测令牌签发，只测 logout/all 端点。
	flow.Register(r, svc, nil)
	return r
}

func formatInt(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// 未认证访问 /identity/logout/all 必须 401，且不得误吊销任何会话。
func TestLogoutAllEndpoint_UnauthenticatedIs401(t *testing.T) {
	f := setupFlow(t, fullIdent(noLogoutAllHTTP))
	engine := newLogoutAllEngine(t, f.flow, 0) // uid=0 → 未认证

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/identity/logout/all", nil))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("未认证访问 /identity/logout/all 应 401，实际 %d body=%s", w.Code, w.Body.String())
	}
}

// 已认证时端点可达并能吊销本人全部会话。
func TestLogoutAllEndpoint_RevokesOwnSessions(t *testing.T) {
	f := setupFlow(t, fullIdent(noLogoutAllHTTP))
	ctx := context.Background()

	// 登录建号并拿到 userID + 第一条会话
	signed, err := f.flow.Login(ctx, flow.Params{
		Method:     "school",
		DeviceID:   "dev-A",
		SchoolCred: login.SchoolCredential{Method: "password", StudentNo: noLogoutAllHTTP, Password: "school-pw"},
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	// 再插第二条同 scope 会话（模拟多设备残留）
	now := time.Now()
	if err := f.store.CreateSession(ctx, platform.Session{
		ID: "http-logout-all-2", UserID: signed.User.ID,
		Scope: platform.ScopeApp, DeviceID: "dev-B",
		ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	engine := newLogoutAllEngine(t, f.flow, signed.User.ID)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/identity/logout/all", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("已认证时应 200，实际 %d body=%s", w.Code, w.Body.String())
	}
	// 响应里应带吊销条数
	var body struct {
		OK      bool `json:"ok"`
		Revoked int  `json:"revoked"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if !body.OK || body.Revoked < 2 {
		t.Fatalf("应吊销 ≥2 条会话，实际 %+v", body)
	}

	// 两条会话都已失效
	for _, id := range []string{signed.SessionID, "http-logout-all-2"} {
		got, err := f.store.SessionByID(ctx, id)
		if err != nil {
			t.Fatalf("查会话 %s：%v", id, err)
		}
		if got.RevokedAt == nil {
			t.Fatalf("会话 %s 应已被吊销", id)
		}
	}
}

// 越权防护：请求体里伪造 userID 不得踢掉别人，只能吊销自己。
func TestLogoutAllEndpoint_ForgedUserIDInBodyDoesNotAffectOthers(t *testing.T) {
	victim := noLogoutOther
	fv := setupFlow(t, fullIdent(victim))
	ctx := context.Background()

	// 受害者登录，留一条有效会话
	vs, err := fv.flow.Login(ctx, flow.Params{
		Method: "school", DeviceID: "victim-dev",
		SchoolCred: login.SchoolCredential{Method: "password", StudentNo: victim, Password: "school-pw"},
	})
	if err != nil {
		t.Fatalf("受害者登录：%v", err)
	}

	// 攻击者登录（另一个用户），拿到自己的 userID
	attackerNo := noLogoutAllHTTP
	fa := setupFlow(t, fullIdent(attackerNo))
	as, err := fa.flow.Login(ctx, flow.Params{
		Method: "school", DeviceID: "attacker-dev",
		SchoolCred: login.SchoolCredential{Method: "password", StudentNo: attackerNo, Password: "school-pw"},
	})
	if err != nil {
		t.Fatalf("攻击者登录：%v", err)
	}

	// 攻击者调用端点，请求体里伪造受害者的 userID
	forged, _ := json.Marshal(map[string]any{"userID": vs.User.ID, "userId": vs.User.ID})
	engine := newLogoutAllEngine(t, fa.flow, as.User.ID)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/identity/logout/all", bytes.NewReader(forged))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("攻击者调自己的端点应 200，实际 %d", w.Code)
	}

	// 受害者的会话必须仍然有效（身份只从 context 取）
	got, err := fv.store.SessionByID(ctx, vs.SessionID)
	if err != nil {
		t.Fatalf("查受害者会话：%v", err)
	}
	if got.RevokedAt != nil {
		t.Fatal("伪造请求体 userID 不得吊销他人会话（身份只应从 context 取）")
	}
}
