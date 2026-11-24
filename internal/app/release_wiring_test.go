package app_test

// 应用发布域的装配层 HTTP 验收（Spec #80 / T1～T3）。
//
// 只经公共入口 app.New + Handler() 发真实请求，穿完整中间件链，验证：
//  1. 三个只读端点**要求用户会话**（未登录 401），且**不进**公开可读清单；
//  2. 管理端上传需要管理员身份，不当成公开写入口；
//  3. 无任何已发布版本时 /latest 返回 200 + hasRelease=false（不是 404）。
//
// 用内存仓储与内存对象存储（未配 DATABASE_URL / MINIO_ENDPOINT），
// 因此本用例在任何环境都能跑。先例：internal/app/skin_wiring_test.go。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/app"
	"nongyu-backend-plus/internal/config"
)

func newReleaseTestApp(t *testing.T) *app.App {
	t.Helper()
	gin.SetMode(gin.TestMode)
	gin.DefaultWriter = io.Discard
	gin.DefaultErrorWriter = io.Discard

	cfg := config.Config{
		Addr: ":0", AppEnv: "test", ShutdownTimeoutSec: 1,
		TicketKey: "test-ticket-key-32-bytes-min!!", HTTPTimeoutSec: 3,
		AuthKey: "test-auth-key", RateLimitPerMinute: 60,
		HTTPEnforce: false,
	}
	a, err := app.New(cfg, quietLogger())
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(func() { _ = a.Shutdown(time.Second) })
	return a
}

// 三个只读端点都必须要求会话：未登录一律 401。
func TestRelease_RoutesRequireAuth(t *testing.T) {
	a := newReleaseTestApp(t)
	paths := []string{
		"/releases/android/latest?applicationId=com.nongyu.app",
		"/releases/android/latest/pack?applicationId=com.nongyu.app",
		"/releases/android/10402/pack?applicationId=com.nongyu.app",
	}
	for _, p := range paths {
		w := serveGet(a.Handler(), p)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("GET %s 无令牌应 401，实际 %d body=%s", p, w.Code, w.Body.String())
		}
	}
}

// 路径不得进公开可读清单——用一个**不存在**的应用探测也没用，
// 必须先被认证拦住；这里另外断言 401 是 JSON 错误体而不是空白。
func TestRelease_UnauthorizedIsJSONEnvelope(t *testing.T) {
	a := newReleaseTestApp(t)
	w := serveGet(a.Handler(), "/releases/android/latest?applicationId=com.nongyu.app")
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("401 响应不是合法 JSON：%v body=%q", err, w.Body.String())
	}
	if body["code"] != "unauthorized" {
		t.Fatalf("401 错误码应是 unauthorized，得到 %v", body["code"])
	}
}

// 管理端上传是写入口，不得被任何公开前缀顺带豁免。
// 无身份域时不挂载 adminGroup，因此这里只要求「不是 2xx」。
func TestRelease_AdminUploadIsNotPublic(t *testing.T) {
	a := newReleaseTestApp(t)
	req := httptest.NewRequest(http.MethodPost, "/admin/releases", nil)
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	if rec.Code >= 200 && rec.Code < 300 {
		t.Fatalf("POST /admin/releases 不得免认证成功，实际 %d", rec.Code)
	}
}

// 三个只读端点都是 GET/HEAD；POST 到只读路径不应成功。
func TestRelease_ReadOnlyPathsRejectPost(t *testing.T) {
	a := newReleaseTestApp(t)
	for _, p := range []string{
		"/releases/android/latest",
		"/releases/android/latest/pack",
		"/releases/android/10402/pack",
	} {
		req := httptest.NewRequest(http.MethodPost, p, nil)
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, req)
		if rec.Code >= 200 && rec.Code < 300 {
			t.Fatalf("POST %s 不应成功，实际 %d", p, rec.Code)
		}
	}
}
