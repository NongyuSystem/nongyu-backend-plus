package platform_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nongyu-backend-plus/internal/app"
	"nongyu-backend-plus/internal/config"
	"nongyu-backend-plus/internal/platform"
)

// ---- from http_doer_test.go ----
// 本文件验证真实出站实现 RealHTTPDoer，seam 只取公共接口 HTTPDoer.Do：
// 不测内部函数、不 mock 内部协作者。靶子一律用 httptest.NewServer 起的真实本地服务器，
// 请求一律用 http.NewRequestWithContext 构造（httptest.NewRequest 会造出带 RequestURI
// 的「服务端请求」，真实 http.Client 会直接拒绝）。

// roundTripperFunc 让测试能注入一条自定义 RoundTripper 链路。
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// hostnameOf 取 URL 的 Hostname（忽略端口），用于填白名单。
func hostnameOf(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("解析 %q 失败：%v", rawURL, err)
	}
	return u.Hostname()
}

func mustRequest(t *testing.T, ctx context.Context, rawURL string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("构造请求 %q 失败：%v", rawURL, err)
	}
	return req
}

// ---------------------------------------------------------------------------
// 1. 真实出站：Spec #18 验收第 1 条——拿到服务器真实 body，不是 mock 的 200 空响应
// ---------------------------------------------------------------------------

func TestRealHTTPDoerRealOutbound(t *testing.T) {
	const realBody = "REAL-UPSTREAM-BODY-3f9c2a"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, realBody)
	}))
	defer srv.Close()

	d := platform.NewRealHTTPDoer(platform.RealHTTPDoerOptions{
		AllowedHosts: []string{hostnameOf(t, srv.URL)},
		Timeout:      5 * time.Second,
	})
	resp, err := d.Do(context.Background(), mustRequest(t, context.Background(), srv.URL+"/probe"))
	if err != nil {
		t.Fatalf("真实出站不应报错：%v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取真实响应体失败：%v", err)
	}
	if string(body) != realBody {
		t.Fatalf("应拿到服务器真实 body，实际 %q", body)
	}
	if len(body) == 0 {
		t.Fatal("不得退化成 mock 的 200 空响应")
	}
}

// ---------------------------------------------------------------------------
// 2. 白名单外零出站：ErrTargetDenied + resp==nil + 服务器计数器==0
// ---------------------------------------------------------------------------

func TestRealHTTPDoerDeniedTargetZeroOutbound(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := platform.NewRealHTTPDoer(platform.RealHTTPDoerOptions{
		// 靶子 host 故意不在白名单内
		AllowedHosts: []string{"allowed.example.com"},
		Timeout:      5 * time.Second,
	})
	resp, err := d.Do(context.Background(), mustRequest(t, context.Background(), srv.URL+"/x"))
	if !errors.Is(err, platform.ErrTargetDenied) {
		t.Fatalf("非白名单目标应返回 ErrTargetDenied，实际 %v", err)
	}
	if resp != nil {
		t.Fatalf("拒绝时 resp 必须为 nil，实际 %+v", resp)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("白名单校验必须发生在出站前、零出站（连 tcp 都不建），服务器被命中 %d 次", got)
	}
}

// ---------------------------------------------------------------------------
// 3. 302 不跟随：Status==302、Location 原样、第二服务器零命中、Set-Cookie 可读
// ---------------------------------------------------------------------------

