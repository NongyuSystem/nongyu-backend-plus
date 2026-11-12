package track_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/platform/pgstore"
	"nongyu-backend-plus/internal/track"
)

func init() { gin.SetMode(gin.TestMode) }

var t0 = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

type fixedClock struct{ now time.Time }

func (f fixedClock) Now() time.Time { return f.now }

// ---------- 脱敏纯函数（顺序是硬约束）----------

func TestSanitize_TopLevelAllowlistDropsUnknown(t *testing.T) {
	raw := map[string]any{
		"eventId":   "e1",
		"eventType": "screen_view",
		"platform":  "android",
		// 未在白名单里的字段必须被丢弃，且不报错
		"userId":       float64(999), // 客户端自报身份：必须丢
		"idCard":       "110101199001011234",
		"wholeProfile": map[string]any{"name": "张三"},
	}
	res, err := track.Sanitize(raw)
	if err != nil {
		t.Fatalf("脱敏不该失败：%v", err)
	}
	if res.DroppedTopLevel != 3 {
		t.Fatalf("应丢弃 3 个未知顶层字段，得到 %d", res.DroppedTopLevel)
	}
	// 关键：客户端自报的 userId 不能被保留（服务端另行注入）
	if res.Event.UserID != 0 {
		t.Fatalf("客户端自报 userId 不该进 Event，得到 %d", res.Event.UserID)
	}
}

func TestSanitize_RemovesSensitivePropsCaseInsensitive(t *testing.T) {
	raw := map[string]any{
		"eventId":   "e1",
		"eventType": "button_click",
		"platform":  "web",
		"props": map[string]any{
			"userPassword": "x",
			"accessToken":  "y",
			"idCardNo":     "z",
			"cookie":       "c",
			"page":         "/home", // 正常字段应保留
			"durationMs":   float64(120),
		},
	}
	res, err := track.Sanitize(raw)
	if err != nil {
		t.Fatalf("脱敏失败：%v", err)
	}
	if res.RemovedProps != 4 {
		t.Fatalf("应剔除 4 个敏感键，得到 %d（props=%v）", res.RemovedProps, res.Event.Props)
	}
	if res.Event.Props["page"] != "/home" {
		t.Fatal("正常字段应保留")
	}
	if _, has := res.Event.Props["userPassword"]; has {
		t.Fatal("userPassword 必须被剔除")
	}
}

func TestSanitize_TruncatesAfterRemoval(t *testing.T) {
	// 构造一个「剔除敏感键后仍超 4KB」的 props。
	big := map[string]any{}
	for i := 0; i < 200; i++ {
		big["k"+strconv.Itoa(i)] = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	}
	big["password"] = "must-be-removed"
	raw := map[string]any{
		"eventId": "e1", "eventType": "perf", "platform": "ios", "props": big,
	}
	res, err := track.Sanitize(raw)
	if err != nil {
		t.Fatalf("脱敏失败：%v", err)
	}
	if res.RemovedProps != 1 {
		t.Fatalf("应剔除 1 个敏感键，得到 %d", res.RemovedProps)
	}
	if !res.PropsTruncated {
		t.Fatal("超 4KB 应被截断（整体丢弃 props）")
	}
	if res.Event.Props != nil {
		t.Fatal("截断后 props 应为 nil")
	}
}

func TestSanitize_OrderMatters_RemovalBeforeTruncation(t *testing.T) {
	// 这条锁「顺序不可颠倒」：敏感键必须**先**被剔除，再判断是否超限。
	// 如果一个刚好卡在 4KB 边界、又含敏感键的 props 先被截断，
	// 敏感值就可能以「不完整片段」的形式留下。
	props := map[string]any{}
	// 精确控制到刚好不超过 4KB 的量级
	for i := 0; i < 100; i++ {
		props["k"+strconv.Itoa(i)] = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	}
	props["token"] = "SECRET_VALUE_SHOULD_NEVER_SURVIVE"

	raw := map[string]any{
		"eventId": "e1", "eventType": "app_open", "platform": "android", "props": props,
	}
	res, err := track.Sanitize(raw)
	if err != nil {
		t.Fatalf("脱敏失败：%v", err)
	}
	// 无论是否被截断，敏感键都必须已被剔除
	if res.RemovedProps != 1 {
		t.Fatalf("敏感键必须先被剔除，得到 RemovedProps=%d", res.RemovedProps)
	}
	if res.Event.Props != nil {
		if v, has := res.Event.Props["token"]; has {
			t.Fatalf("敏感值泄漏：%v", v)
		}
	}
}

