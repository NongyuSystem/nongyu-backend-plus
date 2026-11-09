package jiaowu_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school"
	"nongyu-backend-plus/internal/school/jiaowu"
)

// ---- from handler_test.go ----
// keyHexPrefix 是 WebVPN 编码地址的固定路径段前缀，逐字来自契约清单 §2。
// 白名单成功路径必须产出含该前缀的编码地址。
const keyHexPrefix = "77726476706e69737468656265737421"

// enforcedPolicy 是「出站闸口强制生效」的策略，用于 #14/#17 的闸口测试。
// 白名单覆盖三个真实目标 host；Enforce=true 使 platform 实现层的
// 白名单/超时/302/Set-Cookie 全部强制（ADR-0001 全系统唯一强制点）。
func enforcedPolicy() platform.DoerPolicy {
	return platform.DoerPolicy{
		Enforce:      true,
		AllowedHosts: []string{"jiaowu.sicau.edu.cn", "auth.sicau.edu.cn", "webvpn.sicau.edu.cn"},
	}
}

// newEngine 用「受控的 MockHTTPDoer」起一个只挂了教务子域路由的 gin 引擎。
// 这是 issue #13 要求的 HTTP 层唯一接缝：出站能力在这里被换成记录型假实现，
// 以便用 Requests() 断言「零出站」。
func newEngine(t *testing.T) (http.Handler, *platform.MockHTTPDoer) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	doer := platform.NewMockHTTPDoer(platform.DoerPolicy{})
	engine := gin.New()
	jiaowu.Register(engine, jiaowu.New(doer))
	return engine, doer
}

func doGet(t *testing.T, h http.Handler, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/school/address?"+values.Encode(), nil)
	h.ServeHTTP(w, req)
	return w
}

