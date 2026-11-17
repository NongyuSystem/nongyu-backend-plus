package jiaowu_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
		Method  string `json:"method"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应体不是合法 JSON：%v，body=%s", err, w.Body.String())
	}
	if body.Target != "jiaowu" {
		t.Fatalf("响应体应含逻辑目标名，实际 target=%q", body.Target)
	}
	if body.Method != http.MethodGet {
		t.Fatalf("address 应带 method=GET，实际 %q", body.Method)
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

// TestLoginIsDeprecatedAndNeverStoresTicket 锁住 2026-11-17 的处置：
// /school/login 不再向上游换票、不再写票据存储——它当年写的是上游响应原文，
// 会覆盖 TGTManager 保存的 {TGT, CookieHeader}（cache_miss 全线 session=unavailable 的根因）。
// 端点保留形态（不 404），但一律回 session_expired，并留下 deprecate 审计。
func TestLoginIsDeprecatedAndNeverStoresTicket(t *testing.T) {
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	doer.SetResponder(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK",
			Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
			Header: make(http.Header), Body: io.NopCloser(strings.NewReader("TICKET-XYZ")),
			Request: req,
		}, nil
	})
	h, tickets, _, audit := newAuthEngine(t, doer)

	w := postJSON(t, h, "/school/login", map[string]string{
		"userID": "u1", "code": "1234", "uuid": "cap-1",
	})
	if w.Code == http.StatusOK {
		t.Fatalf("已停用的登录端点不应回 200，实际 body=%s", w.Body.String())
	}
	// 关键断言：存储里绝不能被写入任何东西（这正是它过去破坏 tgt 的方式）。
	if _, _, err := tickets.Get(nil, "test-user"); err == nil {
		t.Fatal("登录端点绝不能再写票据存储（会覆盖真正的 tgt）")
	}
	if !strings.Contains(audit.buf.String(), "deprecated_no_store") {
		t.Fatalf("应留下 deprecated_no_store 审计，实际：%s", audit.buf.String())
	}
	// 也不应真的出站打学校站。
	if got := len(doer.Requests()); got != 0 {
		t.Fatalf("停用后不应出站，实际 %d 次", got)
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
	// 端点已停用，但审计仍应记录「谁调了它」——这是观测老客户端是否还在用的唯一线索。
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

// TestLoginEndpointReturnsGoneAndRecordsAudit 已停用的换票端点回 410 + login_deprecated。
//
// 为什么要 410 而不是 401：401 会被客户端读成「登录态过期，去重新登录」，
// 而真实处置是「这个端点没了，换 /identity/login」。码不同，客户端动作才不同。
func TestLoginEndpointReturnsGoneAndRecordsAudit(t *testing.T) {
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	h, tickets, _, audit := newAuthEngine(t, doer)

	w := postJSON(t, h, "/school/login", map[string]string{
		"code": "1234", "uuid": "cap-x",
	})
	if w.Code != http.StatusGone {
		t.Fatalf("停用端点应回 410，实际 %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Code != "login_deprecated" {
		t.Fatalf("应回 code=login_deprecated，实际 %q", body.Code)
	}
	if _, _, err := tickets.Get(nil, "test-user"); err == nil {
		t.Fatal("停用端点绝不能写票据存储")
	}
	if !strings.Contains(audit.buf.String(), "deprecated_no_store") {
		t.Fatalf("应留下 deprecated_no_store 审计，实际：%s", audit.buf.String())
	}
	if got := len(doer.Requests()); got != 0 {
		t.Fatalf("停用端点不应出站，实际 %d 次", got)
	}
}

// TestLogoutDeletesTicket 退出登录后该用户票据记录被删除（幂等）。
//
// 2026-11-17 改：不再借 `POST /school/login` 造前置数据（它已停止落库）。
// 直接写入存储来验证 Logout 的删除语义本身。
func TestLogoutDeletesTicket(t *testing.T) {
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	h, tickets, _, _ := newAuthEngine(t, doer)

	// 前置：手工放一条记录，模拟「已登录并保管了 tgt」。
	if err := tickets.Put(nil, "test-user", []byte("sealed"), time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("前置写入失败：%v", err)
	}
	if _, _, err := tickets.Get(nil, "test-user"); err != nil {
		t.Fatalf("前置：记录应存在，%v", err)
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
	sched := jiaowu.NewRenewScheduler(tickets, clock).
		// 单测注入 no-op 等待：错峰延迟是秒级，测试不能真等。
		WithPacing(func(context.Context, time.Duration) bool { return true }, func() time.Duration { return 0 })

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

// ── 错峰执行守卫（2026-11-10）─────────────────────────────────────
//
// 背景：Plan 早就按 userID 哈希算出 16 个错峰槽位，但 RunOnce 一直直接同步
// 逐个 exec，**Offset 算了却没用上**——一轮 64 个候选仍会在同一瞬间全打出去，
// 对学校侧就是一次 64 并发尖峰。这两条测试锁住「槽位真的被用起来了」。

func TestSchedulerRunOnce_StaggersAcrossSlots(t *testing.T) {
	tickets := platform.NewMockTicketStore()
	clock := platform.NewMockClock(time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC))

	// 塞足够多的候选，确保散列后落在多个槽位。
	for i := 0; i < 30; i++ {
		u := fmt.Sprintf("u-%02d", i)
		if err := tickets.Put(nil, u, []byte("sealed"), clock.Now().Add(time.Hour)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	var mu sync.Mutex
	var waits []time.Duration
	sched := jiaowu.NewRenewScheduler(tickets, clock).
		WithPacing(
			func(_ context.Context, d time.Duration) bool {
				mu.Lock()
				waits = append(waits, d)
				mu.Unlock()
				return true
			},
			func() time.Duration { return 0 },
		)

	executed := 0
	count, err := sched.RunOnce(context.Background(), func(context.Context, string) { executed++ })
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if count != 30 || executed != 30 {
		t.Fatalf("应处理 30 个候选，count=%d executed=%d", count, executed)
	}

	mu.Lock()
	nWaits := len(waits)
	mu.Unlock()
	// 30 个候选按 16 槽散列，实际槽位数应在 2..16 之间（哈希不保证铺满）。
	// 关键断言：**确实发生过等待**（槽位之间被拉开），而不是一次性全跑完。
	if nWaits == 0 {
		t.Fatal("跨槽位时应至少等待一次（否则等于没有错峰）")
	}
	if nWaits >= count {
		t.Fatalf("等待次数(%d)不应达到候选数(%d)——槽位内应连续执行，只在槽位间等", nWaits, count)
	}
}

func TestSchedulerRunOnce_StopsOnContextCancel(t *testing.T) {
	tickets := platform.NewMockTicketStore()
	clock := platform.NewMockClock(time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC))
	for i := 0; i < 30; i++ {
		u := fmt.Sprintf("u-%02d", i)
		if err := tickets.Put(nil, u, []byte("sealed"), clock.Now().Add(time.Hour)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	// 第一次等待就取消：模拟关停时正在错峰。
	sched := jiaowu.NewRenewScheduler(tickets, clock).
		WithPacing(
			func(context.Context, time.Duration) bool {
				cancel()
				return false
			},
			func() time.Duration { return 0 },
		)

	executed := 0
	count, err := sched.RunOnce(ctx, func(context.Context, string) { executed++ })
	if err != nil {
		t.Fatalf("RunOnce 不该因取消而报错：%v", err)
	}
	// 已处理的必须小于总数：取消后不再继续对外发请求。
	if executed >= 30 || count >= 30 {
		t.Fatalf("ctx 取消后应停止剩余候选，实际 executed=%d count=%d", executed, count)
	}
}
