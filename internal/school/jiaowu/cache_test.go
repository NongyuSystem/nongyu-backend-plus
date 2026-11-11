package jiaowu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school"
)

// ── 测试替身 ─────────────────────────────────────────────────────────────

// memCache 是 CacheStore 的进程内实现，语义与 platform.RedisCache 对齐：
// 键不存在返回 (nil, nil)，Set 覆盖写，TryAcquire 是「到期自动放开」的 SET NX。
type memCache struct {
	mu      sync.Mutex
	data    map[string][]byte
	expires map[string]time.Time
	locks   map[string]time.Time
	now     func() time.Time
	// failGet/failSet 用于模拟缓存层故障。
	failGet bool
	failSet bool
}

func newMemCache(now func() time.Time) *memCache {
	return &memCache{
		data:    make(map[string][]byte),
		expires: make(map[string]time.Time),
		locks:   make(map[string]time.Time),
		now:     now,
	}
}

func (m *memCache) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failGet {
		return nil, fmt.Errorf("模拟缓存读取故障")
	}
	if exp, ok := m.expires[key]; ok && m.now().After(exp) {
		delete(m.data, key)
		delete(m.expires, key)
		return nil, nil
	}
	v, ok := m.data[key]
	if !ok {
		return nil, nil
	}
	// 返回副本，避免调用方改动内部状态。
	out := make([]byte, len(v))
	copy(out, v)
	return out, nil
}

func (m *memCache) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failSet {
		return fmt.Errorf("模拟缓存写入故障")
	}
	cp := make([]byte, len(value))
	copy(cp, value)
	m.data[key] = cp
	m.expires[key] = m.now().Add(ttl)
	return nil
}

func (m *memCache) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	delete(m.expires, key)
	return nil
}

func (m *memCache) TryAcquire(_ context.Context, key string, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if exp, ok := m.locks[key]; ok && m.now().Before(exp) {
		return false, nil
	}
	m.locks[key] = m.now().Add(ttl)
	return true, nil
}

// forceAge 把某个键的 fetchedAt 往前拨，用来测新鲜度窗口与 7 天上限。
func (m *memCache) forceAge(t *testing.T, userID string, dt DataType, age time.Duration) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	key := cacheDataKey(userID, dt)
	raw, ok := m.data[key]
	if !ok {
		t.Fatalf("缓存里没有 %q，无法调整年龄", key)
	}
	var entry cachedEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("解缓存条目失败: %v", err)
	}
	entry.FetchedAt = entry.FetchedAt.Add(-age)
	next, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("重编缓存条目失败: %v", err)
	}
	m.data[key] = next
}

// fakeClock 让测试能直接推进时间（服务端用它给 fetchedAt 盖章）。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Advance 实现 platform.Clock 里可能的推进接口；这里只给测试用。
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// ── 测试脚手架 ───────────────────────────────────────────────────────────

// newCacheTestServer 装配一个只有缓存路由的 gin 引擎。
//
// 认证用最薄的中间件模拟：固定塞一个 userID 进上下文，
// 这样测的是缓存子域本身，而不是鉴权（鉴权已有独立测试）。
func newCacheTestServer(t *testing.T, userID string) (*gin.Engine, *Service, *memCache, *fakeClock) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	clock := newFakeClock()
	cache := newMemCache(func() time.Time { return clock.Now() })

	svc := NewWithAuth(
		platform.NewMockHTTPDoer(platform.DoerPolicy{}),
		platform.NewMockTicketStore(),
		platform.NewMockCipher("test-key"),
		clock,
		nil,
	)
	validator, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("编译 schema 失败: %v", err)
	}
	svc.BindCache(cache)
	svc.BindSchemaValidator(validator)

	engine := gin.New()
	if userID != "" {
		engine.Use(func(c *gin.Context) {
			c.Set(school.CtxUserID, userID)
			c.Next()
		})
	}
	RegisterCache(engine, svc)
	return engine, svc, cache, clock
}