// TestAddressWhitelistReturnsEncodedAddressAndNoOutbound 锁住成功路径的三件事：
// 200、响应体含逻辑目标名与含 KEY_HEX 前缀的编码地址、以及零出站请求。
func TestAddressWhitelistReturnsEncodedAddressAndNoOutbound(t *testing.T) {
	h, doer := newEngine(t)
	w := doGet(t, h, url.Values{"target": {"jiaowu"}})

	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d，响应体：%s", w.Code, w.Body.String())
	}
	var body struct {
		Target  string `json:"target"`
		Address string `json:"address"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应体不是合法 JSON：%v，body=%s", err, w.Body.String())
	}
	if body.Target != "jiaowu" {
		t.Fatalf("响应体应含逻辑目标名，实际 target=%q", body.Target)
	}
	if !strings.Contains(body.Address, keyHexPrefix) {
		t.Fatalf("地址应含 WebVPN KEY_HEX 前缀，实际 address=%q", body.Address)
	}
	if !strings.HasPrefix(body.Address, "https://webvpn.sicau.edu.cn/https/"+keyHexPrefix) {
		t.Fatalf("编码地址形态不符，实际 address=%q", body.Address)
	}
	if got := len(doer.Requests()); got != 0 {
		t.Fatalf("Address 只返回地址、不得发起任何出站请求；成功路径也应零出站，实际记录到 %d 次：%#v", got, doer.Requests())
	}
}

// TestAddressAppendsOptionalPathInPlaintext 覆盖「可选查询条件」：path（含自带
// query 串）应以明文追加到编码地址上，且成功路径依旧零出站。
func TestAddressAppendsOptionalPathInPlaintext(t *testing.T) {
	h, doer := newEngine(t)
	w := doGet(t, h, url.Values{
		"target": {"jiaowu"},
		"path":   {"/jiaoshi/aspsso/caslogin.asp?ticket=ST"},
	})

	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d，响应体：%s", w.Code, w.Body.String())
	}
	var body struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应体不是合法 JSON：%v，body=%s", err, w.Body.String())
	}
	if !strings.Contains(body.Address, "/jiaoshi/aspsso/caslogin.asp?ticket=ST") {
		t.Fatalf("path/query 应以明文保持，实际 address=%q", body.Address)
	}
	if got := len(doer.Requests()); got != 0 {
		t.Fatalf("成功路径应零出站，实际记录到 %d 次：%#v", got, doer.Requests())
	}
}

// TestAddressDeniesNonWhitelistTargetsWithZeroOutbound 是「拒绝发生在闸口而非事后」
// 的可观测证据：未知逻辑名、把 URL 当名字传、把 host 当名字传，都必须是 400 +
// target_denied，且假出站实现记录到零请求。
func TestAddressDeniesNonWhitelistTargetsWithZeroOutbound(t *testing.T) {
	cases := []struct {
		name   string
		target string
	}{
		{"未知逻辑名", "unknown"},
		{"把 URL 当名字传", "https://evil.example.com/x"},
		{"把 host 当名字传", "jiaowu.sicau.edu.cn"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, doer := newEngine(t)
			w := doGet(t, h, url.Values{"target": {tc.target}})

			if w.Code != http.StatusBadRequest {
				t.Fatalf("期望 400，实际 %d，响应体：%s", w.Code, w.Body.String())
			}
			var body struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("响应体不是合法 JSON：%v，body=%s", err, w.Body.String())
			}
			if body.Code != "target_denied" {
				t.Fatalf("错误码应为 target_denied，实际 code=%q，body=%s", body.Code, w.Body.String())
			}
			if got := len(doer.Requests()); got != 0 {
				t.Fatalf("拒绝必须发生在闸口、不得有任何出站请求，实际记录到 %d 次：%#v", got, doer.Requests())
			}
		})
	}
}

// ---- from login_test.go ----
// #15 的 HTTP 层测试：换票主路径、票据加密保管、审计不含票、登出清除。
// 全部用假实现，不依赖真实学校网络。

// captureAudit 捕获结构化审计日志，用于断言「票据不出现在任何字段里」。
type captureAudit struct {
	buf bytes.Buffer
}

func (c *captureAudit) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (c *captureAudit) Handle(_ context.Context, r slog.Record) error {
	attrs := make([]string, 0)
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a.Key+"="+a.Value.String())
		return true
	})
	c.buf.WriteString(r.Message + " | " + strings.Join(attrs, ",") + "\n")
	return nil
}
func (c *captureAudit) WithAttrs(_ []slog.Attr) slog.Handler { return c }
func (c *captureAudit) WithGroup(_ string) slog.Handler      { return c }

func newAuthEngine(t *testing.T, doer *platform.MockHTTPDoer) (http.Handler, *platform.MockTicketStore, *platform.MockClock, *captureAudit) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	tickets := platform.NewMockTicketStore()
	clock := platform.NewMockClock(time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC))
	cipher := platform.NewMockCipher("test-key")
	audit := &captureAudit{}
	logger := slog.New(audit)
	engine := gin.New()
	// 测试用身份注入：模拟认证中间件往 context 写身份（生产由 server.AuthMiddleware 做）。
	// 明确只认头，不认请求体/query，避免测试掩盖冒用漏洞。
	engine.Use(func(c *gin.Context) {
		if u := c.GetHeader("X-Test-User"); u != "" {
			c.Set(school.CtxUserID, u)
		}
		c.Next()
	})
	jiaowu.RegisterWithAuth(engine, jiaowu.NewWithAuth(doer, tickets, cipher, clock, logger))
	return engine, tickets, clock, audit
}

func postJSON(t *testing.T, h http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User", "test-user")
	h.ServeHTTP(w, req)
	return w
}

// TestCaptchaReturnsImageAndUUID 取验证码接口返回可展示图像与事务标识。
func TestCaptchaReturnsImageAndUUID(t *testing.T) {
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	doer.SetResponse(http.StatusOK, "PNG-PLACEHOLDER")
	h, _, _, _ := newAuthEngine(t, doer)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/school/captcha?target=auth", nil)
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d，body=%s", w.Code, w.Body.String())
	}
	var body struct {
		UUID  string `json:"uuid"`
		Image string `json:"image"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("非法 JSON：%v", err)
	}
	if body.UUID == "" || body.Image == "" {
		t.Fatalf("应返回 uuid 与 image，实际 %+v", body)
	}
}

