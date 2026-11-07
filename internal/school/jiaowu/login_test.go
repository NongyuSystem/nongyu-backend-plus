package jiaowu_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school"
	"nongyu-backend-plus/internal/school/jiaowu"
)

// #15 的 HTTP 层测试：换票主路径、票据加密保管、审计不含票、登出清除。
// 全部用假实现，不依赖真实学校网络。

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

// TestCaptchaReturnsImageAndUUID 取验证码接口返回可展示图像与事务标识。
func TestCaptchaReturnsImageAndUUID(t *testing.T) {
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	doer.SetResponse(http.StatusOK, "PNG-PLACEHOLDER")
	h, _, _, _ := newAuthEngine(t, doer)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/school/captcha?target=auth", nil)
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d，body=%s", w.Code, w.Body.String())
	}
	var body struct {
		UUID  string `json:"uuid"`
		Image string `json:"image"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("非法 JSON：%v", err)
	}
	if body.UUID == "" || body.Image == "" {
		t.Fatalf("应返回 uuid 与 image，实际 %+v", body)
	}
}

// TestLoginSuccessStoresAndReturnsTicket 正确验证码 → 换票成功，票据入外置存储、下发给本人。
func TestLoginSuccessStoresAndReturnsTicket(t *testing.T) {
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	doer.SetResponder(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK",
			Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
			Header: make(http.Header), Body: io.NopCloser(strings.NewReader("TICKET-XYZ")),
			Request: req,
		}, nil
	})
	h, tickets, clock, _ := newAuthEngine(t, doer)

	w := postJSON(t, h, "/school/login", map[string]string{
		"userID": "u1", "code": "1234", "uuid": "cap-1",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d，body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Ticket    string `json:"ticket"`
		ExpiresAt string `json:"expiresAt"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("非法 JSON：%v", err)
	}
	if body.Ticket == "" {
		t.Fatalf("应把票据下发给本人，实际 body=%s", w.Body.String())
	}
	// 存储里落的是「加密后的票据」，不应出现明文。
	sealed, expiresAt, err := tickets.Get(nil, "test-user")
	if err != nil {
		t.Fatalf("票据应入外置存储：%v", err)
	}
	if bytes.Contains(sealed, []byte("TICKET-XYZ")) {
		t.Fatalf("存储里应是密文，不得出现明文票据")
	}
	if expiresAt.IsZero() {
		t.Fatalf("过期时刻应随票据一起记录")
	}
	if !expiresAt.After(clock.Now()) {
		t.Fatalf("过期时刻应晚于当前时刻")
	}
}

// TestLoginAuditNeverContainsTicket 审计结构化字段中不含票据（无论明文或密文）。
func TestLoginAuditNeverContainsTicket(t *testing.T) {
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	doer.SetResponse(http.StatusOK, "SECRET-TICKET-VALUE")
	h, _, _, audit := newAuthEngine(t, doer)

	postJSON(t, h, "/school/login", map[string]string{
		"userID": "u2", "code": "1234", "uuid": "cap-2",
	})
	if strings.Contains(audit.buf.String(), "SECRET-TICKET-VALUE") {
		t.Fatalf("审计日志不得包含票据值，实际：%s", audit.buf.String())
	}
	if !strings.Contains(audit.buf.String(), "test-user") {
		t.Fatalf("审计应记录用户标识，实际：%s", audit.buf.String())
	}
}

// TestLoginBadCaptchaNoTicketStored 验证码错误 → 可区分错误，且存储不落任何票据。
func TestLoginBadCaptchaNoTicketStored(t *testing.T) {
	// 上游返回「验证码错误」信号：用带 code=500 的响应体表达。
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	doer.SetResponder(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK",
			Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
			Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":500,"msg":"验证码错误"}`)),
			Request: req,
		}, nil
	})
	h, tickets, _, _ := newAuthEngine(t, doer)

	w := postJSON(t, h, "/school/login", map[string]string{
		"userID": "u3", "code": "WRONG", "uuid": "cap-3",
	})
	if w.Code == http.StatusOK {
		t.Fatalf("错误验证码不应返回 200，实际 body=%s", w.Body.String())
	}
	if _, _, err := tickets.Get(nil, "test-user"); err == nil {
		t.Fatalf("验证码错误时存储里不应落任何票据")
	}
}

// TestLogoutDeletesTicket 退出登录后该用户票据记录被删除。
func TestLogoutDeletesTicket(t *testing.T) {
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	doer.SetResponse(http.StatusOK, "TICKET-LOGOUT")
	h, tickets, _, _ := newAuthEngine(t, doer)

	postJSON(t, h, "/school/login", map[string]string{
		"userID": "u4", "code": "1234", "uuid": "cap-4",
	})
	if _, _, err := tickets.Get(nil, "test-user"); err != nil {
		t.Fatalf("前置：登录应落票据，%v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/school/logout", nil)
	req.Header.Set("X-Test-User", "test-user")
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("登出期望 200，实际 %d，body=%s", w.Code, w.Body.String())
	}
	if _, _, err := tickets.Get(nil, "test-user"); err == nil {
		t.Fatalf("登出后票据记录应被删除")
	}
}
