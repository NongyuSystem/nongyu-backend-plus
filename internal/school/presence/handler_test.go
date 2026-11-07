package presence_test

// HTTP 层测试（package presence_test，外部测试包）：用 httptest + gin 起引擎，
// 只挂 presence.Register 登记的路由，不 import internal/app（装配层接线由 Lead 合并）。
// 时间源用 platform.MockClock.Set 推进，断言「上报后判定改变 / 陈旧后回落」。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school"
	"nongyu-backend-plus/internal/school/presence"
)

const presencePath = "/school/presence"

// newEngine 起一个只挂了 presence 子域路由的 gin 引擎，回报 store/clock 供断言。
// 引擎里额外挂一个 /healthz 存活探针（模拟装配层的 server.New()，但不起 app.New()），
// 用来锁住「注册业务路由后存活探针仍绿」这一既有契约。
func newEngine(t *testing.T) (http.Handler, *platform.MockTicketStore, *platform.MockClock) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	gin.DefaultWriter = io.Discard
	gin.DefaultErrorWriter = io.Discard
	store := platform.NewMockTicketStore()
	clock := platform.NewMockClock(baseTime)
	svc := presence.New(store, clock, presence.ModeDirect, testStaleAfter)
	engine := gin.New()
	// 测试用身份注入：模拟认证中间件往 context 写身份，只认头、不认 body/query。
	engine.Use(func(c *gin.Context) {
		if u := c.GetHeader("X-Test-User"); u != "" {
			c.Set(school.CtxUserID, u)
		}
		c.Next()
	})
	engine.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	presence.Register(engine, svc)
	return engine, store, clock
}

func doJSON(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求体失败：%v", err)
		}
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	req.Header.Set("X-Test-User", "test-presence-user")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// decoded 统一解析响应体，便于断言 mode / source / 错误码。
func decoded(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("响应体不是合法 JSON：%v，body=%s", err, w.Body.String())
	}
	return m
}