// doJSON 发一个请求并返回状态码与响应体。
func doJSON(t *testing.T, engine *gin.Engine, method, path, body string) (int, map[string]any) {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, r)

	out := map[string]any{}
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &out)
	}
	return w.Code, out
}

const validCourseBody = `{"schemaVersion":1,"payload":[
	{"courseName":"Web设计与应用","weeks":"1-16","scheduleTime":"3-3,3-4","credit":"2","teacher":"张XX"}]}`

// ── 用例 ─────────────────────────────────────────────────────────────────

// TestCache_FullRoundTrip 是最关键的一条：未命中 → 回传 → 命中，全链走通。
//
// 它同时验证 #40 定的两件事：未命中回 200 + code=cache_miss（不是 404），
// 以及命中时直接返回 payload 不打教务。
func TestCache_FullRoundTrip(t *testing.T) {
	engine, _, _, _ := newCacheTestServer(t, "user-1")

	// 1) 首次 GET：必须是 200 + cache_miss。
	code, body := doJSON(t, engine, http.MethodGet, "/school/cache/course", "")
	if code != http.StatusOK {
		t.Fatalf("未命中应回 200，实际 %d", code)
	}
	if body["code"] != "cache_miss" {
		t.Fatalf("未命中应回 code=cache_miss，实际 %v", body["code"])
	}

	// 2) POST 回传一份合法数据。
	code, body = doJSON(t, engine, http.MethodPost, "/school/cache/course", validCourseBody)
	if code != http.StatusOK {
		t.Fatalf("回传应成功，实际 %d，body=%v", code, body)
	}
	if body["stale"] != false {
		t.Errorf("刚写入的数据 stale 应为 false，实际 %v", body["stale"])
	}
	if body["schemaVersion"] != float64(1) {
		t.Errorf("schemaVersion 应为 1，实际 %v", body["schemaVersion"])
	}
	if !strings.Contains(fmt.Sprint(body["fetchedAt"]), "2026-11-11") {
		t.Errorf("fetchedAt 应由服务端盖章为当前时间，实际 %v", body["fetchedAt"])
	}

	// 3) 再 GET：必须命中，且带回原来的 payload。
	code, body = doJSON(t, engine, http.MethodGet, "/school/cache/course", "")
	if code != http.StatusOK {
		t.Fatalf("命中应回 200，实际 %d", code)
	}
	if body["code"] == "cache_miss" {
		t.Fatal("写入后仍报 cache_miss")
	}
	payload, ok := body["payload"].([]any)
	if !ok || len(payload) != 1 {
		t.Fatalf("payload 应为长度 1 的数组，实际 %v", body["payload"])
	}
	first, _ := payload[0].(map[string]any)
	if first["courseName"] != "Web设计与应用" {
		t.Errorf("payload 内容错位，courseName = %v", first["courseName"])
	}
	if body["stale"] != false {
		t.Errorf("正常命中 stale 应为 false，实际 %v", body["stale"])
	}
}

// TestCache_HitDoesNotTouchUpstream 验证「命中零出站」——这是缓存存在的意义。
//
// 用 MockHTTPDoer 计数：命中路径上不应产生任何上游请求。
func TestCache_HitDoesNotTouchUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	clock := newFakeClock()
	cache := newMemCache(func() time.Time { return clock.Now() })
	doer := platform.NewMockHTTPDoer(platform.DoerPolicy{})

	svc := NewWithAuth(doer, platform.NewMockTicketStore(), platform.NewMockCipher("k"), clock, nil)
	validator, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("编译 schema 失败: %v", err)
	}
	svc.BindCache(cache)
	svc.BindSchemaValidator(validator)

	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set(school.CtxUserID, "user-1"); c.Next() })
	RegisterCache(engine, svc)

	if code, _ := doJSON(t, engine, http.MethodPost, "/school/cache/course", validCourseBody); code != http.StatusOK {
		t.Fatalf("回传失败，code=%d", code)
	}
	before := len(doer.Requests())

	// 连续命中若干次。
	for i := 0; i < 3; i++ {
		code, body := doJSON(t, engine, http.MethodGet, "/school/cache/course", "")
		if code != http.StatusOK || body["code"] == "cache_miss" {
			t.Fatalf("第 %d 次命中失败: code=%d body=%v", i, code, body)
		}
	}

	if after := len(doer.Requests()); after != before {
		t.Fatalf("命中路径产生了 %d 次上游请求（应为零出站）", after-before)
	}
}

