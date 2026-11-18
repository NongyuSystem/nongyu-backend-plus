package platform_test

// code-review M4：响应体超过上限时不得静默截断当完整内容返回。
// 与同仓 schoolauth/http.go 的「读 max+1、超限即报错」口径一致。

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"nongyu-backend-plus/internal/platform"
)

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