// TestLoginSuccessStoresAndReturnsTicket 正确验证码 → 换票成功，票据入外置存储、下发给本人。
func TestLoginSuccessStoresAndReturnsTicket(t *testing.T) {
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	doer.SetResponder(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK",
			Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
			Header: make(http.Header), Body: io.NopCloser(strings.NewReader("TICKET-XYZ")),
			Request: req,
		}, nil
	})
	h, tickets, clock, _ := newAuthEngine(t, doer)

	w := postJSON(t, h, "/school/login", map[string]string{
		"userID": "u1", "code": "1234", "uuid": "cap-1",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d，body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Ticket    string `json:"ticket"`
		ExpiresAt string `json:"expiresAt"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("非法 JSON：%v", err)
	}
	if body.Ticket == "" {
		t.Fatalf("应把票据下发给本人，实际 body=%s", w.Body.String())
	}
	// 存储里落的是「加密后的票据」，不应出现明文。
	sealed, expiresAt, err := tickets.Get(nil, "test-user")
	if err != nil {
		t.Fatalf("票据应入外置存储：%v", err)
	}
	if bytes.Contains(sealed, []byte("TICKET-XYZ")) {
		t.Fatalf("存储里应是密文，不得出现明文票据")
	}
	if expiresAt.IsZero() {
		t.Fatalf("过期时刻应随票据一起记录")
	}
	if !expiresAt.After(clock.Now()) {
		t.Fatalf("过期时刻应晚于当前时刻")
	}
}

// TestLoginAuditNeverContainsTicket 审计结构化字段中不含票据（无论明文或密文）。
func TestLoginAuditNeverContainsTicket(t *testing.T) {
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	doer.SetResponse(http.StatusOK, "SECRET-TICKET-VALUE")
	h, _, _, audit := newAuthEngine(t, doer)

	postJSON(t, h, "/school/login", map[string]string{
		"userID": "u2", "code": "1234", "uuid": "cap-2",
	})
	if strings.Contains(audit.buf.String(), "SECRET-TICKET-VALUE") {
		t.Fatalf("审计日志不得包含票据值，实际：%s", audit.buf.String())
	}
	if !strings.Contains(audit.buf.String(), "test-user") {
		t.Fatalf("审计应记录用户标识，实际：%s", audit.buf.String())
	}
}

// TestLoginBadCaptchaNoTicketStored 验证码错误 → 可区分错误，且存储不落任何票据。
func TestLoginBadCaptchaNoTicketStored(t *testing.T) {
	// 上游返回「验证码错误」信号：用带 code=500 的响应体表达。
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	doer.SetResponder(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK",
			Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
			Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":500,"msg":"验证码错误"}`)),
			Request: req,
		}, nil
	})
	h, tickets, _, _ := newAuthEngine(t, doer)

	w := postJSON(t, h, "/school/login", map[string]string{
		"userID": "u3", "code": "WRONG", "uuid": "cap-3",
	})
	if w.Code == http.StatusOK {
		t.Fatalf("错误验证码不应返回 200，实际 body=%s", w.Body.String())
	}
	if _, _, err := tickets.Get(nil, "test-user"); err == nil {
		t.Fatalf("验证码错误时存储里不应落任何票据")
	}
}

// TestLogoutDeletesTicket 退出登录后该用户票据记录被删除。
func TestLogoutDeletesTicket(t *testing.T) {
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	doer.SetResponse(http.StatusOK, "TICKET-LOGOUT")
	h, tickets, _, _ := newAuthEngine(t, doer)

	postJSON(t, h, "/school/login", map[string]string{
		"userID": "u4", "code": "1234", "uuid": "cap-4",
	})
	if _, _, err := tickets.Get(nil, "test-user"); err != nil {
		t.Fatalf("前置：登录应落票据，%v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/school/logout", nil)
	req.Header.Set("X-Test-User", "test-user")
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("登出期望 200，实际 %d，body=%s", w.Code, w.Body.String())
	}
	if _, _, err := tickets.Get(nil, "test-user"); err == nil {
		t.Fatalf("登出后票据记录应被删除")
	}
}