// TestHTTPReportThenDecideChangesAndFallsBackWhenStale：本票最核心的 HTTP 层验收。
// 客户端 POST 上报 vpn → GET 判定应变为 vpn、且响应标注来源 client；
// 用 MockClock.Set 推进到陈旧阈值之外 → 判定回落后端配置 direct、来源变为 backend。
func TestHTTPReportThenDecideChangesAndFallsBackWhenStale(t *testing.T) {
	h, _, clock := newEngine(t)

	w := doJSON(t, h, http.MethodPost, presencePath, map[string]string{
		"userID": "u-http", "mode": "vpn",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("上报应返回 200，实际 %d，body=%s", w.Code, w.Body.String())
	}

	w = doJSON(t, h, http.MethodGet, presencePath+"?userID=u-http", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("判定应返回 200，实际 %d，body=%s", w.Code, w.Body.String())
	}
	body := decoded(t, w)
	if body["mode"] != string(presence.ModeVPN) || body["source"] != "client" {
		t.Fatalf("上报后判定应变为 vpn/client，实际 body=%v", body)
	}

	// 推进到阈值之外：上报超过陈旧阈值未更新 → 退回后端配置。
	clock.Set(baseTime.Add(testStaleAfter + time.Second))
	w = doJSON(t, h, http.MethodGet, presencePath+"?userID=u-http", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("陈旧后判定应返回 200，实际 %d，body=%s", w.Code, w.Body.String())
	}
	body = decoded(t, w)
	if body["mode"] != string(presence.ModeDirect) || body["source"] != "backend" {
		t.Fatalf("陈旧后应回落后端配置 direct/backend，实际 body=%v", body)
	}
}

// TestHTTPNoReportUsesBackendConfig：从未上报的用户，GET 判定直接用后端启动配置。
func TestHTTPNoReportUsesBackendConfig(t *testing.T) {
	h, _, _ := newEngine(t)
	w := doJSON(t, h, http.MethodGet, presencePath+"?userID=u-never", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("判定应返回 200，实际 %d，body=%s", w.Code, w.Body.String())
	}
	body := decoded(t, w)
	if body["mode"] != string(presence.ModeDirect) || body["source"] != "backend" {
		t.Fatalf("无上报应使用后端配置 direct/backend，实际 body=%v", body)
	}
}

// TestHTTPReportLandsInTicketStoreAndHealthzStaysGreen：上报状态确实落进外置
// TicketStore（按用户键控，expiresAt 零值 = 会话级），且 /healthz 仍绿。
func TestHTTPReportLandsInTicketStoreAndHealthzStaysGreen(t *testing.T) {
	h, store, clock := newEngine(t)

	w := doJSON(t, h, http.MethodPost, presencePath, map[string]string{
		"userID": "test-presence-user", "mode": "vpn",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("上报应返回 200，实际 %d，body=%s", w.Code, w.Body.String())
	}
	sealed, expiresAt, err := store.Get(context.Background(), "test-presence-user")
	if err != nil {
		t.Fatalf("上报状态应落在 TicketStore，Get 失败：%v", err)
	}
	if !expiresAt.IsZero() {
		t.Fatalf("presence 记录应为会话级（expiresAt 零值），实际 %v", expiresAt)
	}
	var got presence.ClientPresence
	if err := json.Unmarshal(sealed, &got); err != nil {
		t.Fatalf("存储内容应可反解：%v，sealed=%q", err, sealed)
	}
	if got.UserID != "test-presence-user" || got.Mode != presence.ModeVPN || !got.At.Equal(clock.Now()) {
		t.Fatalf("存储内容与上报不符：%#v（clock=%v）", got, clock.Now())
	}

	// 既有存活探针契约仍绿。
	w = doJSON(t, h, http.MethodGet, "/healthz", nil)
	if w.Code != http.StatusOK || w.Body.String() != `{"status":"ok"}` {
		t.Fatalf("/healthz 应保持绿，实际 %d / %s", w.Code, w.Body.String())
	}
}

// TestHTTPBadRequestsAreRejected：非法 JSON / 非法 mode / 空 userID / GET 缺 userID
// 都应在 HTTP 层被明确拒绝（400），且不写出任何状态。
func TestHTTPBadRequestsAreRejected(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"非法 mode", http.MethodPost, presencePath, map[string]string{"userID": "attacker", "mode": "bogus"}},
		{"POST 缺 mode", http.MethodPost, presencePath, map[string]string{"userID": "attacker"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newEngine(t)
			w := doJSON(t, h, tc.method, tc.path, tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("期望 400，实际 %d，body=%s", w.Code, w.Body.String())
			}
			body := decoded(t, w)
			if body["code"] != "invalid_presence" {
				t.Fatalf("错误码应为 invalid_presence，实际 body=%v", body)
			}
		})
	}

	// 无认证身份一律 401，且 body/query 里的 userID 不能顶替身份。
	t.Run("无身份头", func(t *testing.T) {
		h, _, _ := newEngine(t)
		req := httptest.NewRequest(http.MethodPost, presencePath,
			bytes.NewBufferString(`{"userID":"attacker","mode":"direct"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("缺认证身份应 401，实际 %d", w.Code)
		}
	})
}

// TestHTTPReportUsesAuthenticatedIdentity 证明身份来自认证上下文，
// body/query 里的 userID 一律不生效（ADR 第 1 条），且 query 无法覆盖身份。
func TestHTTPReportUsesAuthenticatedIdentity(t *testing.T) {
	h, store, _ := newEngine(t)
	w := doJSON(t, h, http.MethodPost, presencePath, map[string]string{
		"userID": "attacker", "mode": "vpn",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("上报应 200，实际 %d", w.Code)
	}
	// 只有认证身份的键被写入，attacker 键不存在。
	if _, _, err := store.Get(context.Background(), "attacker"); err == nil {
		t.Fatal("body 里的 attacker userID 不应被写入存储")
	}
	if _, _, err := store.Get(context.Background(), "test-presence-user"); err != nil {
		t.Fatalf("认证身份应被写入存储：%v", err)
	}
	// GET 带 ?userID=attacker 也不能改变判定所用的身份。
	w = doJSON(t, h, http.MethodGet, presencePath+"?userID=attacker", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET 应 200，实际 %d", w.Code)
	}
}

// TestHTTPMalformedJSONBodyIsRejected：请求体不是合法 JSON 也按入参错误处理。
func TestHTTPMalformedJSONBodyIsRejected(t *testing.T) {
	h, _, _ := newEngine(t)
	req := httptest.NewRequest(http.MethodPost, presencePath, bytes.NewBufferString("{not-json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User", "test-presence-user")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应返回 400，实际 %d，body=%s", w.Code, w.Body.String())
	}
	if got := decoded(t, w)["code"]; got != "invalid_presence" {
		t.Fatalf("错误码应为 invalid_presence，实际 %v", w.Body.String())
	}
}

// TestHTTPDecideAfterNetworkSwitch：模拟「网络切换时再上报一次」——直接上报
// 相反模式，判定应立刻翻转（direct ↔ vpn）。
func TestHTTPDecideAfterNetworkSwitch(t *testing.T) {
	h, _, _ := newEngine(t)
	for _, want := range []string{string(presence.ModeVPN), string(presence.ModeDirect)} {
		w := doJSON(t, h, http.MethodPost, presencePath, map[string]string{"userID": "u-switch", "mode": want})
		if w.Code != http.StatusOK {
			t.Fatalf("上报 %s 应 200，实际 %d，body=%s", want, w.Code, w.Body.String())
		}
		w = doJSON(t, h, http.MethodGet, presencePath+"?userID=u-switch", nil)
		if got := decoded(t, w)["mode"]; got != want {
			t.Fatalf("网络切换后判定应为 %s，实际 %v，body=%s", want, got, w.Body.String())
		}
	}
}