func TestRealHTTPDoerDoesNotFollowRedirect(t *testing.T) {
	var secondHits atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer second.Close()

	const location = "/landed"
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Set-Cookie", "ASPSESSIONIDABC=xyz; Path=/")
		w.Header().Set("Location", second.URL+location)
		w.WriteHeader(http.StatusFound)
	}))
	defer first.Close()

	// 两个 host 都放进白名单：排除「第二服务器没被命中是因为被白名单拦下」这一替代解释，
	// 从而把结论唯一地钉在 CheckRedirect 上。
	d := platform.NewRealHTTPDoer(platform.RealHTTPDoerOptions{
		AllowedHosts: []string{hostnameOf(t, first.URL), hostnameOf(t, second.URL)},
		Timeout:      5 * time.Second,
	})
	resp, err := d.Do(context.Background(), mustRequest(t, context.Background(), first.URL+"/start"))
	if err != nil {
		t.Fatalf("不跟随重定向时 Do 不应报错：%v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("应原样返回 302，实际 %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != second.URL+location {
		t.Fatalf("Location 应原样保留，实际 %q", got)
	}
	if got := secondHits.Load(); got != 0 {
		t.Fatalf("302 不得被自动跟随，第二服务器被命中 %d 次", got)
	}
	// 关键：中间跳 302 下发的 ASPSESSIONID 必须能从 resp.Cookies() 读到（不剥离 Set-Cookie）。
	if got := resp.Header.Get("Set-Cookie"); got == "" {
		t.Fatal("302 的 Set-Cookie 不得被剥离")
	}
	var found bool
	for _, c := range resp.Cookies() {
		if c.Name == "ASPSESSIONIDABC" && c.Value == "xyz" {
			found = true
		}
	}
	if !found {
		t.Fatalf("302 的 Set-Cookie 应能从 resp.Cookies() 读到，实际 %v", resp.Cookies())
	}
}

// ---------------------------------------------------------------------------
// 4. 慢目标按整体超时失败
// ---------------------------------------------------------------------------

func TestRealHTTPDoerOverallTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 一直不发响应头；客户端超时后会断开连接，r.Context() 随之 Done，handler 立即退出，
		// 这样 defer srv.Close() 不会被长睡眠拖住。
		<-r.Context().Done()
	}))
	defer srv.Close()

	d := platform.NewRealHTTPDoer(platform.RealHTTPDoerOptions{
		AllowedHosts: []string{hostnameOf(t, srv.URL)},
		Timeout:      120 * time.Millisecond,
	})
	start := time.Now()
	resp, err := d.Do(context.Background(), mustRequest(t, context.Background(), srv.URL+"/slow"))
	elapsed := time.Since(start)

	if err == nil {
		if resp != nil {
			resp.Body.Close()
		}
		t.Fatal("慢目标应按整体超时失败，实际返回了成功")
	}
	if elapsed > time.Second {
		t.Fatalf("超时未生效：耗时 %v，明显超过设定的 120ms 量级", elapsed)
	}
}

// ---------------------------------------------------------------------------
// 5. 调用方可 cancel 在途请求：errors.Is(context.Canceled) 且立即返回
// ---------------------------------------------------------------------------

func TestRealHTTPDoerCallerCancel(t *testing.T) {
	started := make(chan struct{})
	var startOnce sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startOnce.Do(func() { close(started) })
		<-r.Context().Done()
	}))
	defer srv.Close()

	d := platform.NewRealHTTPDoer(platform.RealHTTPDoerOptions{
		AllowedHosts: []string{hostnameOf(t, srv.URL)},
		Timeout:      10 * time.Second, // 故意给很宽的整体超时，确保返回来自 cancel 而非超时
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := d.Do(ctx, mustRequest(t, ctx, srv.URL+"/inflight"))
		done <- err
	}()

	<-started // 确认请求已经打到服务器
	cancel()
	startWait := time.Now()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("调用方 cancel 应返回 context.Canceled，实际 %v", err)
		}
		if waited := time.Since(startWait); waited > time.Second {
			t.Fatalf("cancel 后应立即返回，实际等待 %v", waited)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel 后 2s 内仍未返回，在途请求未被 ctx 取消")
	}
}

// ---------------------------------------------------------------------------
// 6. 响应体超上限被截断：io.ReadAll 恰得上界字节
// ---------------------------------------------------------------------------

func TestRealHTTPDoerBodySizeLimit(t *testing.T) {
	const limit int64 = 16
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("A", 4096))
	}))
	defer srv.Close()

	d := platform.NewRealHTTPDoer(platform.RealHTTPDoerOptions{
		AllowedHosts: []string{hostnameOf(t, srv.URL)},
		Timeout:      5 * time.Second,
		MaxBodyBytes: limit,
	})
	resp, err := d.Do(context.Background(), mustRequest(t, context.Background(), srv.URL+"/big"))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	// 超限必须**可识别地失败**（评审 M4）：静默截断成 16 字节会让
	// jiaowu.Service.Fetch 以 200 + 审计 "ok" 返回不完整内容。
	_, err = io.ReadAll(resp.Body)
	if err == nil {
		t.Fatal("响应体超过上限时读取必须返回可识别错误，不能静默当完整内容")
	}
	if !strings.Contains(err.Error(), "超过上限") {
		t.Fatalf("错误应说明超限原因，实际 %v", err)
	}
}

// ---------------------------------------------------------------------------
// 7. 分块响应在 Do 返回后仍可读完——抓 `defer cancel()` 那个 bug 的红测
//
// 服务器 Flush 响应头后阻塞，Do 返回后调用方才放行正文。
// 若实现里用 `defer cancel()` 取消 WithTimeout 的 ctx，Do 一返回 ctx 就 Deadline/Cancel，
// 而 resp.Body 的读取与该 ctx 绑定，随后 io.ReadAll 会报 context canceled。
// ---------------------------------------------------------------------------

