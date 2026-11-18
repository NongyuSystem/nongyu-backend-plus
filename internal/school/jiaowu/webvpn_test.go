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
	"nongyu-backend-plus/internal/school"
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
//
// 2026-11-18：/fetch 取数前会先用编码 caslogin.asp 建教务会话。
// 含 caslogin.asp 的请求一律回 200（表示三跳完成），其余请求才用预设响应，
// 这样原有「目标页」断言不被 CAS 请求干扰。
type scriptedWebVPNDoer struct {
	mu        sync.Mutex
	status    int
	body      string
	location  string
	requests  []*http.Request
	cookies   []string
	casStatus int // 非 0 时覆盖 CAS 请求的返回码（供测 CAS 失败分支）
}

func (d *scriptedWebVPNDoer) Do(_ context.Context, req *http.Request) (*http.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.requests = append(d.requests, req)
	d.cookies = append(d.cookies, req.Header.Get("Cookie"))

	// CAS 建会话请求：默认直接 200 完成。
	if strings.Contains(req.URL.Path, "caslogin.asp") {
		st := http.StatusOK
		if d.casStatus != 0 {
			st = d.casStatus
		}
		return &http.Response{
			StatusCode: st,
			Header:     http.Header{},
			Body:       ioNopCloserString(""),
			Request:    req,
		}, nil
	}

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

// targetCookies 只返回「非 CAS」请求的 Cookie（用于断言目标页只带网关票）。
func (d *scriptedWebVPNDoer) targetCookies() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := []string{}
	for i, r := range d.requests {
		if !strings.Contains(r.URL.Path, "caslogin.asp") && i < len(d.cookies) {
			out = append(out, d.cookies[i])
		}
	}
	return out
}

// count 返回总请求数（含 CAS 建会话请求）。
func (d *scriptedWebVPNDoer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.requests)
}

func (d *scriptedWebVPNDoer) lastCookie() string {
	cks := d.targetCookies()
	if len(cks) == 0 {
		return ""
	}
	return cks[len(cks)-1]
}

type nopCloser struct{ *strings.Reader }

func (nopCloser) Close() error { return nil }

func ioNopCloserString(s string) nopCloser {
	return nopCloser{strings.NewReader(s)}
}

// fakeJiaowuSTIssuer 给 /fetch 的 CAS 建会话步骤提供 st。
type fakeJiaowuSTIssuer struct {
	st  string
	err error
}

func (f *fakeJiaowuSTIssuer) IssueJiaowuST(context.Context, string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if f.st == "" {
		return "ST-FAKE-FOR-TEST", nil
	}
	return f.st, nil
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
	// /fetch 取数前要先用 st 建网关侧教务会话。
	svc.BindJiaowuSTIssuer(&fakeJiaowuSTIssuer{})
	// cache_miss 需要缓存未命中：绑内存缓存 + schema 校验器，走完整 GET 路径。
	svc.BindCache(newMemCache(func() time.Time { return time.Now() }))
	validator, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("编译 schema 失败: %v", err)
	}
	svc.BindSchemaValidator(validator)

	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set(school.CtxUserID, "user-1")
		c.Next()
	})
	RegisterWebVPN(engine, svc)
	// 同一 svc 也挂缓存路由：cache_miss 与 /fetch 共用票状态，正是缺陷所在的那条链路。
	RegisterCache(engine, svc)
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
		"",                      // 空
		"short",                 // 太短
		"has space in it here",  // 含空白
		`<a href="x">Found</a>`, // HTML 片段（实测踩过的坑）
		"ticket\nwith\nnewline", // 控制字符
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