// TestCache_RejectBadSchemaAndKeepOldData 验证 #41 Q5：
// 校验不过时既不写缓存，也不动旧缓存。
func TestCache_RejectBadSchemaAndKeepOldData(t *testing.T) {
	engine, _, _, _ := newCacheTestServer(t, "user-1")

	// 先写入一份好的。
	if code, _ := doJSON(t, engine, http.MethodPost, "/school/cache/course", validCourseBody); code != http.StatusOK {
		t.Fatalf("首次回传失败，code=%d", code)
	}

	// 再回传一份坏的（缺 courseName）。
	bad := `{"schemaVersion":1,"payload":[{"weeks":"1-16"}]}`
	code, body := doJSON(t, engine, http.MethodPost, "/school/cache/course", bad)
	if code != http.StatusBadRequest {
		t.Fatalf("坏数据应回 400，实际 %d", code)
	}
	if body["code"] != "schema_rejected" {
		t.Fatalf("错误码应为 schema_rejected，实际 %v", body["code"])
	}

	// 旧数据必须还在。
	code, body = doJSON(t, engine, http.MethodGet, "/school/cache/course", "")
	if code != http.StatusOK || body["code"] == "cache_miss" {
		t.Fatalf("坏数据被拒后旧缓存丢了: code=%d body=%v", code, body)
	}
	payload, _ := body["payload"].([]any)
	if len(payload) != 1 {
		t.Fatalf("旧缓存内容被破坏，payload=%v", body["payload"])
	}
}

// TestCache_RejectUnknownDataType 验证受控词表：未知 dataType 回 400。
func TestCache_RejectUnknownDataType(t *testing.T) {
	engine, _, _, _ := newCacheTestServer(t, "user-1")

	for _, name := range []string{"unknown", "Course", "scores"} {
		code, body := doJSON(t, engine, http.MethodGet, "/school/cache/"+name, "")
		if code != http.StatusBadRequest {
			t.Errorf("未知 dataType %q 应回 400，实际 %d", name, code)
		}
		if body["code"] != "target_denied" {
			t.Errorf("错误码应为 target_denied，实际 %v", body["code"])
		}
	}
}

// TestCache_RequiresUserID 验证身份只来自上下文：没有认证就 401。
func TestCache_RequiresUserID(t *testing.T) {
	engine, _, _, _ := newCacheTestServer(t, "") // 不注入 userID

	code, body := doJSON(t, engine, http.MethodGet, "/school/cache/course", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("缺少身份应回 401，实际 %d", code)
	}
	if body["code"] != "unauthorized" {
		t.Errorf("错误码应为 unauthorized，实际 %v", body["code"])
	}
}

