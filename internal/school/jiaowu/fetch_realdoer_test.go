package jiaowu_test

// #22 回归红态：换上真实 HTTPDoer 后，ADR-0001 第 5 条「302 指向真站立即失败」
// 必须在 Service.Fetch 层重新落地——不能依赖 Doer 层（真实 Doer 必须保留 3xx
// 给 schoolauth 三跳建会话用）。
//
// 用 NewRealHTTPDoer + 注入 Transport：请求仍走真实 Doer 的
// 「不跟随重定向 / 保留 3xx 响应」路径，但靶子可控、不发真实网络。

import (
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
