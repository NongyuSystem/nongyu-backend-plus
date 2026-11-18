package platform_test

// 本文件是 issue #11 的验收测试：先写测试（红），再补实现（绿）。
// 用外部测试包 platform_test，既能测 platform 的导出 API，也能 import app 装配层
// 验证「注入后进程照常启动」——app 依赖 platform，但 platform_test 不会形成环。

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nongyu-backend-plus/internal/app"
	"nongyu-backend-plus/internal/config"
	"nongyu-backend-plus/internal/platform"
)

// 编译期锁定：4 个 Mock 都实现了对应的外部能力接口。
var (
	_ platform.TicketStore = (*platform.MockTicketStore)(nil)
	_ platform.Cipher      = (*platform.MockCipher)(nil)
	_ platform.HTTPDoer    = (*platform.MockHTTPDoer)(nil)
	_ platform.Clock       = (*platform.MockClock)(nil)
	_ platform.Clock       = (*platform.RealClock)(nil)
)

var errBoom = errors.New("boom")

func ctx() context.Context { return context.Background() }

// ---- TicketStore ----

func TestMockTicketStore_PutGetDelete(t *testing.T) {
	s := platform.NewMockTicketStore()
	exp := time.Date(2026, 11, 7, 20, 0, 0, 0, time.UTC)

	// 不存在 → 复用现有 ErrNotFound
	if _, _, err := s.Get(ctx(), "u1"); !platform.IsNotFound(err) {
		t.Fatalf("Get 不存在的 userID 应返回 ErrNotFound，实际：%v", err)
	}

	sealed := []byte("sealed-blob")
	if err := s.Put(ctx(), "u1", sealed, exp); err != nil {
		t.Fatalf("Put 失败：%v", err)
	}

	// Put 之后调用方改动自己持有的切片，不应污染存储里的值
	sealed[0] = 'X'
	got, gotExp, err := s.Get(ctx(), "u1")
	if err != nil {
		t.Fatalf("Get 失败：%v", err)
	}
	if !bytes.Equal(got, []byte("sealed-blob")) {
		t.Fatalf("Get 返回的 sealed 不符：%q", got)
	}
	if !gotExp.Equal(exp) {
		t.Fatalf("Get 返回的 expiresAt 不符：%v，期望 %v", gotExp, exp)
	}

	if err := s.Delete(ctx(), "u1"); err != nil {
		t.Fatalf("Delete 失败：%v", err)
	}
	if _, _, err := s.Get(ctx(), "u1"); !platform.IsNotFound(err) {
		t.Fatalf("Delete 后 Get 应返回 ErrNotFound，实际：%v", err)
	}
	// Delete 幂等：不存在的 userID 也应干净返回
	if err := s.Delete(ctx(), "u1"); err != nil {
		t.Fatalf("Delete 不存在时应幂等返回 nil，实际：%v", err)
	}
}