// TestCache_StaleOnlyOnFallback 精确锁定 #32 对 stale 的限定：
// 只有「客户端上报失败后带 fallback=1」才会拿到 stale=true；
// 正常 GET 一旦超过新鲜度窗口就是 cache_miss，绝不悄悄给旧数据。
func TestCache_StaleOnlyOnFallback(t *testing.T) {
	engine, _, cache, clock := newCacheTestServer(t, "user-1")

	if code, _ := doJSON(t, engine, http.MethodPost, "/school/cache/course", validCourseBody); code != http.StatusOK {
		t.Fatalf("回传失败")
	}

	// 刚写完：正常 GET 命中、stale=false。
	_, body := doJSON(t, engine, http.MethodGet, "/school/cache/course", "")
	if body["stale"] != false {
		t.Fatalf("新鲜数据 stale 应为 false，实际 %v", body["stale"])
	}

	// 推过课表的 1 小时新鲜度窗口（但仍在 7 天内）。
	clock.Advance(90 * time.Minute)
	cache.forceAge(t, "user-1", DataTypeCourse, 90*time.Minute)

	// 正常 GET：应当 cache_miss（数据还在，但不新鲜了，让客户端去取新的）。
	_, body = doJSON(t, engine, http.MethodGet, "/school/cache/course", "")
	if body["code"] != "cache_miss" {
		t.Fatalf("超过新鲜度窗口应回 cache_miss，实际 %v", body)
	}

	// fallback=1：应拿到旧数据 + stale=true。
	code, body := doJSON(t, engine, http.MethodGet, "/school/cache/course?fallback=1", "")
	if code != http.StatusOK {
		t.Fatalf("fallback 应回 200，实际 %d", code)
	}
	if body["stale"] != true {
		t.Fatalf("fallback 下 stale 应为 true，实际 %v", body["stale"])
	}
	if payload, _ := body["payload"].([]any); len(payload) != 1 {
		t.Fatalf("fallback 应仍返回旧 payload，实际 %v", body["payload"])
	}
}

// TestCache_StaleHardCap 验证 7 天硬上限：再旧就不给了（#32 Q3）。
func TestCache_StaleHardCap(t *testing.T) {
	engine, _, cache, _ := newCacheTestServer(t, "user-1")

	if code, _ := doJSON(t, engine, http.MethodPost, "/school/cache/course", validCourseBody); code != http.StatusOK {
		t.Fatalf("回传失败")
	}

	// 把 fetchedAt 往前拨 8 天（超过 7 天上限）。
	cache.forceAge(t, "user-1", DataTypeCourse, 8*24*time.Hour)

	code, body := doJSON(t, engine, http.MethodGet, "/school/cache/course?fallback=1", "")
	if code != http.StatusOK {
		t.Fatalf("超期应回 200（cache_miss 语义），实际 %d", code)
	}
	if body["code"] != "cache_miss" {
		t.Fatalf("超过 7 天硬上限必须回 cache_miss，实际 %v", body)
	}
}

// TestCache_InvalidateThrottled 验证 #34：60 秒内重复失效回 429，且带 Retry-After。
func TestCache_InvalidateThrottled(t *testing.T) {
	engine, _, _, clock := newCacheTestServer(t, "user-1")

	if code, _ := doJSON(t, engine, http.MethodPost, "/school/cache/course", validCourseBody); code != http.StatusOK {
		t.Fatalf("回传失败")
	}

	// 第一次失效：成功。
	code, body := doJSON(t, engine, http.MethodDelete, "/school/cache/course", "")
	if code != http.StatusOK {
		t.Fatalf("首次失效应成功，实际 %d body=%v", code, body)
	}
	if body["invalidated"] != true {
		t.Errorf("应返回 invalidated=true，实际 %v", body["invalidated"])
	}

	// 失效之后缓存应真的没了。
	_, body = doJSON(t, engine, http.MethodGet, "/school/cache/course", "")
	if body["code"] != "cache_miss" {
		t.Fatalf("失效后应 cache_miss，实际 %v", body)
	}

	// 立即再来一次：应被节流。
	code, body = doJSON(t, engine, http.MethodDelete, "/school/cache/course", "")
	if code != http.StatusTooManyRequests {
		t.Fatalf("窗口内重复失效应回 429，实际 %d", code)
	}
	if body["code"] != "refresh_too_soon" {
		t.Errorf("错误码应为 refresh_too_soon，实际 %v", body["code"])
	}

	// 推进 61 秒后应恢复可用。
	clock.Advance(61 * time.Second)
	if code, _ := doJSON(t, engine, http.MethodDelete, "/school/cache/course", ""); code != http.StatusOK {
		t.Fatalf("窗口过后失效应恢复可用，实际 %d", code)
	}
}

