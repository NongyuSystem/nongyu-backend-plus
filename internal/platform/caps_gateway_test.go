package platform_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nongyu-backend-plus/internal/platform"
)

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
