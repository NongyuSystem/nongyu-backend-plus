package jiaowu_test

// #15 的 HTTP 层测试：换票主路径、票据加密保管、审计不含票、登出清除。
// 全部用假实现，不依赖真实学校网络。
//
// HTTP 层脚手架（captureAudit / newAuthEngine / postJSON）在 handler_test.go。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nongyu-backend-plus/internal/platform"
)

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

// TestLoginIsDeprecatedAndNeverStoresTicket 锁住 2026-11-17 的处置：
// /school/login 不再向上游换票、不再写票据存储——它当年写的是上游响应原文，
// 会覆盖 TGTManager 保存的 {TGT, CookieHeader}（cache_miss 全线 session=unavailable 的根因）。
// 端点保留形态（不 404），但一律回 session_expired，并留下 deprecate 审计。
func TestLoginIsDeprecatedAndNeverStoresTicket(t *testing.T) {
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	doer.SetResponder(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK",
			Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
			Header: make(http.Header), Body: io.NopCloser(strings.NewReader("TICKET-XYZ")),
			Request: req,
		}, nil
	})
	h, tickets, _, audit := newAuthEngine(t, doer)

	w := postJSON(t, h, "/school/login", map[string]string{
		"userID": "u1", "code": "1234", "uuid": "cap-1",
	})
	if w.Code == http.StatusOK {
		t.Fatalf("已停用的登录端点不应回 200，实际 body=%s", w.Body.String())
	}
	// 关键断言：存储里绝不能被写入任何东西（这正是它过去破坏 tgt 的方式）。
	if _, _, err := tickets.Get(nil, "test-user"); err == nil {
		t.Fatal("登录端点绝不能再写票据存储（会覆盖真正的 tgt）")
	}
	if !strings.Contains(audit.buf.String(), "deprecated_no_store") {
		t.Fatalf("应留下 deprecated_no_store 审计，实际：%s", audit.buf.String())
	}
	// 也不应真的出站打学校站。
	if got := len(doer.Requests()); got != 0 {
		t.Fatalf("停用后不应出站，实际 %d 次", got)
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
	// 端点已停用，但审计仍应记录「谁调了它」——这是观测老客户端是否还在用的唯一线索。
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

// TestLoginEndpointReturnsGoneAndRecordsAudit 已停用的换票端点回 410 + login_deprecated。
//
// 为什么要 410 而不是 401：401 会被客户端读成「登录态过期，去重新登录」，
// 而真实处置是「这个端点没了，换 /identity/login」。码不同，客户端动作才不同。
func TestLoginEndpointReturnsGoneAndRecordsAudit(t *testing.T) {
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	h, tickets, _, audit := newAuthEngine(t, doer)

	w := postJSON(t, h, "/school/login", map[string]string{
		"code": "1234", "uuid": "cap-x",
	})
	if w.Code != http.StatusGone {
		t.Fatalf("停用端点应回 410，实际 %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Code != "login_deprecated" {
		t.Fatalf("应回 code=login_deprecated，实际 %q", body.Code)
	}
	if _, _, err := tickets.Get(nil, "test-user"); err == nil {
		t.Fatal("停用端点绝不能写票据存储")
	}
	if !strings.Contains(audit.buf.String(), "deprecated_no_store") {
		t.Fatalf("应留下 deprecated_no_store 审计，实际：%s", audit.buf.String())
	}
	if got := len(doer.Requests()); got != 0 {
		t.Fatalf("停用端点不应出站，实际 %d 次", got)
	}
}

// TestLogoutDeletesTicket 退出登录后该用户票据记录被删除（幂等）。
//
// 2026-11-17 改：不再借 `POST /school/login` 造前置数据（它已停止落库）。
// 直接写入存储来验证 Logout 的删除语义本身。
func TestLogoutDeletesTicket(t *testing.T) {
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	h, tickets, _, _ := newAuthEngine(t, doer)

	// 前置：手工放一条记录，模拟「已登录并保管了 tgt」。
	if err := tickets.Put(nil, "test-user", []byte("sealed"), time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("前置写入失败：%v", err)
	}
	if _, _, err := tickets.Get(nil, "test-user"); err != nil {
		t.Fatalf("前置：记录应存在，%v", err)
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

// TestAuditCoversAddressFetchCaptcha 证明「代请求」类入口也记审计（ADR 第 4 条），
// 且审计只记 userID/action/result/target 四个结构化字段，不含票据或响应体。
func TestAuditCoversAddressFetchCaptcha(t *testing.T) {
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	doer.SetResponse(http.StatusOK, "upstream-payload")
	h, _, _, audit := newAuthEngine(t, doer)

	for _, path := range []string{
		"/school/address?target=jiaowu",
		"/school/fetch?target=jiaowu",
		"/school/captcha?target=auth",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Test-User", "test-user")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Fatalf("%s 应已注册", path)
		}
	}
	log := audit.buf.String()
	for _, action := range []string{"address", "fetch", "captcha"} {
		if !strings.Contains(log, "action="+action) {
			t.Fatalf("审计应记录 action=%s，实际日志：%s", action, log)
		}
	}
	if !strings.Contains(log, "userID=test-user") {
		t.Fatalf("审计应记录已认证身份，实际：%s", log)
	}
	if strings.Contains(log, "upstream-payload") {
		t.Fatalf("审计不得记录上游响应体，实际：%s", log)
	}
	// 目标逻辑名必须真实：address/fetch 打的是 jiaowu，captcha 打的是 auth。
	if !strings.Contains(log, "target=jiaowu") {
		t.Fatalf("审计应记录真实目标逻辑名 jiaowu，实际：%s", log)
	}
	if !strings.Contains(log, "target=auth") {
		t.Fatalf("captcha 审计应记录目标 auth，实际：%s", log)
	}
}
