package jiaowu_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school/jiaowu"
)

// ---- from fetch_test.go ----
// #14 的 HTTP 层测试：全部用假出站实现注入上游行为，不依赖真实网络。
// 沿用 #13 的 newEngine（jiaowu.New(doer) 签名不变），额外用 SetResponder 定制上游响应。

func fetchGet(t *testing.T, h http.Handler, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/school/fetch?"+values.Encode(), nil)
	h.ServeHTTP(w, req)
	return w
}

// 构造一个带 3xx + Location 的上游响应，以及一个带 Set-Cookie 的 200 响应。
func redirectResponse(req *http.Request, location string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusFound,
		Status:     "302 Found",
		Proto:      "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header:  http.Header{"Location": []string{location}},
		Body:    http.NoBody,
		Request: req,
	}
}

// TestFetchNormalResponse 正常 200：结果透出，一次出站。
func TestFetchNormalResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	doer.SetResponse(http.StatusOK, `{"ok":true}`)
	engine := gin.New()
	jiaowu.Register(engine, jiaowu.New(doer))

	w := fetchGet(t, engine, url.Values{"target": {"jiaowu"}})
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d，body=%s", w.Code, w.Body.String())
	}
	if got := len(doer.Requests()); got != 1 {
		t.Fatalf("代取应恰好一次出站，实际 %d 次：%#v", got, doer.Requests())
	}
}

// TestFetchBlockedRedirectDoesNotFollow 上游 302 指向真站 → blocked_redirect，
// 且底层只记录到一次请求（证明没有跟随）。这是 ADR-0001 第 5 条的可观测证据。
func TestFetchBlockedRedirectDoesNotFollow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	doer.SetResponder(func(req *http.Request) (*http.Response, error) {
		return redirectResponse(req, "https://jiaowu.sicau.edu.cn/login"), nil
	})
	engine := gin.New()
	jiaowu.Register(engine, jiaowu.New(doer))

	w := fetchGet(t, engine, url.Values{"target": {"jiaowu"}})
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应体不是合法 JSON：%v，body=%s", err, w.Body.String())
	}
	if body.Code != "blocked_redirect" {
		t.Fatalf("期望 blocked_redirect，实际 code=%q，body=%s", body.Code, w.Body.String())
	}
	if got := len(doer.Requests()); got != 1 {
		t.Fatalf("不得跟随真站 302，底层应只记录 1 次请求，实际 %d 次：%#v", got, doer.Requests())
	}
}

// TestFetchWebvpnLoginMapsToSessionExpired 上游 302 指向 webvpn 域 /login
// → session_expired（与 blocked_redirect 可区分）。
func TestFetchWebvpnLoginMapsToSessionExpired(t *testing.T) {
	gin.SetMode(gin.TestMode)
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	doer.SetResponder(func(req *http.Request) (*http.Response, error) {
		return redirectResponse(req, "https://webvpn.sicau.edu.cn/login"), nil
	})
	engine := gin.New()
	jiaowu.Register(engine, jiaowu.New(doer))

	w := fetchGet(t, engine, url.Values{"target": {"jiaowu"}})
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应体不是合法 JSON：%v，body=%s", err, w.Body.String())
	}
	if body.Code != "session_expired" {
		t.Fatalf("期望 session_expired，实际 code=%q，body=%s", body.Code, w.Body.String())
	}
}

// TestFetchDoesNotLeakSetCookie 上游 Set-Cookie 不得回显给调用方，但其它响应头
// 应正常透出（证明 Headers 通路真的生效，避免恒真断言）。
func TestFetchDoesNotLeakSetCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	doer.SetResponder(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Proto:      "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
			Header: http.Header{
				"Set-Cookie": []string{"ASPSESSIONID=SECRET; Path=/"},
				"X-Probe":    []string{"header-passthrough-works"},
			},
			Body:    http.NoBody,
			Request: req,
		}, nil
	})
	engine := gin.New()
	jiaowu.Register(engine, jiaowu.New(doer))

	w := fetchGet(t, engine, url.Values{"target": {"jiaowu"}})
	if got := w.Header().Get("Set-Cookie"); got != "" {
		t.Fatalf("上游 Set-Cookie 不得回显，实际 %q", got)
	}
	if got := w.Header().Get("X-Probe"); got != "header-passthrough-works" {
		t.Fatalf("其它响应头应正常透出以证明 Headers 通路生效，实际 %q", got)
	}
}