// TestCache_InvalidateDoesNotCascade 验证 #34：只删本类型，不级联。
func TestCache_InvalidateDoesNotCascade(t *testing.T) {
	engine, _, _, _ := newCacheTestServer(t, "user-1")

	if code, _ := doJSON(t, engine, http.MethodPost, "/school/cache/course", validCourseBody); code != http.StatusOK {
		t.Fatalf("课表回传失败")
	}
	scoreBody := `{"schemaVersion":1,"payload":[{"courseName":"高等数学","score":"90"}]}`
	if code, _ := doJSON(t, engine, http.MethodPost, "/school/cache/score", scoreBody); code != http.StatusOK {
		t.Fatalf("成绩回传失败")
	}

	// 失效课表。
	if code, _ := doJSON(t, engine, http.MethodDelete, "/school/cache/course", ""); code != http.StatusOK {
		t.Fatalf("失效课表失败")
	}

	// 成绩必须还在。
	_, body := doJSON(t, engine, http.MethodGet, "/school/cache/score", "")
	if body["code"] == "cache_miss" {
		t.Fatal("失效课表级联删掉了成绩缓存（#34 明确不级联）")
	}
}

// TestCache_IsolatedByUser 验证键维度含 userID：一个用户的数据不能被另一个读到。
func TestCache_IsolatedByUser(t *testing.T) {
	clock := newFakeClock()
	cache := newMemCache(func() time.Time { return clock.Now() })
	validator, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("编译 schema 失败: %v", err)
	}

	build := func(userID string) *gin.Engine {
		svc := NewWithAuth(platform.NewMockHTTPDoer(platform.DoerPolicy{}), platform.NewMockTicketStore(),
			platform.NewMockCipher("k"), clock, nil)
		svc.BindCache(cache)
		svc.BindSchemaValidator(validator)
		e := gin.New()
		e.Use(func(c *gin.Context) { c.Set(school.CtxUserID, userID); c.Next() })
		RegisterCache(e, svc)
		return e
	}

	alice := build("alice")
	bob := build("bob")

	if code, _ := doJSON(t, alice, http.MethodPost, "/school/cache/course", validCourseBody); code != http.StatusOK {
		t.Fatalf("alice 回传失败")
	}

	// bob 不该读到 alice 的缓存。
	_, body := doJSON(t, bob, http.MethodGet, "/school/cache/course", "")
	if body["code"] != "cache_miss" {
		t.Fatalf("bob 读到了 alice 的缓存（键维度隔离失效）: %v", body)
	}

	// alice 自己读得到。
	_, body = doJSON(t, alice, http.MethodGet, "/school/cache/course", "")
	if body["code"] == "cache_miss" {
		t.Fatal("alice 读不到自己刚写的缓存")
	}
}

// TestCache_PayloadTooLarge 验证 1 MiB 上限（#40）。
func TestCache_PayloadTooLarge(t *testing.T) {
	engine, _, _, _ := newCacheTestServer(t, "user-1")

	// 造一个超过 1 MiB 的体：一个超长 courseName。
	huge := `{"schemaVersion":1,"payload":[{"courseName":"` +
		strings.Repeat("课", maxSubmitBytes/3+1024) + `"}]}`

	code, body := doJSON(t, engine, http.MethodPost, "/school/cache/course", huge)
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限体应回 413，实际 %d body=%v", code, body)
	}
}

// TestCache_UnavailableWhenNotBound 验证未注入缓存时回 503，而不是 500 或崩溃。
func TestCache_UnavailableWhenNotBound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := NewWithAuth(platform.NewMockHTTPDoer(platform.DoerPolicy{}), platform.NewMockTicketStore(),
		platform.NewMockCipher("k"), newFakeClock(), nil)

	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set(school.CtxUserID, "user-1"); c.Next() })
	RegisterCache(engine, svc)

	code, body := doJSON(t, engine, http.MethodGet, "/school/cache/course", "")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("未配置缓存应回 503，实际 %d", code)
	}
	if body["code"] != "cache_unavailable" {
		t.Errorf("错误码应为 cache_unavailable，实际 %v", body["code"])
	}
}

