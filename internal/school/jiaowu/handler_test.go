package jiaowu_test

// #13 / #14 的 HTTP 层白名单测试：/school/address 的成功路径与拒绝路径。
// 白名单成功路径必须产出含 WebVPN KEY_HEX 前缀的编码地址，且全程零出站；
// 非白名单目标必须在闸口被拒（400 + target_denied），同样零出站。
//
// 本文件同时放登录类测试共用的 HTTP 层脚手架（captureAudit / newAuthEngine / postJSON）：
// 它们被 login_test.go 与 cache_test.go 引用，集中在这里而不是散在某一个用例旁。

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school"
	"nongyu-backend-plus/internal/school/jiaowu"
)

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
