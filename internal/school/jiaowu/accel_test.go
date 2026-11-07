package jiaowu_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school/jiaowu"
)

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
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	tickets := platform.NewMockTicketStore()
	clock := platform.NewMockClock(time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC))
	// 3 个即将过期的用户。
	for _, u := range []string{"u-a", "u-b", "u-c"} {
		if err := tickets.Put(nil, u, []byte("sealed-"+u), clock.Now().Add(1*time.Hour)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	sched := jiaowu.NewRenewScheduler(tickets, clock, doer)
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
	doer := platform.NewMockHTTPDoer(enforcedPolicy())
	tickets := platform.NewMockTicketStore()
	clock := platform.NewMockClock(time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC))
	sched := jiaowu.NewRenewScheduler(tickets, clock, doer)

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
	sched := jiaowu.NewRenewScheduler(tickets, clock, doer)
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