// TestWebVPN_CacheMissIsZeroOutbound 公共页分流到取数账号池后，学生侧
// cache_miss 保持零出站：教务会话在交票时预热一次即可，不再每次 miss 重跑 CAS。
func TestWebVPN_CacheMissIsZeroOutbound(t *testing.T) {
	doer := &scriptedWebVPNDoer{body: "<html>ok</html>"}
	engine, _, _ := newWebVPNEngine(t, doer)
	doReq(t, engine, http.MethodPost, "/school/webvpn-session",
		`{"ticket":"43d5b3947913eab2"}`)

	casCount := func() int {
		doer.mu.Lock()
		defer doer.mu.Unlock()
		n := 0
		for _, r := range doer.requests {
			if strings.Contains(r.URL.Path, "caslogin.asp") {
				n++
			}
		}
		return n
	}
	before := casCount()
	if before == 0 {
		t.Fatal("交票时应已做一次 CAS 预热")
	}
	cacheMissBody(t, engine)
	cacheMissBody(t, engine)
	if got := casCount(); got != before {
		t.Fatalf("cache_miss 不应新增 CAS 出站（before=%d after=%d）", before, got)
	}
}

// TestWebVPN_SubmitPrewarmsJiaowuSession 交票时要用 st 回投编码 caslogin 预热
// 网关侧教务会话。这是「客户端拿 cache_miss 的 address+cookies 直连就能拿到
// 课表」的前提——预热只在交票做一次，cache_miss 保持零出站。
func TestWebVPN_SubmitPrewarmsJiaowuSession(t *testing.T) {
	doer := &scriptedWebVPNDoer{body: "<html>ok</html>"}
	engine, _, _ := newWebVPNEngine(t, doer)

	code, _ := doReq(t, engine, http.MethodPost, "/school/webvpn-session",
		`{"ticket":"43d5b3947913eab2"}`)
	if code != http.StatusOK {
		t.Fatalf("交票应成功，实际 %d", code)
	}
	doer.mu.Lock()
	defer doer.mu.Unlock()
	casSeen := false
	for i, r := range doer.requests {
		if strings.Contains(r.URL.Path, "caslogin.asp") {
			casSeen = true
			if !strings.Contains(doer.cookies[i], "wengine_vpn_ticketwebvpn_sicau_edu_cn=43d5b3947913eab2") {
				t.Fatalf("CAS 预热必须带网关票，实际 %q", doer.cookies[i])
			}
		}
	}
	if !casSeen {
		t.Fatal("交票未做 CAS 预热，客户端直连会拿到 302/登录壳")
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
// TestWebVPN_FetchExpiredMarksTicket 网关 302 回 /login 时判定票失效。
//
// 现在改为写「失效哨兵」而不是删记录：删了就只能报 missing，客户端会把
// 「登录过、票死了」误当成「从没登录过」，弹窗引导动作会错。
func TestWebVPN_FetchExpiredMarksTicket(t *testing.T) {
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
	// 记录还在，但已换成失效哨兵：后续 cache_miss 报 expired 而非 missing。
	stored, _, err := store.Get(context.Background(), "user-1")
	if err != nil || !isExpiredSentinel(stored) {
		t.Fatalf("失效后应留下哨兵，实际 stored=%q err=%v", stored, err)
	}
	// 判死后再代取，仍然回 401，且不会拿死票再次打学校站。
	doer.mu.Lock()
	before := len(doer.requests)
	doer.mu.Unlock()
	code, _ = doReq(t, engine, http.MethodGet, "/school/jiaowu/cache/course/fetch", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("判死后再次代取应仍回 401，实际 %d", code)
	}
	doer.mu.Lock()
	after := len(doer.requests)
	doer.mu.Unlock()
	if after != before {
		t.Fatalf("失效哨兵不应再出站打学校站，实际多了 %d 次", after-before)
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
	// 提交票本身现在会做一次 CAS 预热出站；记录基线，之后断言 /fetch 没有新增请求。
	before := doer.count()
	code, _ := doReq(t, engine, http.MethodGet, "/school/jiaowu/cache/classroomInfo/fetch", "")
	if code == http.StatusOK {
		t.Fatal("缺入参的类型不应代取成功")
	}
	if doer.count() != before {
		t.Fatalf("缺入参时 /fetch 不应出站，实际新增 %d 次", doer.count()-before)
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

// ── 缺陷回归：vpn 形态的 cookies 必须带上保管库里的网关票 ────────────

// cacheMissBody 发一个带 vpn 头的 cache GET，回响应体（辅助）。
func cacheMissBody(t *testing.T, engine *gin.Engine) map[string]any {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/school/jiaowu/cache/course", nil)
	r.Header.Set("X-Nongyu-Campus-Network", "vpn")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, r)
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return out
}

// cookiesOf 取响应里的 cookies 数组。
func cookiesOf(body map[string]any) []any {
	cs, _ := body["cookies"].([]any)
	return cs
}

// TestWebVPN_CacheMissNoTicket 没交过票：cookies 为空、session=unavailable。
// 客户端拿不到票，请求编码地址会被 302 回 /login，由它自己判 302 弹窗。
func TestWebVPN_CacheMissNoTicket(t *testing.T) {
	engine, _, _ := newWebVPNEngine(t, &scriptedWebVPNDoer{})
	body := cacheMissBody(t, engine)
	if body["code"] != "cache_miss" {
		t.Fatalf("应仍是 cache_miss，实际 %v", body["code"])
	}
	if len(cookiesOf(body)) != 0 {
		t.Fatalf("没票时 cookies 应为空，实际 %v", body["cookies"])
	}
	if body["session"] != "unavailable" {
		t.Fatalf("没票应 session=unavailable，实际 %v", body["session"])
	}
	if _, ok := body["webvpn"]; ok {
		t.Fatal("不再下发 webvpn 状态字段（弹窗靠客户端判 302）")
	}
}

// TestWebVPN_CacheMissCarriesGatewayTicket 交过票：cookies 必须带 wengine_vpn_ticket。
// 这是本次修复的核心——原来恒为空，客户端/代取链拿不到票。
func TestWebVPN_CacheMissCarriesGatewayTicket(t *testing.T) {
	engine, _, _ := newWebVPNEngine(t, &scriptedWebVPNDoer{})
	doReq(t, engine, http.MethodPost, "/school/webvpn-session",
		`{"ticket":"43d5b3947913eab2"}`)

	body := cacheMissBody(t, engine)
	cs := cookiesOf(body)
	if len(cs) != 1 {
		t.Fatalf("交过票后 cookies 应有 1 项，实际 %v", body["cookies"])
	}
	c, _ := cs[0].(map[string]any)
	if c["name"] != gatewayTicketCookieName {
		t.Fatalf("cookie 名应为 %s，实际 %v", gatewayTicketCookieName, c["name"])
	}
	if c["value"] != "43d5b3947913eab2" {
		t.Fatalf("cookie 值应为保管库里的票，实际 %v", c["value"])
	}
	if body["session"] != "ok" {
		t.Fatalf("有票应 session=ok（与 direct 同构），实际 %v", body["session"])
	}
	// 地址仍须是编码形态
	addr, _ := body["address"].(string)
	if !strings.Contains(addr, "webvpn.sicau.edu.cn") {
		t.Fatalf("地址应为编码形态，实际 %q", addr)
	}
}

// TestWebVPN_CacheMissExpiredSentinelNoTicket /fetch 判死后 cookies 为空（哨兵不下发）。
func TestWebVPN_CacheMissExpiredSentinelNoTicket(t *testing.T) {
	engine, svc, _ := newWebVPNEngine(t, &scriptedWebVPNDoer{})
	doReq(t, engine, http.MethodPost, "/school/webvpn-session",
		`{"ticket":"43d5b3947913eab2"}`)
	svc.markWebVPNTicketExpired(context.Background(), "user-1")
	body := cacheMissBody(t, engine)
	if len(cookiesOf(body)) != 0 {
		t.Fatalf("失效哨兵不应下发成 cookie，实际 %v", body["cookies"])
	}
	if body["session"] != "unavailable" {
		t.Fatalf("哨兵状态应 session=unavailable，实际 %v", body["session"])
	}
}

// TestWebVPN_ResubmitRestoresTicket 重新登录交票后 cookies 重新带上票。
func TestWebVPN_ResubmitRestoresTicket(t *testing.T) {
	engine, svc, _ := newWebVPNEngine(t, &scriptedWebVPNDoer{})
	doReq(t, engine, http.MethodPost, "/school/webvpn-session",
		`{"ticket":"43d5b3947913eab2"}`)
	svc.markWebVPNTicketExpired(context.Background(), "user-1")
	doReq(t, engine, http.MethodPost, "/school/webvpn-session",
		`{"ticket":"a1b2c3d4e5f60718"}`)
	body := cacheMissBody(t, engine)
	cs := cookiesOf(body)
	if len(cs) != 1 {
		t.Fatalf("重交票后 cookies 应恢复，实际 %v", body["cookies"])
	}
	c, _ := cs[0].(map[string]any)
	if c["value"] != "a1b2c3d4e5f60718" {
		t.Fatalf("应带新票，实际 %v", c["value"])
	}
}

// ── 缺陷回归：/fetch 必须先建网关侧教务会话 ────────────────────────────

// TestWebVPN_FetchEstablishesJiaowuSession /fetch 取数前要用 st 回投编码 caslogin。
//
// 实测（2026-11-18）：只带网关票打课表页会 302 / 登录壳；补 CAS 三跳后
// 才能拿到 32848 字节真实课表。教务会话绑在网关服务端，客户端零 cookie。
func TestWebVPN_FetchEstablishesJiaowuSession(t *testing.T) {
	doer := &scriptedWebVPNDoer{body: "<html>真实课表</html>"}
	engine, _, _ := newWebVPNEngine(t, doer)
	doReq(t, engine, http.MethodPost, "/school/webvpn-session",
		`{"ticket":"43d5b3947913eab2"}`)

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/school/jiaowu/cache/course/fetch", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("代取应成功，实际 %d body=%s", w.Code, w.Body.String())
	}
	// 至少一次请求是编码后的 caslogin.asp，且带了网关票。
	doer.mu.Lock()
	defer doer.mu.Unlock()
	casSeen := false
	for i, r := range doer.requests {
		if strings.Contains(r.URL.Path, "caslogin.asp") {
			casSeen = true
			if !strings.Contains(doer.cookies[i], "wengine_vpn_ticketwebvpn_sicau_edu_cn=43d5b3947913eab2") {
				t.Fatalf("CAS 请求必须带网关票，实际 %q", doer.cookies[i])
			}
			if !strings.Contains(r.URL.RawQuery, "ticket=") || !strings.Contains(r.URL.RawQuery, "appid=") {
				t.Fatalf("CAS 请求应带 appid 与 st，实际 query=%s", r.URL.RawQuery)
			}
		}
	}
	if !casSeen {
		t.Fatal("未发出 caslogin 建会话请求（需登录页会拿到登录壳）")
	}
}

// TestWebVPN_FetchRejectsLoginShell 教务把未登录渲染成 200 登录壳时，必须明确报错，
// 绝不能当成功页面透给客户端去解析（这正是之前静默失败的根因）。
func TestWebVPN_FetchRejectsLoginShell(t *testing.T) {
	doer := &scriptedWebVPNDoer{body: `<html><title>川农e认证 - 统一身份认证</title></html>`}
	engine, _, _ := newWebVPNEngine(t, doer)
	doReq(t, engine, http.MethodPost, "/school/webvpn-session",
		`{"ticket":"43d5b3947913eab2"}`)

	code, body := doReq(t, engine, http.MethodGet, "/school/jiaowu/cache/course/fetch", "")
	if code == http.StatusOK {
		t.Fatalf("登录壳绝不能当成功透传，实际 http=%d body=%v", code, body)
	}
}

// TestWebVPN_FetchWithoutSTIssuerIsExplicit 没接入换 st 能力时，明确报错而不是
// 静默返回登录壳。
func TestWebVPN_FetchWithoutSTIssuerIsExplicit(t *testing.T) {
	doer := &scriptedWebVPNDoer{body: "<html>ok</html>"}
	engine, svc, _ := newWebVPNEngine(t, doer)
	svc.BindJiaowuSTIssuer(nil)
	doReq(t, engine, http.MethodPost, "/school/webvpn-session",
		`{"ticket":"43d5b3947913eab2"}`)

	code, _ := doReq(t, engine, http.MethodGet, "/school/jiaowu/cache/course/fetch", "")
	if code == http.StatusOK {
		t.Fatal("未接入换票能力时不应静默成功")
	}
}