// TestAuditCoversAddressFetchCaptcha 证明「代请求」类入口也记审计（ADR 第 4 条），
// 且审计只记 userID/action/result/target 四个结构化字段，不含票据或响应体。
func TestAuditCoversAddressFetchCaptcha(t *testing.T) {
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	doer.SetResponse(http.StatusOK, "upstream-payload")
	h, _, _, audit := newAuthEngine(t, doer)

	for _, path := range []string{
		"/school/address?target=jiaowu",
		"/school/fetch?target=jiaowu",
		"/school/captcha?target=auth",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Test-User", "test-user")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Fatalf("%s 应已注册", path)
		}
	}
	log := audit.buf.String()
	for _, action := range []string{"address", "fetch", "captcha"} {
		if !strings.Contains(log, "action="+action) {
			t.Fatalf("审计应记录 action=%s，实际日志：%s", action, log)
		}
	}
	if !strings.Contains(log, "userID=test-user") {
		t.Fatalf("审计应记录已认证身份，实际：%s", log)
	}
	if strings.Contains(log, "upstream-payload") {
		t.Fatalf("审计不得记录上游响应体，实际：%s", log)
	}
	// 目标逻辑名必须真实：address/fetch 打的是 jiaowu，captcha 打的是 auth。
	if !strings.Contains(log, "target=jiaowu") {
		t.Fatalf("审计应记录真实目标逻辑名 jiaowu，实际：%s", log)
	}
	if !strings.Contains(log, "target=auth") {
		t.Fatalf("captcha 审计应记录目标 auth，实际：%s", log)
	}
}

// ---- from scheduler_test.go ----
// 定时续期循环的针对性测试：Plan/PlanningWindow 必须有生产调用点，
// 且循环可优雅关停、不静默重试刷屏。

func TestSchedulerRunOncePicksCandidatesFromObservedWindow(t *testing.T) {
	tickets := platform.NewMockTicketStore()
	clock := platform.NewMockClock(time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC))
	sched := jiaowu.NewRenewScheduler(tickets, clock)

	// 3 个即将过期用户，1 个远期用户（不该被挑中）。
	for _, u := range []string{"u-a", "u-b", "u-c"} {
		if err := tickets.Put(nil, u, []byte("sealed-"+u), clock.Now().Add(1*time.Hour)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if err := tickets.Put(nil, "u-far", []byte("sealed"), clock.Now().Add(30*24*time.Hour)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	var mu sync.Mutex
	var executed []string
	count, err := sched.RunOnce(context.Background(), func(ctx context.Context, u string) {
		mu.Lock()
		defer mu.Unlock()
		executed = append(executed, u)
	})
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if count != 3 {
		t.Fatalf("应挑出观测窗口内 3 个候选，实际 %d（executed=%v）", count, executed)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, u := range executed {
		if u == "u-far" {
			t.Fatalf("远期用户不应被挑中：%v", executed)
		}
	}
}

func TestSchedulerRunStopsCleanly(t *testing.T) {
	tickets := platform.NewMockTicketStore()
	clock := platform.NewMockClock(time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC))
	sched := jiaowu.NewRenewScheduler(tickets, clock)

	ctx, cancel := context.WithCancel(context.Background())
	var executed int64
	var mu sync.Mutex
	done := sched.Run(ctx, 10*time.Millisecond, func(_ context.Context, _ string) {
		mu.Lock()
		executed++
		mu.Unlock()
	})

	// 放一个候选，让循环有事可做。
	if err := tickets.Put(nil, "u-run", []byte("sealed"), clock.Now().Add(1*time.Hour)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	time.Sleep(35 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// 优雅退出
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Run 未在 ctx 取消后及时退出")
	}
	// Stop 幂等，二次调用不 panic。
	sched.Stop()
}
