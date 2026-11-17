package jiaowu

// 网关票保管与代取的测试。
//
// 断言只看外部可观察行为：HTTP 状态码、响应字段、是否产生上游请求、票是否被清。
// 不断言内部调用顺序——那些是实现细节。

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/platform"
)

// ── 内存版 WebVPNTicketStore ────────────────────────────────────────────

type memWebVPNStore struct {
	mu   sync.Mutex
	data map[string]string
	exp  map[string]time.Time
}

func newMemWebVPNStore() *memWebVPNStore {
	return &memWebVPNStore{data: map[string]string{}, exp: map[string]time.Time{}}
}

func (m *memWebVPNStore) Put(_ context.Context, userID, ticket string, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[userID] = ticket
	m.exp[userID] = expiresAt
	return nil
}

func (m *memWebVPNStore) Get(_ context.Context, userID string) (string, time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[userID]
	if !ok {
		return "", time.Time{}, ErrWebVPNTicketMissing
	}
	return v, m.exp[userID], nil
}

func (m *memWebVPNStore) Delete(_ context.Context, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, userID)
	delete(m.exp, userID)
	return nil
}

// ── 测试脚手架 ──────────────────────────────────────────────────────────

// scriptedWebVPNDoer 按 URL 返回预设响应，并记录请求（用于断言票是否被带上）。
type scriptedWebVPNDoer struct {
	mu       sync.Mutex
	status   int
	body     string
	location string
	requests []*http.Request
	cookies  []string
}

func (d *scriptedWebVPNDoer) Do(_ context.Context, req *http.Request) (*http.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.requests = append(d.requests, req)
	d.cookies = append(d.cookies, req.Header.Get("Cookie"))

	status := d.status
	if status == 0 {
		status = http.StatusOK
	}
	h := http.Header{}
	if d.location != "" {
		h.Set("Location", d.location)
	}
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       ioNopCloserString(d.body),
		Request:    req,
	}, nil
}

func (d *scriptedWebVPNDoer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.requests)
}

func (d *scriptedWebVPNDoer) lastCookie() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.cookies) == 0 {
		return ""
	}
	return d.cookies[len(d.cookies)-1]
}

type nopCloser struct{ *strings.Reader }

func (nopCloser) Close() error { return nil }

func ioNopCloserString(s string) nopCloser {
	return nopCloser{strings.NewReader(s)}
}

func newWebVPNEngine(t *testing.T, doer *scriptedWebVPNDoer) (*gin.Engine, *Service, *memWebVPNStore) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	svc := NewWithAuth(
		doer,
		platform.NewMockTicketStore(),
		platform.NewMockCipher("test-key"),
		platform.NewRealClock(),
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	)
	store := newMemWebVPNStore()
	svc.BindWebVPNTicketStore(store)

	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("authenticatedUserID", "user-1")
		c.Next()
	})
	RegisterWebVPN(engine, svc)
	return engine, svc, store
}

func doReq(t *testing.T, engine *gin.Engine, method, path, body string) (int, map[string]any) {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, r)
	out := map[string]any{}
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &out)
	}
	return w.Code, out
}

// ── 用例 ────────────────────────────────────────────────────────────────

// TestWebVPN_SubmitAndStatus 提交后能查到、且响应里没有票值。
func TestWebVPN_SubmitAndStatus(t *testing.T) {
	engine, _, _ := newWebVPNEngine(t, &scriptedWebVPNDoer{})

	code, body := doReq(t, engine, http.MethodPost, "/school/webvpn-session",
		`{"ticket":"43d5b3947913eab2"}`)
	if code != http.StatusOK {
		t.Fatalf("提交应成功，实际 %d body=%v", code, body)
	}
	if body["present"] != true {
		t.Fatalf("应回 present=true，实际 %v", body["present"])
	}
	if _, ok := body["ticket"]; ok {
		t.Fatal("提交响应绝不能回显票值")
	}

	code, body = doReq(t, engine, http.MethodGet, "/school/webvpn-session", "")
	if code != http.StatusOK || body["present"] != true {
		t.Fatalf("查询应回 present=true，实际 %d %v", code, body)
	}
	if _, ok := body["ticket"]; ok {
		t.Fatal("查询响应绝不能回显票值")
	}
}

// TestWebVPN_RejectsBadShape 形态不合法的票必须被拒，且不入库。
func TestWebVPN_RejectsBadShape(t *testing.T) {
	engine, _, store := newWebVPNEngine(t, &scriptedWebVPNDoer{})

	for _, bad := range []string{
		"",                       // 空
		"short",                  // 太短
		"has space in it here",   // 含空白
		`<a href="x">Found</a>`,  // HTML 片段（实测踩过的坑）
		"ticket\nwith\nnewline",  // 控制字符
	} {
		code, _ := doReq(t, engine, http.MethodPost, "/school/webvpn-session",
			`{"ticket":`+quote(bad)+`}`)
		if code == http.StatusOK {
			t.Fatalf("形态不合法的票 %q 不应被接受", bad)
		}
	}
	if _, _, err := store.Get(context.Background(), "user-1"); err == nil {
		t.Fatal("被拒的票不应入存储")
	}
}

// TestWebVPN_FetchWithoutTicket 没交票时代取回 409，且零出站。
func TestWebVPN_FetchWithoutTicket(t *testing.T) {
	doer := &scriptedWebVPNDoer{}
	engine, _, _ := newWebVPNEngine(t, doer)

	code, body := doReq(t, engine, http.MethodGet, "/school/jiaowu/cache/course/fetch", "")
	if code != http.StatusConflict {
		t.Fatalf("没有票应回 409，实际 %d body=%v", code, body)
	}
	if body["code"] != "webvpn_ticket_missing" {
		t.Fatalf("应回 webvpn_ticket_missing，实际 %v", body["code"])
	}
	if doer.count() != 0 {
		t.Fatalf("没有票时不应出站，实际 %d 次", doer.count())
	}
}

