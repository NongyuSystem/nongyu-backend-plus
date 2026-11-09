package platform_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nongyu-backend-plus/internal/platform"
)

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
