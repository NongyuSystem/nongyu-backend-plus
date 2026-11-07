package presence_test

// 本文件是「校内/校外状态」核心模型的外部测试（package presence_test）。
// 刻意不 import internal/app / internal/config / internal/server，也不 import
// 任何其它业务子域：production 包只依赖 platform 接口，测试同样保持这个方向。
// 装配层（app.go）接线由 Lead 合并时完成，这里不起 app.New()，保证本包测试
// 不受并行子代理 #14 的 TDD 红态影响。

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school/presence"
)

// 固定时间源：所有陈旧判定都由 MockClock 驱动，测试不依赖真实时钟。
var baseTime = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

const testStaleAfter = 5 * time.Minute

// 编译期结构性保证：New 的构造参数里没有 platform.HTTPDoer，也没有任何
// ticker/后台 goroutine 的入口 —— 后端侧判定「不做周期性自探测、零出站」
// 不是靠运行时拦截，而是在类型签名上就不可能。装配层若想加出站能力，必须
// 先改这个签名（会被本行编译错误挡下）。
var _ func(platform.TicketStore, platform.Clock, presence.Mode, time.Duration) *presence.Service = presence.New

// newService 构造「后端配置 = ModeDirect（直连形态）、陈旧阈值 = testStaleAfter」
// 的子域服务，并回报同一份 store / clock 供断言。
// 注意：New 的构造参数里没有 HTTPDoer —— 这是「不做周期性自探测、零出站」的
// 结构性保证（见 TestBackendConfigReadAtConstructionNoOutboundOrProbing）。
func newService(t *testing.T) (*presence.Service, *platform.MockTicketStore, *platform.MockClock) {
	t.Helper()
	store := platform.NewMockTicketStore()
	clock := platform.NewMockClock(baseTime)
	svc := presence.New(store, clock, presence.ModeDirect, testStaleAfter)
	return svc, store, clock
}

func mustDecide(t *testing.T, svc *presence.Service, userID string) presence.Mode {
	t.Helper()
	m, err := svc.Decide(context.Background(), userID)
	if err != nil {
		t.Fatalf("Decide(%q) 返回错误：%v", userID, err)
	}
	return m
}

// TestDecideUsesBackendModeWhenNoReport：无上报 → 用后端启动配置；且构造本身不写
// 任何状态（不自探测、不外置写入），TicketStore 里应仍是 ErrNotFound。
func TestDecideUsesBackendModeWhenNoReport(t *testing.T) {
	svc, store, _ := newService(t)
	if got := mustDecide(t, svc, "u-no-report"); got != presence.ModeDirect {
		t.Fatalf("无上报应退回后端配置 ModeDirect，实际 %q", got)
	}
	if _, _, err := store.Get(context.Background(), "u-no-report"); !platform.IsNotFound(err) {
		t.Fatalf("构造后 TicketStore 不应有记录，实际 err=%v", err)
	}
}

// TestReportChangesDecideForSameUser：同一用户上报后判定随之改变（direct ↔ vpn）。
// 后端配置故意设为 direct，与上报的 vpn 不同，才能观察到「判定改变」。
func TestReportChangesDecideForSameUser(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := context.Background()
	if err := svc.Report(ctx, presence.ClientPresence{UserID: "u1", Mode: presence.ModeVPN}); err != nil {
		t.Fatalf("Report(vpn) 失败：%v", err)
	}
	if got := mustDecide(t, svc, "u1"); got != presence.ModeVPN {
		t.Fatalf("上报 vpn 后应判定为 vpn，实际 %q", got)
	}
	if err := svc.Report(ctx, presence.ClientPresence{UserID: "u1", Mode: presence.ModeDirect}); err != nil {
		t.Fatalf("Report(direct) 失败：%v", err)
	}
	if got := mustDecide(t, svc, "u1"); got != presence.ModeDirect {
		t.Fatalf("网络切换后上报 direct，应判定为 direct，实际 %q", got)
	}
}