// TestCache_SuccessFieldIsAuditOnly 验证 #41 Q4：
// success 只随信封回显、不参与准入——success=false 的数据只要形状对也必须被接受。
func TestCache_SuccessFieldIsAuditOnly(t *testing.T) {
	engine, _, _, _ := newCacheTestServer(t, "user-1")

	// success=false，但 payload 形状完全合法。
	body := `{"schemaVersion":1,"success":false,"payload":[{"courseName":"某课"}]}`
	code, resp := doJSON(t, engine, http.MethodPost, "/school/cache/course", body)
	if code != http.StatusOK {
		t.Fatalf("形状合法时 success=false 不应影响准入，实际 %d body=%v", code, resp)
	}
	if resp["success"] != false {
		t.Errorf("success 应原样回显为 false，实际 %v", resp["success"])
	}

	// 反过来：success=true 但形状不对，必须被拒——证明准入看形状不看 success。
	bad := `{"schemaVersion":1,"success":true,"payload":[{"weeks":"1-16"}]}`
	code, resp = doJSON(t, engine, http.MethodPost, "/school/cache/course", bad)
	if code != http.StatusBadRequest {
		t.Fatalf("success=true 但形状不对时必须拒收，实际 %d body=%v", code, resp)
	}
}

// ── Spec 验收门槛补测 ─────────────────────────────────────────────────────

// recordingAuditStore 记录所有审计条目，用来断言 Action/Result 的口径。
type recordingAuditStore struct {
	mu      sync.Mutex
	entries []platform.AuditEntry
}

func (r *recordingAuditStore) Append(_ context.Context, e platform.AuditEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, e)
	return nil
}
func (r *recordingAuditStore) Flush(context.Context) error { return nil }
func (r *recordingAuditStore) Dropped() int64              { return 0 }
func (r *recordingAuditStore) Close() error                { return nil }

func (r *recordingAuditStore) results(action string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.entries {
		if e.Action == action {
			out = append(out, e.Result)
		}
	}
	return out
}