func TestRealHTTPDoerChunkedBodyReadableAfterDoReturns(t *testing.T) {
	const want = "CHUNK-1|CHUNK-2|FINAL"
	releaseBody := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseBody) }) }

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush() // 先只把响应头推出去，让 Do 得以返回
		}
		<-releaseBody // 阻塞，正文等 Do 返回后才放行
		_, _ = io.WriteString(w, want)
	}))
	defer srv.Close()
	defer release() // 即使断言提前失败也要放行 handler，保证 srv.Close() 能退出

	d := platform.NewRealHTTPDoer(platform.RealHTTPDoerOptions{
		AllowedHosts: []string{hostnameOf(t, srv.URL)},
		Timeout:      10 * time.Second, // 宽超时：确保读完正文前 ctx 不会因超时而自然结束
	})
	ctx := context.Background()
	resp, err := d.Do(ctx, mustRequest(t, ctx, srv.URL+"/chunked"))
	if err != nil {
		t.Fatalf("Do（收到响应头即返回）: %v", err)
	}
	defer resp.Body.Close()

	release() // Do 已返回，现在才放行正文
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Do 返回后应仍能读完整正文（defer cancel 会在此触发 context canceled）：%v", err)
	}
	if string(body) != want {
		t.Fatalf("分块正文不完整，实际 %q", body)
	}
}

// ---------------------------------------------------------------------------
// 8. 可注入自定义 Transport：走注入链路；非白名单目标仍被拒且不再调用
// ---------------------------------------------------------------------------

func TestRealHTTPDoerInjectedTransport(t *testing.T) {
	const injectedBody = "FROM-INJECTED-TRANSPORT"
	var calls atomic.Int32
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK",
			Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
			Header:  make(http.Header),
			Body:    io.NopCloser(strings.NewReader(injectedBody)),
			Request: r,
		}, nil
	})

	d := platform.NewRealHTTPDoer(platform.RealHTTPDoerOptions{
		AllowedHosts: []string{"allowed.example.com"},
		Timeout:      5 * time.Second,
		Transport:    rt,
	})

	// (a) 合法目标：应走注入的 RoundTripper 链路，拿到它造的响应。
	resp, err := d.Do(context.Background(), mustRequest(t, context.Background(), "https://allowed.example.com/x"))
	if err != nil {
		t.Fatalf("注入 Transport 后合法目标不应报错：%v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("读取注入链路响应失败：%v", err)
	}
	if string(body) != injectedBody {
		t.Fatalf("应走注入的 Transport 链路，实际 body=%q", body)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("注入的 RoundTripper 应被调用 1 次，实际 %d", got)
	}

	// (b) 非白名单目标：仍被拒，且注入的 Transport 不再被调用（零出站）。
	badResp, err := d.Do(context.Background(), mustRequest(t, context.Background(), "https://evil.example.com/x"))
	if !errors.Is(err, platform.ErrTargetDenied) {
		t.Fatalf("注入后非白名单目标仍应返回 ErrTargetDenied，实际 %v", err)
	}
	if badResp != nil {
		t.Fatalf("拒绝对 resp 必须为 nil，实际 %+v", badResp)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("非白名单目标不得触达注入的 Transport，实际累计调用 %d 次", got)
	}
}

// ---------------------------------------------------------------------------
// 9. 注入 *http.Client 后仍强制补 CheckRedirect（且不自作主张改动调用方的 client）
// ---------------------------------------------------------------------------

func TestRealHTTPDoerInjectedClientForcesNoRedirect(t *testing.T) {
	var secondHits atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer second.Close()

	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL+"/landed", http.StatusFound)
	}))
	defer first.Close()

	// 故意注入一个「允许自动跟随」的 client：实现必须覆盖它而不是听之任之。
	injected := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return nil },
	}
	d := platform.NewRealHTTPDoer(platform.RealHTTPDoerOptions{
		AllowedHosts: []string{hostnameOf(t, first.URL), hostnameOf(t, second.URL)},
		Timeout:      5 * time.Second,
		Client:       injected,
	})
	resp, err := d.Do(context.Background(), mustRequest(t, context.Background(), first.URL+"/start"))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("注入 client 后仍不得跟随 302，实际 %d", resp.StatusCode)
	}
	if got := secondHits.Load(); got != 0 {
		t.Fatalf("注入 client 后 302 不得被跟随，第二服务器被命中 %d 次", got)
	}
	// 实现应浅拷贝注入的 client，而不是原地改写调用方持有的对象。
	if err := injected.CheckRedirect(nil, nil); err != nil {
		t.Fatalf("不应原地修改注入的 *http.Client，其 CheckRedirect 实际返回 %v", err)
	}
}