// TestReportLandsInTicketStoreKeyedByUser：上报状态外置到 TicketStore（按用户键控），
// 且上报时刻由 Clock 盖章。记录应为会话级（expiresAt 零值），不参与票据错峰扫描。
func TestReportLandsInTicketStoreKeyedByUser(t *testing.T) {
	svc, store, clock := newService(t)
	ctx := context.Background()
	if err := svc.Report(ctx, presence.ClientPresence{UserID: "u-store", Mode: presence.ModeVPN}); err != nil {
		t.Fatalf("Report 失败：%v", err)
	}
	sealed, expiresAt, err := store.Get(ctx, "u-store")
	if err != nil {
		t.Fatalf("上报状态应落在 TicketStore（按用户键控），Get 失败：%v", err)
	}
	if !expiresAt.IsZero() {
		t.Fatalf("presence 记录应为会话级（expiresAt 零值），实际 %v", expiresAt)
	}
	var got presence.ClientPresence
	if err := json.Unmarshal(sealed, &got); err != nil {
		t.Fatalf("存储内容应是可反解的上报记录，sealed=%q, err=%v", sealed, err)
	}
	if got.UserID != "u-store" || got.Mode != presence.ModeVPN {
		t.Fatalf("存储内容与上报不符：%#v", got)
	}
	if !got.At.Equal(clock.Now()) {
		t.Fatalf("上报时刻应由 Clock 盖章，期望 %v，实际 %v", clock.Now(), got.At)
	}
}

// TestDecideFallsBackToBackendWhenReportIsStale：用 MockClock 把时间推进到阈值之外，
// 断言判定回落到后端配置。语义：「超过」阈值才判陈旧，恰好等于阈值仍算新鲜。
func TestDecideFallsBackToBackendWhenReportIsStale(t *testing.T) {
	svc, _, clock := newService(t)
	ctx := context.Background()
	if err := svc.Report(ctx, presence.ClientPresence{UserID: "u-stale", Mode: presence.ModeVPN}); err != nil {
		t.Fatalf("Report 失败：%v", err)
	}
	clock.Set(baseTime.Add(testStaleAfter - time.Nanosecond))
	if got := mustDecide(t, svc, "u-stale"); got != presence.ModeVPN {
		t.Fatalf("阈值内应仍用上报 vpn，实际 %q", got)
	}
	clock.Set(baseTime.Add(testStaleAfter))
	if got := mustDecide(t, svc, "u-stale"); got != presence.ModeVPN {
		t.Fatalf("恰好等于阈值仍未「超过」，应仍用上报 vpn，实际 %q", got)
	}
	clock.Set(baseTime.Add(testStaleAfter + time.Nanosecond))
	if got := mustDecide(t, svc, "u-stale"); got != presence.ModeDirect {
		t.Fatalf("超过阈值未更新应退回后端配置 ModeDirect，实际 %q", got)
	}
}

// TestFreshReportRefreshesStalenessWindow：「网络切换时再上报一次」应把陈旧窗口
// 从新上报时刻重新计时，而不是沿用首次上报时刻。
func TestFreshReportRefreshesStalenessWindow(t *testing.T) {
	svc, _, clock := newService(t)
	ctx := context.Background()
	if err := svc.Report(ctx, presence.ClientPresence{UserID: "u-refresh", Mode: presence.ModeVPN}); err != nil {
		t.Fatalf("首次 Report 失败：%v", err)
	}
	t1 := baseTime.Add(testStaleAfter - time.Minute)
	clock.Set(t1)
	if err := svc.Report(ctx, presence.ClientPresence{UserID: "u-refresh", Mode: presence.ModeVPN}); err != nil {
		t.Fatalf("刷新 Report 失败：%v", err)
	}
	clock.Set(t1.Add(testStaleAfter - time.Second))
	if got := mustDecide(t, svc, "u-refresh"); got != presence.ModeVPN {
		t.Fatalf("刷新后阈值内应仍用上报 vpn，实际 %q", got)
	}
	clock.Set(t1.Add(testStaleAfter + time.Second))
	if got := mustDecide(t, svc, "u-refresh"); got != presence.ModeDirect {
		t.Fatalf("刷新后超过阈值应退回后端配置，实际 %q", got)
	}
}

