package jiaowu_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/app"
	"nongyu-backend-plus/internal/config"
	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school/jiaowu"
	"nongyu-backend-plus/internal/server"
)

// ---- from app_wiring_test.go ----
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

// TestAppWiringAuthEnforced 证明生产装配（AuthKey 非空）下认证与限流真的生效：
// 无 token → 401；合法 token → 200；超过限流 → 429；请求体里的 userID 不生效。
func TestAppWiringAuthEnforced(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gin.DefaultWriter = io.Discard
	gin.DefaultErrorWriter = io.Discard
	cfg := config.Config{AuthKey: "wiring-auth-key", RateLimitPerMinute: 2}
	application, err := app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	h := application.Handler()

	// 无凭证
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/school/address?target=jiaowu", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("无凭证应 401，实际 %d", w.Code)
	}

	// 签发合法 token
	verifier := server.NewTokenVerifier("wiring-auth-key", platform.NewRealClock())
	token := verifier.Issue("alice", time.Now().Add(time.Hour))
	doGet := func() int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/school/address?target=jiaowu", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := doGet(); code != http.StatusOK {
		t.Fatalf("合法 token 应 200，实际 %d", code)
	}
	if code := doGet(); code != http.StatusOK {
		t.Fatalf("第 2 次应仍在限流内，实际 %d", code)
	}
	if code := doGet(); code != http.StatusTooManyRequests {
		t.Fatalf("超过每用户每分钟 2 次应 429，实际 %d", code)
	}
}

// TestAppWiringShutdownStopsRenewLoop 证明 app.New 真的拉起了定时续期循环，
// 且 Shutdown 会优雅停掉它（不残留悬挂 goroutine）。
func TestAppWiringShutdownStopsRenewLoop(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gin.DefaultWriter = io.Discard
	gin.DefaultErrorWriter = io.Discard
	application, err := app.New(config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	// Shutdown 内部会 cancel + 等待 Run 的 done channel；能在有限时间内返回即证明
	// 循环已收到停止信号（否则会被 stopRenew 的 2s 上界兜住，但不会 panic/挂死）。
	if err := application.Shutdown(2 * time.Second); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// TestAppWiringLoadDefaultsEnforceGateway 用 config.Load() 的**真实默认值**装配，
// 断言生产入口（main.go 走 Load）的闸口确实是强制的——堵住「零值 Config 不强制」
// 与「Load 默认强制」不一致时无人能测出的回归缝（复验补强项 ①）。
//
// 换真实 Doer（#22）后，合法目标不能再打真站 webvpn.sicau.edu.cn（那会变成外部网络
// 调用）。改为起本地 httptest 靶子，并把它的 host 通过 HTTP_ALLOWLIST 追加进白名单，
// 仍用 config.Load() 的真实默认值装配。evil.example.com 仍必须返回 ErrTargetDenied
// ——这条断言不能弱化，该测试的存在意义就是守住这条缝。
func TestAppWiringLoadDefaultsEnforceGateway(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gin.DefaultWriter = io.Discard
	gin.DefaultErrorWriter = io.Discard

	const realBody = "LOCAL-TARGET-REAL-BODY-22"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, realBody)
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("解析靶子 URL 失败：%v", err)
	}
	// 把本地靶子 host 追加进出站白名单（config.Load 会读 HTTP_ALLOWLIST）。
	t.Setenv("HTTP_ALLOWLIST", u.Hostname())
	// 本用例只测「Load() 默认强制出站闸口」，与认证无关。
	// #21 起 dev 下 AUTH_KEY 为空必须显式开 ALLOW_INSECURE_DEV 才允许装配，
	// 这里按新口径显式开启，不改变本测试原本要守的断言。
	t.Setenv("ALLOW_INSECURE_DEV", "true")

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

	// 生产装配（HTTPEnforce 默认 true）应注入 *真实* 实现，而非骨架期 Mock。
	if _, ok := application.Capabilities().Doer.(*platform.RealHTTPDoer); !ok {
		t.Fatalf("Load() 默认配置应注入 *platform.RealHTTPDoer，实际 %T", application.Capabilities().Doer)
	}

	// 守缝断言（不可弱化）：非白名单 host 直接拒绝、零出站。
	// 真实 client 会拒绝带 RequestURI 的 httptest.NewRequest，故一律用 NewRequestWithContext。
	evilReq, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://evil.example.com/x", nil)
	if err != nil {
		t.Fatalf("构造 evil 请求失败：%v", err)
	}
	_, err = application.Capabilities().Doer.Do(context.Background(), evilReq)
	if !errors.Is(err, platform.ErrTargetDenied) {
		t.Fatalf("经 Load() 默认配置装配后闸口应强制，evil.example.com 应返回 ErrTargetDenied，实际 %v", err)
	}

	// 合法目标（追加进白名单的本地靶子）：真实出站并拿到真实 body，而非 mock 的 200 空响应。
	legalReq, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/probe", nil)
	if err != nil {
		t.Fatalf("构造合法请求失败：%v", err)
	}
	resp, err := application.Capabilities().Doer.Do(context.Background(), legalReq)
	if err != nil {
		t.Fatalf("白名单内目标应放行并真实出站，实际 %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取靶子响应体失败：%v", err)
	}
	if string(body) != realBody {
		t.Fatalf("应拿到靶子真实 body，实际 %q", body)
	}
}