// ---- from http_body_limit_test.go ----
// code-review M4：响应体超过上限时不得静默截断当完整内容返回。
// 与同仓 schoolauth/http.go 的「读 max+1、超限即报错」口径一致。

func TestRealHTTPDoer_BodyOverLimitIsDetectable(t *testing.T) {
	// 让靶子返回明显超过上限的内容
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, strings.Repeat("x", 4096))
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("解析靶子 URL：%v", err)
	}
	doer := platform.NewRealHTTPDoer(platform.RealHTTPDoerOptions{
		AllowedHosts: []string{u.Hostname()},
		MaxBodyBytes: 64, // 故意设小，便于测试
	})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/big", nil)
	if err != nil {
		t.Fatalf("构造请求：%v", err)
	}
	resp, err := doer.Do(context.Background(), req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(resp.Body)
	if readErr == nil {
		t.Fatalf("超过上限的响应体读取时必须返回可识别错误，实际静默读到 %d 字节", len(body))
	}
	if !strings.Contains(readErr.Error(), "超过上限") {
		t.Fatalf("错误应说明超限原因，实际 %v", readErr)
	}
}

// ---- from caps_test.go ----
// 本文件是 issue #11 的验收测试：先写测试（红），再补实现（绿）。
// 用外部测试包 platform_test，既能测 platform 的导出 API，也能 import app 装配层
// 验证「注入后进程照常启动」——app 依赖 platform，但 platform_test 不会形成环。

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

// ---- from caps_gateway_test.go ----
// 出站闸口下沉到 platform 实现层后的针对性验证（B2）。
// 只有 Enforce=true 时白名单/超时/302/Set-Cookie 才强制。

func enforcedPolicy() platform.DoerPolicy {
	return platform.DoerPolicy{
		Enforce:      true,
		AllowedHosts: []string{"webvpn.sicau.edu.cn"},
		Timeout:      3 * time.Second,
	}
}

func TestEnforcedDoerRejectsNonAllowlistedHostBeforeOutbound(t *testing.T) {
	d := platform.NewMockHTTPDoer(enforcedPolicy())
	_, err := d.Do(nil, httptest.NewRequest(http.MethodGet, "https://evil.example.com/x", nil))
	if !errors.Is(err, platform.ErrTargetDenied) {
		t.Fatalf("非白名单目标应被 ErrTargetDenied 拒绝，实际 %v", err)
	}
	if got := len(d.Requests()); got != 0 {
		t.Fatalf("拒绝必须发生在闸口、零出站，实际 %d 次", got)
	}
}

func TestEnforcedDoerBlocksRedirectWithoutFollowing(t *testing.T) {
	d := platform.NewMockHTTPDoer(enforcedPolicy())
	d.SetResponder(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusFound, Status: "302 Found",
			Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
			Header: http.Header{"Location": []string{"https://jiaowu.sicau.edu.cn/login"}},
			Body:   http.NoBody, Request: req,
		}, nil
	})
	_, err := d.Do(nil, httptest.NewRequest(http.MethodGet, "https://webvpn.sicau.edu.cn/x", nil))
	if !errors.Is(err, platform.ErrBlockedRedirect) {
		t.Fatalf("指向真站的 302 应被 ErrBlockedRedirect 拦下，实际 %v", err)
	}
	if got := len(d.Requests()); got != 1 {
		t.Fatalf("不得跟随 302，底层应恰好一次请求，实际 %d 次", got)
	}
}

func TestEnforcedDoerMapsGatewayLoginToSessionExpired(t *testing.T) {
	d := platform.NewMockHTTPDoer(enforcedPolicy())
	d.SetResponder(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusFound, Status: "302 Found",
			Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
			Header: http.Header{"Location": []string{"https://webvpn.sicau.edu.cn:443/login"}},
			Body:   http.NoBody, Request: req,
		}, nil
	})
	_, err := d.Do(nil, httptest.NewRequest(http.MethodGet, "https://webvpn.sicau.edu.cn/x", nil))
	if !errors.Is(err, platform.ErrSessionExpired) {
		t.Fatalf("网关登录页（含端口）应映射 ErrSessionExpired，实际 %v", err)
	}
}

func TestEnforcedDoerStripsSetCookie(t *testing.T) {
	d := platform.NewMockHTTPDoer(enforcedPolicy())
	d.SetResponder(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK",
			Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
			Header: http.Header{"Set-Cookie": []string{"SECRET=1"}},
			Body:   http.NoBody, Request: req,
		}, nil
	})
	resp, err := d.Do(nil, httptest.NewRequest(http.MethodGet, "https://webvpn.sicau.edu.cn/x", nil))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Set-Cookie"); got != "" {
		t.Fatalf("Set-Cookie 应被剥离，实际 %q", got)
	}
}