// TestStateIsExternalNotProcessLocal：两个独立 Service 实例共享同一 store，
// A 上报、B 判定也应读到同一份状态 —— 证明状态外置、进程内无状态。
func TestStateIsExternalNotProcessLocal(t *testing.T) {
	store := platform.NewMockTicketStore()
	clock := platform.NewMockClock(baseTime)
	svcA := presence.New(store, clock, presence.ModeDirect, testStaleAfter)
	svcB := presence.New(store, clock, presence.ModeDirect, testStaleAfter)
	if err := svcA.Report(context.Background(), presence.ClientPresence{UserID: "u-shared", Mode: presence.ModeVPN}); err != nil {
		t.Fatalf("svcA.Report 失败：%v", err)
	}
	if got := mustDecide(t, svcB, "u-shared"); got != presence.ModeVPN {
		t.Fatalf("另一个 Service 实例应读到同一份外置状态，实际 %q", got)
	}
}

// TestBackendConfigReadAtConstructionNoOutboundOrProbing：后端侧模式在构造时读一次、
// 进程内不变；Service 不持有 HTTPDoer、无后台 goroutine/ticker，因此不做周期性自探测、
// 不发起任何出站请求。即便环境里存在 MockHTTPDoer，Requests() 也必须为零。
func TestBackendConfigReadAtConstructionNoOutboundOrProbing(t *testing.T) {
	doer := platform.NewMockHTTPDoer(platform.DoerPolicy{})
	store := platform.NewMockTicketStore()
	clock := platform.NewMockClock(baseTime)
	svc := presence.New(store, clock, presence.ModeDirect, testStaleAfter)

	if got := svc.BackendMode(); got != presence.ModeDirect {
		t.Fatalf("后端配置应在构造时固定为 ModeDirect，实际 %q", got)
	}
	// 推进时钟、反复判定、再上报 —— 都不应改变后端配置，也不产生出站请求。
	for i := 0; i < 3; i++ {
		clock.Set(baseTime.Add(time.Duration(i) * time.Hour))
		_ = mustDecide(t, svc, "u-outbound")
	}
	if err := svc.Report(context.Background(), presence.ClientPresence{UserID: "u-outbound", Mode: presence.ModeVPN}); err != nil {
		t.Fatalf("Report 失败：%v", err)
	}
	if got := svc.BackendMode(); got != presence.ModeDirect {
		t.Fatalf("后端配置不应随时间/上报变化，实际 %q", got)
	}
	if got := len(doer.Requests()); got != 0 {
		t.Fatalf("后端侧判定不得发起任何出站请求，实际记录到 %d 次：%#v", got, doer.Requests())
	}
}

// TestReportRejectsInvalidInput：非法入参在闸口处拒绝，且不污染外置存储。
func TestReportRejectsInvalidInput(t *testing.T) {
	svc, store, _ := newService(t)
	ctx := context.Background()
	if err := svc.Report(ctx, presence.ClientPresence{UserID: "u", Mode: presence.Mode("bogus")}); !errors.Is(err, presence.ErrInvalidMode) {
		t.Fatalf("非法 mode 应返回 ErrInvalidMode，实际 %v", err)
	}
	if err := svc.Report(ctx, presence.ClientPresence{UserID: "", Mode: presence.ModeDirect}); !errors.Is(err, presence.ErrEmptyUserID) {
		t.Fatalf("空 userID 应返回 ErrEmptyUserID，实际 %v", err)
	}
	if _, _, err := store.Get(ctx, "u"); !platform.IsNotFound(err) {
		t.Fatalf("非法上报不应写入 TicketStore，实际 err=%v", err)
	}
	if got := mustDecide(t, svc, "u"); got != presence.ModeDirect {
		t.Fatalf("非法上报后仍应退回后端配置，实际 %q", got)
	}
}

// TestNewCoercesInvalidConstructorArgsToDefaults：非法后端模式 / 非正陈旧阈值
// 回落到文档化的安全默认值，装配层传空值也不会让服务进入不确定状态。
func TestNewCoercesInvalidConstructorArgsToDefaults(t *testing.T) {
	store := platform.NewMockTicketStore()
	clock := platform.NewMockClock(baseTime)
	svc := presence.New(store, clock, presence.Mode(""), 0)
	if got := svc.BackendMode(); got != presence.DefaultBackendMode {
		t.Fatalf("非法后端模式应回落到 DefaultBackendMode，实际 %q", got)
	}
	if got := svc.StaleAfter(); got != presence.DefaultStaleAfter {
		t.Fatalf("非正陈旧阈值应回落到 DefaultStaleAfter，实际 %v", got)
	}
}