// TestCache_AuditResults 验证审计口径（#40 定 Action=cache_submit 三种 Result、
// #34 定 Action=cache_invalidate）。
//
// 审计是这条链路唯一的可观测面——后端看不到前端直连教务那一段（ADR-0001:43），
// 所以「提交成功/被拒/限流」这三种结局必须从这里读得出来。
func TestCache_AuditResults(t *testing.T) {
	gin.SetMode(gin.TestMode)
	clock := newFakeClock()
	cache := newMemCache(func() time.Time { return clock.Now() })
	audit := &recordingAuditStore{}

	svc := NewWithAuth(platform.NewMockHTTPDoer(platform.DoerPolicy{}), platform.NewMockTicketStore(),
		platform.NewMockCipher("k"), clock, nil)
	validator, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("编译 schema 失败: %v", err)
	}
	svc.BindCache(cache)
	svc.BindSchemaValidator(validator)
	svc.BindAuditStore(audit)

	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set(school.CtxUserID, "user-audit"); c.Next() })
	RegisterCache(engine, svc)

	// 1) 成功回传 → ok
	if code, _ := doJSON(t, engine, http.MethodPost, "/school/cache/course", validCourseBody); code != http.StatusOK {
		t.Fatalf("回传失败")
	}
	// 2) 坏数据 → schema_rejected
	doJSON(t, engine, http.MethodPost, "/school/cache/course", `{"schemaVersion":1,"payload":[{"weeks":"1"}]}`)
	// 3) 失效成功 → ok
	doJSON(t, engine, http.MethodDelete, "/school/cache/course", "")
	// 4) 立即再失效 → too_soon
	doJSON(t, engine, http.MethodDelete, "/school/cache/course", "")

	submit := audit.results("cache_submit")
	wantSubmit := []string{"ok", "schema_rejected"}
	if len(submit) != len(wantSubmit) {
		t.Fatalf("cache_submit 审计条数 = %d (%v), 期望 %d", len(submit), submit, len(wantSubmit))
	}
	for i := range wantSubmit {
		if submit[i] != wantSubmit[i] {
			t.Errorf("cache_submit[%d] = %q, 期望 %q", i, submit[i], wantSubmit[i])
		}
	}

	inv := audit.results("cache_invalidate")
	wantInv := []string{"ok", "too_soon"}
	if len(inv) != len(wantInv) {
		t.Fatalf("cache_invalidate 审计条数 = %d (%v), 期望 %d", len(inv), inv, len(wantInv))
	}
	for i := range wantInv {
		if inv[i] != wantInv[i] {
			t.Errorf("cache_invalidate[%d] = %q, 期望 %q", i, inv[i], wantInv[i])
		}
	}

	// 审计条目里绝不能出现 payload 内容或凭据（mail.go 的铁律）。
	audit.mu.Lock()
	defer audit.mu.Unlock()
	for _, e := range audit.entries {
		blob := e.RequestID + e.UserID + e.Action + e.Target + e.Result
		if strings.Contains(blob, "Web设计与应用") {
			t.Fatalf("审计条目泄漏了 payload 内容: %+v", e)
		}
	}
}

// TestCache_DegradesToMemory 验证 Spec 验收门槛第 7 条「降级可用」。
//
// 用真实 RedisCache 配 FallbackEnabled + 一个不可达的 Redis 地址：
// 写入应落到进程内存兜底，读取仍能拿到数据。这条不测「Redis 正常」，
// 恰恰测「Redis 挂了还能用」——缓存层是可选依赖，挂了不该让业务失败。
func TestCache_DegradesToMemory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	clock := newFakeClock()

	// 127.0.0.1:1 几乎必然连接被拒，用作「Redis 不可用」的模拟。
	redisCache, err := platform.NewRedisCache(
		platform.RedisOptions{Addr: "127.0.0.1:1", Prefix: "degrade:"},
		platform.CacheOptions{
			DefaultTTL:      time.Hour,
			JitterRatio:     0,
			FallbackEnabled: true,
		},
	)
	if err != nil {
		t.Fatalf("构造 RedisCache 失败: %v", err)
	}
	defer func() { _ = redisCache.Close() }()

	validator, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("编译 schema 失败: %v", err)
	}

	svc := NewWithAuth(platform.NewMockHTTPDoer(platform.DoerPolicy{}), platform.NewMockTicketStore(),
		platform.NewMockCipher("k"), clock, nil)
	svc.BindCache(redisCache)
	svc.BindSchemaValidator(validator)

	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set(school.CtxUserID, "user-degrade"); c.Next() })
	RegisterCache(engine, svc)

	// Redis 不可达，但写入应降级到内存成功。
	code, body := doJSON(t, engine, http.MethodPost, "/school/cache/course", validCourseBody)
	if code != http.StatusOK {
		t.Fatalf("Redis 不可用时回传应降级成功，实际 %d body=%v", code, body)
	}

	// 读取也应能从内存兜底拿到。
	code, body = doJSON(t, engine, http.MethodGet, "/school/cache/course", "")
	if code != http.StatusOK {
		t.Fatalf("Redis 不可用时读取应降级成功，实际 %d", code)
	}
	if body["code"] == "cache_miss" {
		t.Fatal("降级后读不到刚写入的数据（内存兜底未生效）")
	}
	payload, _ := body["payload"].([]any)
	if len(payload) != 1 {
		t.Fatalf("降级读取的 payload 不对: %v", body["payload"])
	}
}