// TestFetchDeniedTargetZeroOutbound 非白名单目标 → target_denied + 零出站（与 Address 一致）。
func TestFetchDeniedTargetZeroOutbound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	engine := gin.New()
	jiaowu.Register(engine, jiaowu.New(doer))

	w := fetchGet(t, engine, url.Values{"target": {"https://evil.example.com/x"}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d，body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应体不是合法 JSON：%v", err)
	}
	if body.Code != "target_denied" {
		t.Fatalf("期望 target_denied，实际 %q", body.Code)
	}
	if got := len(doer.Requests()); got != 0 {
		t.Fatalf("拒绝应在闸口、零出站，实际 %d 次", got)
	}
}

// TestFetchResultCarriesUpstreamBody 正常响应体透出给调用方。
func TestFetchResultCarriesUpstreamBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	doer.SetResponse(http.StatusOK, "hello-upstream")
	engine := gin.New()
	jiaowu.Register(engine, jiaowu.New(doer))

	w := fetchGet(t, engine, url.Values{"target": {"jiaowu"}})
	if !strings.Contains(w.Body.String(), "hello-upstream") {
		t.Fatalf("上游响应体应透出，实际 body=%s", w.Body.String())
	}
}

// ---- from fetch_realdoer_test.go ----
// #22 回归红态：换上真实 HTTPDoer 后，ADR-0001 第 5 条「302 指向真站立即失败」
// 必须在 Service.Fetch 层重新落地——不能依赖 Doer 层（真实 Doer 必须保留 3xx
// 给 schoolauth 三跳建会话用）。
//
// 用 NewRealHTTPDoer + 注入 Transport：请求仍走真实 Doer 的
// 「不跟随重定向 / 保留 3xx 响应」路径，但靶子可控、不发真实网络。

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func realDoerReturning(status int, location string) *platform.RealHTTPDoer {
	return platform.NewRealHTTPDoer(platform.RealHTTPDoerOptions{
		// Fetch 拼出的目标 host 是编码后的 webvpn 形态，这里放行它
		AllowedHosts: []string{"webvpn.sicau.edu.cn", "jiaowu.sicau.edu.cn"},
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			h := http.Header{}
			if location != "" {
				h.Set("Location", location)
			}
			return &http.Response{
				StatusCode: status,
				Status:     http.StatusText(status),
				Proto:      "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
				Header:  h,
				Body:    io.NopCloser(strings.NewReader("")),
				Request: req,
			}, nil
		}),
	})
}

// 上游 302 指向真站（非网关）时，Fetch 必须返回 CodeBlockedRedirect（502），
// 且绝不把 Location 回显给调用方。
func TestFetch_RealDoer_RedirectToOriginIsBlocked(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gin.DefaultWriter = io.Discard
	gin.DefaultErrorWriter = io.Discard

	doer := realDoerReturning(http.StatusFound, "https://jiaowu.sicau.edu.cn/real-origin")
	engine := gin.New()
	jiaowu.Register(engine, jiaowu.New(doer))

	q := url.Values{"target": {"jiaowu"}}
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/school/fetch?"+q.Encode(), nil))

	if w.Code == http.StatusFound {
		t.Fatal("302 不得原样回给客户端（ADR-0001 第 5 条）")
	}
	if strings.Contains(w.Body.String(), "jiaowu.sicau.edu.cn/real-origin") {
		t.Fatalf("真站 Location 不得回显，body=%s", w.Body.String())
	}
	if w.Code != http.StatusBadGateway {
		t.Fatalf("指向真站的 302 应映射为 502 CodeBlockedRedirect，实际 %d body=%s", w.Code, w.Body.String())
	}
}

// 302 指向网关登录页时映射为 CodeSessionExpired（401），与「真站」可区分。
func TestFetch_RealDoer_GatewayLoginRedirectIsSessionExpired(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gin.DefaultWriter = io.Discard
	gin.DefaultErrorWriter = io.Discard

	doer := realDoerReturning(http.StatusFound, "https://webvpn.sicau.edu.cn/login")
	engine := gin.New()
	jiaowu.Register(engine, jiaowu.New(doer))

	q := url.Values{"target": {"jiaowu"}}
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/school/fetch?"+q.Encode(), nil))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("网关登录页 302 应映射为 401 CodeSessionExpired，实际 %d body=%s", w.Code, w.Body.String())
	}
}