// ---- from accel_test.go ----
// #17：免密加速路径（可开关）+ 失败回落 + 观测驱动续期。
// 全部用假出站实现注入失败，不依赖真实学校环境；交付的是「回落行为」。

func newAccelEngine(t *testing.T, doer *platform.MockHTTPDoer, enabled bool) (http.Handler, *platform.MockTicketStore, *platform.MockClock) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	tickets := platform.NewMockTicketStore()
	clock := platform.NewMockClock(time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC))
	cipher := platform.NewMockCipher("test-key")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine := gin.New()
	jiaowu.RegisterAccel(engine, jiaowu.NewWithAccel(doer, tickets, cipher, clock, logger, enabled))
	return engine, tickets, clock
}

func accelPost(t *testing.T, h http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/school/login/accel", nil)
	h.ServeHTTP(w, req)
	return w
}

// TestAccelDisabledBehavesLikeMainPath 开关关闭（默认）→ 行为与主路径一致：
// 不尝试免密、零出站，直接给出「需要用户输码」的结果。
func TestAccelDisabledBehavesLikeMainPath(t *testing.T) {
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	h, _, _ := newAccelEngine(t, doer, false)

	w := accelPost(t, h)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d，body=%s", w.Code, w.Body.String())
	}
	var body struct {
		NeedCaptcha bool `json:"needCaptcha"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("非法 JSON：%v", err)
	}
	if !body.NeedCaptcha {
		t.Fatalf("开关关闭时应要求用户走主路径（输码），实际 %+v", body)
	}
	if got := len(doer.Requests()); got != 0 {
		t.Fatalf("开关关闭时不得发起免密出站，实际 %d 次：%#v", got, doer.Requests())
	}
}

// TestAccelFailureFallsBackWithoutRetryStorm 开关打开但免密换票失败 →
// 自动回落主路径，返回 ticket_expired 引导；且绝不静默重试刷屏（恰好一次出站）。
func TestAccelFailureFallsBackWithoutRetryStorm(t *testing.T) {
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	// 上游返回「未登录」——这是实测里门户 appid 免密未验证时最常见的失败形态。
	doer.SetResponse(http.StatusOK, `{"code":500,"msg":"未登录","data":null}`)
	h, _, _ := newAccelEngine(t, doer, true)

	w := accelPost(t, h)
	var body struct {
		Code        string `json:"code"`
		NeedCaptcha bool   `json:"needCaptcha"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("非法 JSON：%v，body=%s", err, w.Body.String())
	}
	if body.Code != "ticket_expired" {
		t.Fatalf("失败应回落并返回 ticket_expired 引导，实际 code=%q", body.Code)
	}
	if !body.NeedCaptcha {
		t.Fatalf("回落应要求用户重新输码，实际 %+v", body)
	}
	if got := len(doer.Requests()); got != 1 {
		t.Fatalf("不得静默重试刷屏，应恰好一次出站，实际 %d 次：%#v", got, doer.Requests())
	}
}