func TestMockTicketStore_ListExpiringSoon(t *testing.T) {
	s := platform.NewMockTicketStore()
	base := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	before := base.Add(2 * time.Hour)

	// 零值 ExpiresAt = 会话级、无明确过期 → 不参与候选
	mustPut(t, s, "u_session", time.Time{})
	// 过期时刻晚于 before → 不参与候选
	mustPut(t, s, "u_late", base.Add(3*time.Hour))
	// 三个候选，故意乱序写入
	mustPut(t, s, "u_b", base.Add(90*time.Minute))
	mustPut(t, s, "u_a", base.Add(30*time.Minute))
	mustPut(t, s, "u_c", base.Add(60*time.Minute))

	// 零值过期要能原样读回
	_, exp, err := s.Get(ctx(), "u_session")
	if err != nil {
		t.Fatalf("Get 零值过期票据失败：%v", err)
	}
	if !exp.IsZero() {
		t.Fatalf("零值过期应原样返回，实际：%v", exp)
	}

	refs, err := s.ListExpiringSoon(ctx(), before, 10)
	if err != nil {
		t.Fatalf("ListExpiringSoon 失败：%v", err)
	}
	want := []string{"u_a", "u_c", "u_b"} // 按 ExpiresAt 升序
	if len(refs) != len(want) {
		t.Fatalf("期望 %d 个候选，实际 %d：%+v", len(want), len(refs), refs)
	}
	for i, w := range want {
		if refs[i].UserID != w {
			t.Fatalf("第 %d 个候选期望 %s，实际 %s（整体：%+v）", i, w, refs[i].UserID, refs)
		}
		if refs[i].ExpiresAt.IsZero() {
			t.Fatalf("候选 %s 不应带零值 ExpiresAt", refs[i].UserID)
		}
	}

	// limit 截断（仍取最早到期的几个）
	refs, err = s.ListExpiringSoon(ctx(), before, 2)
	if err != nil {
		t.Fatalf("ListExpiringSoon(limit=2) 失败：%v", err)
	}
	if len(refs) != 2 || refs[0].UserID != "u_a" || refs[1].UserID != "u_c" {
		t.Fatalf("limit=2 应返回最早的两个，实际：%+v", refs)
	}

	// limit <= 0 → 空列表
	refs, err = s.ListExpiringSoon(ctx(), before, 0)
	if err != nil {
		t.Fatalf("ListExpiringSoon(limit=0) 失败：%v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("limit<=0 应返回空列表，实际：%+v", refs)
	}

	// before 之前没有任何明确过期的票
	refs, err = s.ListExpiringSoon(ctx(), base, 10)
	if err != nil {
		t.Fatalf("ListExpiringSoon(before=base) 失败：%v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("before 之前无候选，实际：%+v", refs)
	}
}

func mustPut(t *testing.T, s platform.TicketStore, userID string, exp time.Time) {
	t.Helper()
	if err := s.Put(ctx(), userID, []byte("sealed-"+userID), exp); err != nil {
		t.Fatalf("Put(%s) 失败：%v", userID, err)
	}
}

// ---- Cipher ----

func TestMockCipher_SealOpenRoundTrip(t *testing.T) {
	c := platform.NewMockCipher("key-alpha")
	cases := [][]byte{
		[]byte("hello ticket"),
		[]byte(""),
		{0x00, 0xff, 0x01, 0x7f},
	}
	for _, in := range cases {
		sealed, err := c.Seal(in)
		if err != nil {
			t.Fatalf("Seal(%v) 失败：%v", in, err)
		}
		if len(in) > 0 && bytes.Equal(sealed, in) {
			t.Fatalf("Seal 应产出与明文不同的字节，in=%v", in)
		}
		got, err := c.Open(sealed)
		if err != nil {
			t.Fatalf("Open(Seal(%v)) 失败：%v", in, err)
		}
		if !bytes.Equal(got, in) {
			t.Fatalf("往返不符：in=%v got=%v", in, got)
		}
	}

	// 密钥不符 → 应报错（密钥来自配置，接口不暴露）
	sealed, _ := c.Seal([]byte("secret"))
	other := platform.NewMockCipher("key-beta")
	if _, err := other.Open(sealed); err == nil {
		t.Fatalf("用错误密钥 Open 应返回错误")
	}

	// 垃圾 / 过短输入
	if _, err := c.Open(nil); err == nil {
		t.Fatalf("nil sealed 应返回错误")
	}
	if _, err := c.Open([]byte("xx")); err == nil {
		t.Fatalf("过短的 sealed 应返回错误")
	}

	// 篡改 → 应报错
	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := c.Open(tampered); err == nil {
		t.Fatalf("被篡改的 sealed 应返回错误")
	}

	// 空密钥有安全默认值：不 panic、能往返
	def := platform.NewMockCipher("")
	s, err := def.Seal([]byte("x"))
	if err != nil {
		t.Fatalf("空密钥 Seal 失败：%v", err)
	}
	got, err := def.Open(s)
	if err != nil || !bytes.Equal(got, []byte("x")) {
		t.Fatalf("空密钥往返不符：got=%q err=%v", got, err)
	}
}

// ---- HTTPDoer ----

func TestMockHTTPDoer_RecordsAndResponds(t *testing.T) {
	d := platform.NewMockHTTPDoer(platform.DoerPolicy{
		AllowedHosts: []string{"jiaowu.example.edu.cn"},
		Timeout:      3 * time.Second,
	})
	d.SetResponse(http.StatusTeapot, "hello")

	req1 := httptest.NewRequest(http.MethodGet, "https://jiaowu.example.edu.cn/captcha", nil)
	resp1, err := d.Do(ctx(), req1)
	if err != nil {
		t.Fatalf("Do 失败：%v", err)
	}
	if resp1.StatusCode != http.StatusTeapot {
		t.Fatalf("期望配置的 %d，实际 %d", http.StatusTeapot, resp1.StatusCode)
	}
	body, err := io.ReadAll(resp1.Body)
	if err != nil {
		t.Fatalf("读响应体失败：%v", err)
	}
	resp1.Body.Close()
	if string(body) != "hello" {
		t.Fatalf("响应体不符：%q", body)
	}

	// 第二个请求不在白名单里，但骨架期 mock 只记录、不强制策略（完整策略留后续实现票）
	req2 := httptest.NewRequest(http.MethodPost, "https://auth.example.edu.cn/login", nil)
	resp2, err := d.Do(ctx(), req2)
	if err != nil {
		t.Fatalf("第二次 Do 失败：%v", err)
	}
	resp2.Body.Close()

	recorded := d.Requests()
	if len(recorded) != 2 {
		t.Fatalf("期望记录 2 个请求，实际 %d：%+v", len(recorded), recorded)
	}
	if recorded[0].Method != http.MethodGet || recorded[0].Host != "jiaowu.example.edu.cn" {
		t.Fatalf("第 1 条记录不符：%+v", recorded[0])
	}
	if recorded[1].Method != http.MethodPost || recorded[1].Host != "auth.example.edu.cn" {
		t.Fatalf("第 2 条记录不符：%+v", recorded[1])
	}

	// 默认响应（未配置 responder）
	d2 := platform.NewMockHTTPDoer(platform.DoerPolicy{})
	resp3, err := d2.Do(ctx(), httptest.NewRequest(http.MethodGet, "https://x.example/x", nil))
	if err != nil {
		t.Fatalf("默认 Do 失败：%v", err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("未配置响应时应返回 200，实际 %d", resp3.StatusCode)
	}

	// responder 也可以返回错误
	d3 := platform.NewMockHTTPDoer(platform.DoerPolicy{})
	d3.SetResponder(func(*http.Request) (*http.Response, error) { return nil, errBoom })
	if _, err := d3.Do(ctx(), httptest.NewRequest(http.MethodGet, "https://x.example/x", nil)); !errors.Is(err, errBoom) {
		t.Fatalf("配置的错误应透传，实际：%v", err)
	}

	// 策略结构已留好，可读回
	p := d.Policy()
	if len(p.AllowedHosts) != 1 || p.AllowedHosts[0] != "jiaowu.example.edu.cn" {
		t.Fatalf("AllowedHosts 不符：%+v", p.AllowedHosts)
	}
	if p.Timeout != 3*time.Second {
		t.Fatalf("策略不符：%+v", p)
	}
}

// ---- Clock ----

func TestMockClock_Settable(t *testing.T) {
	t0 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	c := platform.NewMockClock(t0)
	if !c.Now().Equal(t0) {
		t.Fatalf("Now 应为设定值 %v，实际 %v", t0, c.Now())
	}
	t1 := t0.Add(48 * time.Hour)
	c.Set(t1)
	if !c.Now().Equal(t1) {
		t.Fatalf("Set 后 Now 应为 %v，实际 %v", t1, c.Now())
	}

	// 零值构造回落到系统时间（非零）
	if platform.NewMockClock(time.Time{}).Now().IsZero() {
		t.Fatalf("零值构造应回落到非零的系统时间")
	}

	// RealClock 读系统时间
	rc := platform.NewRealClock()
	before := time.Now()
	got := rc.Now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Fatalf("RealClock.Now 应落在 [before, after] 之间：%v / %v / %v", before, got, after)
	}
}

// ---- config ----

func TestConfigLoad_CapabilityDefaultsAndOverride(t *testing.T) {
	t.Setenv("SHUTDOWN_TIMEOUT_SEC", "10")
	t.Setenv("TICKET_KEY", "")
	t.Setenv("HTTP_TIMEOUT_SEC", "")
	t.Setenv("HTTP_ALLOWLIST", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("缺项时 Load 不应失败：%v", err)
	}
	if cfg.TicketKey == "" {
		t.Fatalf("TICKET_KEY 缺项应有安全默认密钥")
	}
	if cfg.HTTPTimeoutSec <= 0 {
		t.Fatalf("HTTP_TIMEOUT_SEC 缺项应有正的默认超时，实际 %d", cfg.HTTPTimeoutSec)
	}

	t.Setenv("TICKET_KEY", "from-env")
	t.Setenv("HTTP_TIMEOUT_SEC", "7")
	t.Setenv("HTTP_ALLOWLIST", "a.example, b.example")
	cfg2, err := config.Load()
	if err != nil {
		t.Fatalf("覆盖时 Load 不应失败：%v", err)
	}
	if cfg2.TicketKey != "from-env" || cfg2.HTTPTimeoutSec != 7 || cfg2.HTTPAllowlist != "a.example, b.example" {
		t.Fatalf("环境变量覆盖未生效：%+v", cfg2)
	}
}

// ---- app 装配层 ----

func TestAppWiresCapabilitiesAndHealthz(t *testing.T) {
	cfg := config.Config{
		Addr:               ":0",
		AppEnv:             "test",
		ShutdownTimeoutSec: 1,
		TicketKey:          "test-ticket-key",
		HTTPTimeoutSec:     3,
		HTTPAllowlist:      "jiaowu.example.edu.cn, auth.example.edu.cn",
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := app.New(cfg, logger)
	if err != nil {
		t.Fatalf("app.New 失败：%v", err)
	}
	if a == nil {
		t.Fatalf("app.New 应返回非 nil 的 App")
	}

	// 5 个能力都已注入、可用（issue #23 移除了无消费者的 Store 字段）。
	caps := a.Capabilities()
	if caps.Tickets == nil || caps.Cipher == nil || caps.Doer == nil || caps.Clock == nil {
		t.Fatalf("装配层应注入全部外部能力，实际：%+v", caps)
	}
	if _, _, err := caps.Tickets.Get(ctx(), "nobody"); !platform.IsNotFound(err) {
		t.Fatalf("注入的 TicketStore 不可用：%v", err)
	}
	if _, err := caps.Cipher.Seal([]byte("x")); err != nil {
		t.Fatalf("注入的 Cipher 不可用：%v", err)
	}
	if caps.Clock.Now().IsZero() {
		t.Fatalf("注入的 Clock 应返回真实时间")
	}
	resp, err := caps.Doer.Do(ctx(), httptest.NewRequest(http.MethodGet, "https://jiaowu.example.edu.cn/x", nil))
	if err != nil {
		t.Fatalf("注入的 HTTPDoer 不可用：%v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("注入的 HTTPDoer 默认响应不是 200：%d", resp.StatusCode)
	}

	// app.New 注入后存活探针契约不变：200 + {"status":"ok"}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d，响应体：%s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != `{"status":"ok"}` {
		t.Fatalf("响应体不符合契约，实际：%s", got)
	}
}