func TestSanitize_RejectsServerOnlyType(t *testing.T) {
	raw := map[string]any{
		"eventId": "e1", "eventType": "llm_proxy_fail", "platform": "web",
	}
	if _, err := track.Sanitize(raw); err == nil {
		t.Fatal("llm_proxy_fail 仅服务端，客户端上报必须被拒")
	}
}

func TestSanitize_RejectsUnknownEnum(t *testing.T) {
	for _, bad := range []map[string]any{
		{"eventId": "e1", "eventType": "nope", "platform": "web"},
		{"eventId": "e1", "eventType": "perf", "platform": "symbian"},
		{"eventId": "", "eventType": "perf", "platform": "web"},
	} {
		if _, err := track.Sanitize(bad); err == nil {
			t.Fatalf("应被拒：%v", bad)
		}
	}
}

// ---------- HTTP 集成 ----------

func newEnv(t *testing.T) (*gin.Engine, *track.Service, *pgstore.TrackMemoryRepo) {
	t.Helper()
	repo := pgstore.NewTrackMemoryRepo()
	svc := track.New(track.Options{
		Repo:  repo,
		Clock: fixedClock{now: t0},
		// 小容量 + 短周期，让测试能快速观察到 flush
		BufferOptions: track.BufferOptions{Capacity: 4, BatchSize: 2, FlushPeriod: 10 * time.Millisecond},
	})
	uidOf := func(c *gin.Context) (int64, bool) {
		raw := c.GetHeader("X-Test-UID")
		if raw == "" {
			return 0, false
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return 0, false
		}
		return id, true
	}
	r := gin.New()
	track.Register(r, svc, uidOf)
	track.RegisterAdmin(r, svc)
	return r, svc, repo
}

func post(t *testing.T, r *gin.Engine, uid string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/track/events", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if uid != "" {
		req.Header.Set("X-Test-UID", uid)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	out := map[string]any{}
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &out)
	}
	return w.Code, out
}

func TestReport_PartialSuccess(t *testing.T) {
	r, _, _ := newEnv(t)
	ok := func(id string) map[string]any {
		return map[string]any{"eventId": id, "eventType": "screen_view", "platform": "web"}
	}
	bad := map[string]any{"eventId": "bad", "eventType": "llm_proxy_fail", "platform": "web"}

	code, body := post(t, r, "7", map[string]any{"events": []any{ok("a"), bad, ok("b")}})
	if code != http.StatusOK {
		t.Fatalf("应 200（单条失败不整批拒），得到 %d %v", code, body)
	}
	// accepted=2, rejected=1
	if body["accepted"].(float64) != 2 {
		t.Fatalf("accepted 应为 2，得到 %v", body["accepted"])
	}
	if body["rejected"].(float64) != 1 {
		t.Fatalf("rejected 应为 1，得到 %v", body["rejected"])
	}
}

func TestReport_RejectsEmptyAndOversizedBatch(t *testing.T) {
	r, _, _ := newEnv(t)
	if code, _ := post(t, r, "7", map[string]any{"events": []any{}}); code != http.StatusBadRequest {
		t.Fatalf("空批量应 400，得到 %d", code)
	}
	items := make([]any, 101)
	for i := range items {
		items[i] = map[string]any{"eventId": strconv.Itoa(i), "eventType": "perf", "platform": "web"}
	}
	if code, _ := post(t, r, "7", map[string]any{"events": items}); code != http.StatusBadRequest {
		t.Fatalf("超过 100 条应 400，得到 %d", code)
	}
}

func TestReport_RequiresAuth(t *testing.T) {
	r, _, _ := newEnv(t)
	if code, _ := post(t, r, "", map[string]any{"events": []any{}}); code != http.StatusUnauthorized {
		t.Fatalf("未登录应 401，得到 %d", code)
	}
}

