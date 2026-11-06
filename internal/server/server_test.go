package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHealthz 锁住存活探针的契约：200 + status=ok。
// 这是骨架期唯一的自动化测试，后续业务域各自补自己的 _test.go。
func TestHealthz(t *testing.T) {
	r := New()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d，响应体：%s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != `{"status":"ok"}` {
		t.Fatalf("响应体不符合契约，实际：%s", got)
	}
}
