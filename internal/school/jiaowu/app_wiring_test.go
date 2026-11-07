package jiaowu_test

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/app"
	"nongyu-backend-plus/internal/config"
	"nongyu-backend-plus/internal/platform"
)

// 说明：本文件是「与装配层配合」的 HTTP 层测试（见 issue #13 测试要求）。
// 为严格遵守「只允许在 internal/school/jiaowu/ 下新建文件」的写范围，它放在本目录、
// 用外部测试包 package jiaowu_test，而不是 internal/app/app_test.go。
// 这样：生产代码 package jiaowu 仍只依赖 school + platform（app/config 仅出现在测试里），
// 依赖方向约束不被破坏。

func newAppHandler(t *testing.T) (http.Handler, *platform.MockHTTPDoer) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	gin.DefaultWriter = io.Discard
	gin.DefaultErrorWriter = io.Discard

	application, err := app.New(config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("app.New 失败：%v", err)
	}
	// 装配层把 HTTPDoer 通过接口注入；这里断言其具体类型，只为观察「出站记录」，
	// 生产子域依赖的仍是 platform.HTTPDoer 接口。
	doer, ok := application.Capabilities().Doer.(*platform.MockHTTPDoer)
	if !ok {
		t.Fatalf("装配层应注入 *platform.MockHTTPDoer，实际 %T", application.Capabilities().Doer)
	}
	return application.Handler(), doer
}

func appGet(t *testing.T, h http.Handler, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/school/address?"+values.Encode(), nil)
	h.ServeHTTP(w, req)
	return w
}

// TestAppWiringAddressWhitelist 验证装配层确实把子域路由注册进 gin.Engine：
// 经 app.New().Handler() 访问白名单目标的 Address，应拿到编码地址且零出站。
func TestAppWiringAddressWhitelist(t *testing.T) {
	h, doer := newAppHandler(t)
	w := appGet(t, h, url.Values{"target": {"jiaowu"}})

	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d，响应体：%s", w.Code, w.Body.String())
	}
	var body struct {
		Target  string `json:"target"`
		Address string `json:"address"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应体不是合法 JSON：%v，body=%s", err, w.Body.String())
	}
	if body.Target != "jiaowu" {
		t.Fatalf("响应体应含逻辑目标名，实际 target=%q", body.Target)
	}
	if !strings.HasPrefix(body.Address, "https://webvpn.sicau.edu.cn/https/"+keyHexPrefix) {
		t.Fatalf("编码地址形态不符，实际 address=%q", body.Address)
	}
	if got := len(doer.Requests()); got != 0 {
		t.Fatalf("经装配层访问 Address 也应零出站，实际记录到 %d 次：%#v", got, doer.Requests())
	}
}

// TestAppWiringDeniedTargetZeroOutbound 验证非白名单目标经装配层也返回
// target_denied + 400，且装配层注入的假出站实现记录到零请求。
func TestAppWiringDeniedTargetZeroOutbound(t *testing.T) {
	h, doer := newAppHandler(t)
	w := appGet(t, h, url.Values{"target": {"https://evil.example.com/x"}})

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
		t.Fatalf("拒绝必须发生在闸口、零出站，实际记录到 %d 次：%#v", got, doer.Requests())
	}
}

// TestAppWiringHealthz 锁住既有存活探针契约在装配层注册业务路由后仍成立：
// 200 + {"status":"ok"}（既有 internal/server/server_test.go 保持绿）。
func TestAppWiringHealthz(t *testing.T) {
	h, _ := newAppHandler(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d，响应体：%s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != `{"status":"ok"}` {
		t.Fatalf("响应体不符合契约，实际：%s", got)
	}
}

// TestAppWiringAccelRouteReachable 证明 #17 的加速端点在生产装配里真正可达（B1），
// 且默认关闭时返回「需要走主路径」（不发起免密出站）。
func TestAppWiringAccelRouteReachable(t *testing.T) {
	h, doer := newAppHandler(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/school/login/accel", nil)
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("加速端点应已注册（不是 404），实际 %d，body=%s", w.Code, w.Body.String())
	}
	var body struct {
		NeedCaptcha bool `json:"needCaptcha"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("非法 JSON：%v", err)
	}
	if !body.NeedCaptcha {
		t.Fatalf("默认关闭时应要求走主路径，实际 %+v", body)
	}
	if got := len(doer.Requests()); got != 0 {
		t.Fatalf("默认关闭时不得发起免密出站，实际 %d 次", got)
	}
}

// TestAppWiringDoerIsEnforced 证明装配层注入的 HTTPDoer 就是守卫实例（B2）：
// 非白名单 host 直接被拒，Capabilities().Doer 不再裸奔。
func TestAppWiringDoerIsEnforced(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gin.DefaultWriter = io.Discard
	gin.DefaultErrorWriter = io.Discard
	application, err := app.New(config.Config{HTTPEnforce: true}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	_, err = application.Capabilities().Doer.Do(nil, httptest.NewRequest(http.MethodGet, "https://evil.example.com/x", nil))
	if !errors.Is(err, platform.ErrTargetDenied) {
		t.Fatalf("装配层注入的 Doer 应以 ErrTargetDenied 拒绝非白名单目标，实际 %v", err)
	}
}

// TestAppWiringLoadDefaultsEnforceGateway 用 config.Load() 的**真实默认值**装配，
// 断言生产入口（main.go 走 Load）的闸口确实是强制的——堵住「零值 Config 不强制」
// 与「Load 默认强制」不一致时无人能测出的回归缝（复验补强项 ①）。
func TestAppWiringLoadDefaultsEnforceGateway(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gin.DefaultWriter = io.Discard
	gin.DefaultErrorWriter = io.Discard
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if !cfg.HTTPEnforce {
		t.Fatal("Load() 应默认启用出站闸口强制")
	}
	application, err := app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	_, err = application.Capabilities().Doer.Do(nil, httptest.NewRequest(http.MethodGet, "https://evil.example.com/x", nil))
	if !errors.Is(err, platform.ErrTargetDenied) {
		t.Fatalf("经 Load() 默认配置装配后闸口应强制，实际 %v", err)
	}
	// 合法目标（内建白名单）应放行。
	resp, err := application.Capabilities().Doer.Do(nil, httptest.NewRequest(http.MethodGet, "https://webvpn.sicau.edu.cn/x", nil))
	if err != nil {
		t.Fatalf("内建白名单目标应放行，实际 %v", err)
	}
	resp.Body.Close()
}