// TestWebVPN_FetchSuccessCarriesOnlyGatewayTicket 成功代取时只带网关票、透出上游内容。
func TestWebVPN_FetchSuccessCarriesOnlyGatewayTicket(t *testing.T) {
	doer := &scriptedWebVPNDoer{status: http.StatusOK, body: "<html>课表</html>"}
	engine, _, _ := newWebVPNEngine(t, doer)

	if code, _ := doReq(t, engine, http.MethodPost, "/school/webvpn-session",
		`{"ticket":"43d5b3947913eab2"}`); code != http.StatusOK {
		t.Fatal("前置：提交票失败")
	}

	req := httptest.NewRequest(http.MethodGet, "/school/jiaowu/cache/course/fetch", nil)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("代取应成功，实际 %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "课表") {
		t.Fatalf("应原样透出上游内容，实际 %q", w.Body.String())
	}
	ck := doer.lastCookie()
	if !strings.Contains(ck, "wengine_vpn_ticketwebvpn_sicau_edu_cn=43d5b3947913eab2") {
		t.Fatalf("应带上网关票，实际 Cookie=%q", ck)
	}
	// 关键：不得带 go-admin-tgt——校外链路不需要它，带上反而扩大暴露面。
	if strings.Contains(ck, "go-admin-tgt") {
		t.Fatalf("不得带 go-admin-tgt，实际 Cookie=%q", ck)
	}
}

// TestWebVPN_FetchExpiredClearsTicket 网关 302 回 /login 时判定票失效并清票。
func TestWebVPN_FetchExpiredClearsTicket(t *testing.T) {
	doer := &scriptedWebVPNDoer{
		status:   http.StatusFound,
		location: "https://webvpn.sicau.edu.cn/login",
	}
	engine, _, store := newWebVPNEngine(t, doer)

	if code, _ := doReq(t, engine, http.MethodPost, "/school/webvpn-session",
		`{"ticket":"43d5b3947913eab2"}`); code != http.StatusOK {
		t.Fatal("前置：提交票失败")
	}

	code, body := doReq(t, engine, http.MethodGet, "/school/jiaowu/cache/course/fetch", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("票失效应回 401，实际 %d body=%v", code, body)
	}
	if body["code"] != "webvpn_ticket_expired" {
		t.Fatalf("应回 webvpn_ticket_expired，实际 %v", body["code"])
	}
	if _, _, err := store.Get(context.Background(), "user-1"); err == nil {
		t.Fatal("失效的票必须被清掉，否则下次还会拿死票打学校站")
	}
}

// TestWebVPN_NonLoginRedirectKeepsTicket 其它 3xx（如教务登录闸门）不算票失效，不清票。
func TestWebVPN_NonLoginRedirectKeepsTicket(t *testing.T) {
	doer := &scriptedWebVPNDoer{
		status:   http.StatusFound,
		location: "https://webvpn.sicau.edu.cn/https/7772.../jiaoshi/bangong/index.asp",
	}
	engine, _, store := newWebVPNEngine(t, doer)

	if code, _ := doReq(t, engine, http.MethodPost, "/school/webvpn-session",
		`{"ticket":"43d5b3947913eab2"}`); code != http.StatusOK {
		t.Fatal("前置：提交票失败")
	}

	code, _ := doReq(t, engine, http.MethodGet, "/school/jiaowu/cache/course/fetch", "")
	if code != http.StatusFound {
		t.Fatalf("非登录跳转应原样透出，实际 %d", code)
	}
	if _, _, err := store.Get(context.Background(), "user-1"); err != nil {
		t.Fatal("教务内部跳转不应清票")
	}
}

// TestWebVPN_FetchNeedsInputTypeRejects 需要用户输入的类型无法由后端代取。
func TestWebVPN_FetchNeedsInputTypeRejects(t *testing.T) {
	doer := &scriptedWebVPNDoer{body: "x"}
	engine, _, _ := newWebVPNEngine(t, doer)

	if code, _ := doReq(t, engine, http.MethodPost, "/school/webvpn-session",
		`{"ticket":"43d5b3947913eab2"}`); code != http.StatusOK {
		t.Fatal("前置：提交票失败")
	}
	code, _ := doReq(t, engine, http.MethodGet, "/school/jiaowu/cache/classroomInfo/fetch", "")
	if code == http.StatusOK {
		t.Fatal("缺入参的类型不应代取成功")
	}
	if doer.count() != 0 {
		t.Fatalf("缺入参时不应出站，实际 %d 次", doer.count())
	}
}

// TestWebVPN_Clear 清票后再代取回 409。
func TestWebVPN_Clear(t *testing.T) {
	engine, _, _ := newWebVPNEngine(t, &scriptedWebVPNDoer{body: "x"})

	doReq(t, engine, http.MethodPost, "/school/webvpn-session", `{"ticket":"43d5b3947913eab2"}`)
	if code, _ := doReq(t, engine, http.MethodDelete, "/school/webvpn-session", ""); code != http.StatusOK {
		t.Fatalf("清票应成功，实际 %d", code)
	}
	code, _ := doReq(t, engine, http.MethodGet, "/school/jiaowu/cache/course/fetch", "")
	if code != http.StatusConflict {
		t.Fatalf("清票后应回 409，实际 %d", code)
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