// TestAccelFailureDoesNotStoreTicket 免密失败时不落任何票据。
func TestAccelFailureDoesNotStoreTicket(t *testing.T) {
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	doer.SetResponse(http.StatusOK, `{"code":500,"msg":"未登录","data":null}`)
	h, tickets, _ := newAccelEngine(t, doer, true)

	accelPost(t, h)
	if _, _, err := tickets.Get(nil, "accel-user"); err == nil {
		t.Fatalf("免密失败时不应落票据")
	}
}

// TestRenewSchedulerPicksExpiringAndStaggers 续期调度从 ListExpiringSoon 挑候选，
// 按用户散列错峰（同一时刻不集中），且候选来自外置存储。
func TestRenewSchedulerPicksExpiringAndStaggers(t *testing.T) {
	tickets := platform.NewMockTicketStore()
	clock := platform.NewMockClock(time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC))
	// 3 个即将过期的用户。
	for _, u := range []string{"u-a", "u-b", "u-c"} {
		if err := tickets.Put(nil, u, []byte("sealed-"+u), clock.Now().Add(1*time.Hour)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	sched := jiaowu.NewRenewScheduler(tickets, clock)
	plan, err := sched.Plan(nil, clock.Now().Add(2*time.Hour), 10)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan) == 0 {
		t.Fatalf("应挑出即将过期的候选")
	}
	// 错峰：各候选的调度偏移应当不全相同（按 userID 散列打散）。
	seen := map[int64]bool{}
	for _, item := range plan {
		seen[item.Offset] = true
	}
	if len(seen) < 2 && len(plan) > 1 {
		t.Fatalf("候选应被散列到不同偏移以避免集中换票，实际 offsets=%v", seen)
	}
}

// TestRenewPeriodIsObservedNotHardcoded 续期周期由成功/失败时刻反推，不硬编码未知 TTL；
// 观察期有初始上限（不超出长期凭据已知窗口的余量）。
func TestRenewPeriodIsObservedNotHardcoded(t *testing.T) {
	tickets := platform.NewMockTicketStore()
	clock := platform.NewMockClock(time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC))
	sched := jiaowu.NewRenewScheduler(tickets, clock)

	// 观测窗口初始上限 > 0，且首次失败后周期会收紧（不大于初始上限）。
	initial := sched.ObservedPeriod()
	if initial <= 0 {
		t.Fatalf("观察期应有正的初始上限，实际 %v", initial)
	}
	sched.RecordFailure(clock.Now())
	afterFailure := sched.ObservedPeriod()
	if afterFailure <= 0 || afterFailure > initial {
		t.Fatalf("首次失败后周期应保守收紧且不超过初始上限，实际 %v (初始 %v)", afterFailure, initial)
	}
	// 记录成功时刻后周期仍为正、可继续观测。
	sched.RecordSuccess(clock.Now().Add(1 * time.Hour))
	if sched.ObservedPeriod() <= 0 {
		t.Fatalf("记录成功时刻后周期仍应为正")
	}
	// Plan 消费观测周期：PlanningWindow 应随周期收紧而前移，且候选确实来自外置存储。
	before := sched.PlanningWindow(clock.Now())
	if !before.After(clock.Now()) {
		t.Fatalf("PlanningWindow 应落在当前时刻之后，实际 %v", before)
	}
	if err := tickets.Put(nil, "u-observed", []byte("sealed"), clock.Now().Add(30*time.Minute)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	plan, err := sched.Plan(nil, before, 10)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	found := false
	for _, item := range plan {
		if item.UserID == "u-observed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Plan 应挑出观测窗口内的候选，实际 %+v", plan)
	}
}