func TestEnforcedDoerIgnores304NotModified(t *testing.T) {
	d := platform.NewMockHTTPDoer(enforcedPolicy())
	d.SetResponse(http.StatusNotModified, "")
	resp, err := d.Do(nil, httptest.NewRequest(http.MethodGet, "https://webvpn.sicau.edu.cn/x", nil))
	if err != nil {
		t.Fatalf("304 不应被当成重定向拦截，实际 %v", err)
	}
	defer resp.Body.Close()
}

func TestUnenforcedDoerKeepsSkeletonBehavior(t *testing.T) {
	// 零值策略（#11 契约测试用的形态）保持骨架期行为：只记录、不强制。
	d := platform.NewMockHTTPDoer(platform.DoerPolicy{})
	resp, err := d.Do(nil, httptest.NewRequest(http.MethodGet, "https://any.example.com/x", nil))
	if err != nil {
		t.Fatalf("骨架期不应强制，实际 %v", err)
	}
	resp.Body.Close()
	if len(d.Requests()) != 1 {
		t.Fatalf("骨架期仍应记录请求")
	}
}

// ---- from caps_cipher_test.go ----
// AEAD 语义测试：Authenticated Encryption 必须满足
//   - 同一明文两次封装产出不同密文（随机 nonce）
//   - 密文不含明文
//   - 错误密钥 / 篡改 / 截断一律拒绝
// 这些是「可上生产」相对骨架期 XOR 占位的关键增量。

func TestCipher_IsAuthenticatedEncryption(t *testing.T) {
	c := platform.NewMockCipher("production-key-from-config")
	plain := []byte("TICKET-SUPER-SECRET-VALUE")

	s1, err := c.Seal(plain)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	s2, err := c.Seal(plain)
	if err != nil {
		t.Fatalf("Seal #2: %v", err)
	}
	if bytes.Equal(s1, s2) {
		t.Fatal("AEAD 每次封装应使用随机 nonce，两次 Seal 不得相同（当前是确定性 XOR 占位）")
	}
	if bytes.Contains(s1, plain) {
		t.Fatalf("密文不得包含明文，实际 %q", s1)
	}
	got, err := c.Open(s1)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("往返失败：got=%q err=%v", got, err)
	}
	// 第二次密文也必须可解（nonce 随密文一起存放）。
	got2, err := c.Open(s2)
	if err != nil || !bytes.Equal(got2, plain) {
		t.Fatalf("第二次密文往返失败：got=%q err=%v", got2, err)
	}

	// 错误密钥
	other := platform.NewMockCipher("a-totally-different-key")
	if _, err := other.Open(s1); err == nil {
		t.Fatal("错误密钥 Open 必须失败")
	}
	// 篡改（含 nonce 区与密文区各一处）
	for _, idx := range []int{5, len(s1) - 1} {
		tampered := append([]byte(nil), s1...)
		tampered[idx] ^= 0x01
		if _, err := c.Open(tampered); err == nil {
			t.Fatalf("篡改第 %d 字节后 Open 必须失败", idx)
		}
	}
	// 截断
	if _, err := c.Open(s1[:len(s1)/2]); err == nil {
		t.Fatal("截断的密文必须失败")
	}
	// 空明文也能往返
	empty, err := c.Seal(nil)
	if err != nil {
		t.Fatalf("Seal(nil): %v", err)
	}
	back, err := c.Open(empty)
	if err != nil || len(back) != 0 {
		t.Fatalf("空明文往返失败：got=%q err=%v", back, err)
	}
}

// ---- from aescipher_test.go ----
// #21 M9：mock 与真实实现命名可区分。
// MockCipher 实际是真实 AES-256-GCM，名字误导；补 AESCipher 别名，
// 让装配代码可以写出「这是真实实现」的名字，同时不破坏既有调用方。

func TestAESCipher_IsRealAESNotAMock(t *testing.T) {
	c := platform.NewAESCipher("issue21-key")
	// 编译期：别名必须仍满足 Cipher 接口
	var _ platform.Cipher = c

	const plain = "农屿真实加密，不是占位桩"
	sealed, err := c.Seal([]byte(plain))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Equal(sealed, []byte(plain)) {
		t.Fatal("密文不得等于明文")
	}
	got, err := c.Open(sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if string(got) != plain {
		t.Fatalf("还原不符：%q", got)
	}
}