func TestReport_EventIDIsIdempotent(t *testing.T) {
	r, svc, repo := newEnv(t)
	ev := map[string]any{"eventId": "same-id", "eventType": "perf", "platform": "web"}

	for i := 0; i < 3; i++ {
		if code, _ := post(t, r, "7", map[string]any{"events": []any{ev}}); code != http.StatusOK {
			t.Fatal("上报失败")
		}
	}
	svc.Flush()
	// 同 eventId 三次只应落 1 条
	if got := repo.Count(); got != 1 {
		t.Fatalf("同 eventId 应只落 1 条，得到 %d", got)
	}
}

func TestReport_BufferFullDoesNotReturn503(t *testing.T) {
	// 容量 4、BatchSize 2：连续塞入远超容量的条数，且后台消费被拖慢，
	// 用来验证「缓冲满不返回 503」这条硬约束。
	r, svc, _ := newEnv(t)

	for i := 0; i < 20; i++ {
		items := make([]any, 0, 2)
		for j := 0; j < 2; j++ {
			items = append(items, map[string]any{
				"eventId":   "e" + strconv.Itoa(i) + "-" + strconv.Itoa(j),
				"eventType": "heartbeat", "platform": "android",
			})
		}
		code, body := post(t, r, "7", map[string]any{"events": items})
		if code != http.StatusOK {
			t.Fatalf("缓冲满也必须 200，得到 %d %v", code, body)
		}
	}
	svc.Flush()
	// dropped 可能 > 0，但那不是错误；这里只断言「没报错」这条已由上面的 200 保证。
}

func TestReport_ServerInjectsUserID(t *testing.T) {
	r, svc, repo := newEnv(t)
	// 客户端试图自报 userId=999
	ev := map[string]any{
		"eventId": "e1", "eventType": "perf", "platform": "web",
		"userId": float64(999),
	}
	if code, _ := post(t, r, "7", map[string]any{"events": []any{ev}}); code != http.StatusOK {
		t.Fatal("上报失败")
	}
	svc.Flush()
	events := repo.Events()
	if len(events) != 1 {
		t.Fatalf("应落 1 条，得到 %d", len(events))
	}
	if events[0].UserID != 7 {
		t.Fatalf("userId 必须由服务端注入为 7，得到 %d（客户端自报被采纳了）", events[0].UserID)
	}
}

func TestSummary_OnlyCountsByType(t *testing.T) {
	r, svc, _ := newEnv(t)
	for i := 0; i < 3; i++ {
		post(t, r, "7", map[string]any{"events": []any{
			map[string]any{"eventId": "p" + strconv.Itoa(i), "eventType": "perf", "platform": "web"},
		}})
	}
	post(t, r, "7", map[string]any{"events": []any{
		map[string]any{"eventId": "s1", "eventType": "screen_view", "platform": "web"},
	}})
	svc.Flush()

	req := httptest.NewRequest(http.MethodGet, "/track/events/summary", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("summary 应 200，得到 %d", w.Code)
	}
	var out struct {
		Counts map[string]int `json:"counts"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)

	if out.Counts["perf"] != 3 {
		t.Fatalf("perf 应为 3，得到 %d", out.Counts["perf"])
	}
	if out.Counts["screen_view"] != 1 {
		t.Fatalf("screen_view 应为 1，得到 %d", out.Counts["screen_view"])
	}
	// 只给计数，不能带 props 明细
	if bytes.Contains(w.Body.Bytes(), []byte("props")) {
		t.Fatal("summary 不该返回 props 明细")
	}
}

func TestBuffer_FlushIsIdempotent(t *testing.T) {
	repo := pgstore.NewTrackMemoryRepo()
	b := track.NewBuffer(repo, track.BufferOptions{Capacity: 8, BatchSize: 2, FlushPeriod: time.Hour})
	ctx := context.Background()
	_ = ctx
	b.Enqueue(track.Event{EventID: "x", EventType: track.TypePerf, ReceivedAt: t0})

	b.Flush()
	b.Flush() // 重复调用不能 panic / 死锁
}