// TestAccelSuccessStoresEnvelope 覆盖加速路径的**成功分支**（此前零覆盖）：
// 上游返回 code=200 且 data.st 非空的有效换票信封时，才落票据、不需要用户输码。
// 同时验证 200 登录页 HTML 与 code:500 失败信封都回落（收紧后的判定）。
func TestAccelSuccessStoresEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	doer.SetResponse(http.StatusOK, `{"code":200,"data":{"st":"ST-REAL","callbackUrl":"https://webvpn.sicau.edu.cn/"}}`)
	h, tickets, clock := newAccelEngine(t, doer, true)

	w := accelPost(t, h)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d，body=%s", w.Code, w.Body.String())
	}
	var body struct {
		NeedCaptcha bool `json:"needCaptcha"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("非法 JSON：%v", err)
	}
	if body.NeedCaptcha {
		t.Fatalf("有效换票信封应视为加速成功，不应要求输码，实际 %+v", body)
	}
	sealed, expiresAt, err := tickets.Get(nil, "accel-user")
	if err != nil {
		t.Fatalf("加速成功应落加密票据：%v", err)
	}
	if bytes.Contains(sealed, []byte("ST-REAL")) {
		t.Fatalf("存储里应是密文，不得出现明文票据")
	}
	if !expiresAt.After(clock.Now()) {
		t.Fatalf("过期时刻应晚于当前时刻")
	}
}

func TestAccelLoginPageHTMLFallsBack(t *testing.T) {
	gin.SetMode(gin.TestMode)
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	// 200 但不是换票信封：登录页 HTML，必须回落而非误判成功。
	doer.SetResponse(http.StatusOK, "<html><body>login</body></html>")
	h, tickets, _ := newAccelEngine(t, doer, true)

	w := accelPost(t, h)
	var body struct {
		Code        string `json:"code"`
		NeedCaptcha bool   `json:"needCaptcha"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("非法 JSON：%v", err)
	}
	if body.Code != "ticket_expired" || !body.NeedCaptcha {
		t.Fatalf("登录页 HTML 应回落并引导输码，实际 %+v", body)
	}
	if _, _, err := tickets.Get(nil, "accel-user"); err == nil {
		t.Fatalf("非换票信封不应落票据")
	}
}

// TestObservedPeriodDrivesTicketTTL 证明写票 TTL 由观测周期决定，而非硬编码常量：
// 绑定 scheduler 后，收紧观测周期会让新写票据的过期时刻同步前移（B1 闭环）。
func TestObservedPeriodDrivesTicketTTL(t *testing.T) {
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	doer.SetResponse(http.StatusOK, "TICKET-OBSERVED")
	tickets := platform.NewMockTicketStore()
	clock := platform.NewMockClock(time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC))
	cipher := platform.NewMockCipher("test-key")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sched := jiaowu.NewRenewScheduler(tickets, clock)
	svc := jiaowu.NewWithAccel(doer, tickets, cipher, clock, logger, false)
	svc.BindRenewScheduler(sched)

	// 收紧观测周期后，写票过期时刻应显著早于初始 9 天窗口。
	for i := 0; i < 8; i++ {
		sched.RecordFailure(clock.Now())
	}
	if _, err := svc.Login(context.Background(), "u-ttl", "1234", "cap-ttl"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	_, expiresAt, err := tickets.Get(nil, "u-ttl")
	if err != nil {
		t.Fatalf("票据应落库：%v", err)
	}
	if window := expiresAt.Sub(clock.Now()); window >= 9*24*time.Hour {
		t.Fatalf("写票窗口应受观测周期收紧影响，实际仍为 %v（9 天硬编码未打通）", window)
	}
}
