package secondclass

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

const cachePath = "/school/secondclass/cache/"

// 未命中：200 + code=cache_miss（不是 404），且响应体含完整配方（规格 §6.2 / §8.2）。
func TestCache_MissReturns200AndFullRecipe(t *testing.T) {
	engine, _, _, _ := newCacheTestServer(t, "u-1")

	res := doJSON(t, engine, http.MethodGet, cachePath+"secondclassCredit", "")
	if res.status != http.StatusOK {
		t.Fatalf("未命中应回 200，实际 %d: %s", res.status, res.raw)
	}
	if res.body["code"] != "cache_miss" {
		t.Errorf("未命中 code 应为 cache_miss，实际 %v", res.body["code"])
	}
	recipe, ok := res.body["recipe"].(map[string]any)
	if !ok {
		t.Fatalf("未命中响应体缺 recipe 对象: %s", res.raw)
	}
	// 完整配方：七/八字段齐全，且 id/target 与该 dataType 一致。
	for _, k := range []string{"id", "target", "method", "path", "contentType", "auth", "params", "fields", "headers"} {
		if _, ok := recipe[k]; !ok {
			t.Errorf("recipe 缺字段 %q", k)
		}
	}
	if recipe["id"] != "secondclass.credit.getSecondActivityCredit" {
		t.Errorf("配方 id 不匹配: %v", recipe["id"])
	}
	if recipe["target"] != "secondclass" {
		t.Errorf("配方 target 应为 secondclass: %v", recipe["target"])
	}
	// 不含教务侧字段集（探索笔记「不要照抄」第 1 条）。
	for _, k := range []string{"parser", "cookies", "session", "address", "mode"} {
		if _, ok := recipe[k]; ok {
			t.Errorf("recipe 不应含教务侧字段 %q", k)
		}
	}
}

// 命中：payload 原样透出，数组形态不被破坏（规格 §6.2 / §8.2）。
func TestCache_HitPreservesArrayPayload(t *testing.T) {
	engine, _, _, _ := newCacheTestServer(t, "u-1")
	body, content := submitBodyFromFixture(t, "2-学分明细.json")

	res := doJSON(t, engine, http.MethodPost, cachePath+"creditRecordDetail", string(body))
	if res.status != http.StatusOK {
		t.Fatalf("回传应成功，实际 %d: %s", res.status, res.raw)
	}

	got := doJSON(t, engine, http.MethodGet, cachePath+"creditRecordDetail", "")
	if got.status != http.StatusOK {
		t.Fatalf("命中应回 200，实际 %d: %s", got.status, got.raw)
	}
	if got.body["code"] != nil {
		t.Errorf("命中响应不应带 code: %v", got.body["code"])
	}
	if !jsonEqual(t, payloadOf(t, got), content) {
		t.Errorf("数组 payload 未原样透出:\n got=%s\nwant=%s", payloadOf(t, got), content)
	}
	// 明确断言 JSON 顶层是数组（map 接会丢形态）。
	var arr []any
	if err := json.Unmarshal(payloadOf(t, got), &arr); err != nil {
		t.Fatalf("payload 应是数组: %v", err)
	}
	if len(arr) == 0 {
		t.Error("数组 payload 不应为空")
	}
	// 信封字段名沿用教务、不新增。
	for _, k := range []string{"payload", "schemaVersion", "fetchedAt", "stale"} {
		if _, ok := got.body[k]; !ok {
			t.Errorf("命中信封缺字段 %q", k)
		}
	}
	if got.body["schemaVersion"] != float64(1) {
		t.Errorf("schemaVersion 应为 1: %v", got.body["schemaVersion"])
	}
	if got.body["stale"] != false {
		t.Errorf("新鲜命中 stale 应为 false: %v", got.body["stale"])
	}
}

// 单对象形态同样不被破坏；全文端到端：未命中 → 回传 → 命中。
func TestCache_FullRoundTripWithObjectPayload(t *testing.T) {
	engine, _, _, _ := newCacheTestServer(t, "student-A")

	miss := doJSON(t, engine, http.MethodGet, cachePath+"secondclassCredit", "")
	if miss.body["code"] != "cache_miss" {
		t.Fatalf("首次访问应未命中: %s", miss.raw)
	}

	body, content := submitBodyFromFixture(t, "1-学期学分.json")
	sub := doJSON(t, engine, http.MethodPost, cachePath+"secondclassCredit", string(body))
	if sub.status != http.StatusOK {
		t.Fatalf("回传失败: %d %s", sub.status, sub.raw)
	}

	hit := doJSON(t, engine, http.MethodGet, cachePath+"secondclassCredit", "")
	if hit.body["code"] == "cache_miss" {
		t.Fatal("回传后应命中")
	}
	if !jsonEqual(t, payloadOf(t, hit), content) {
		t.Errorf("单对象 payload 未原样透出")
	}
	var obj map[string]any
	if err := json.Unmarshal(payloadOf(t, hit), &obj); err != nil {
		t.Fatalf("payload 应是单对象: %v", err)
	}
	if obj["unitName"] != "二课学时" {
		t.Errorf("payload 内容不符: %v", obj["unitName"])
	}
}

// 超 1 MiB → 400 invalid_payload，且旧数据仍可读（规格 §6.12）。
func TestCache_OverOneMiBReturnsInvalidPayloadAndKeepsOldData(t *testing.T) {
	engine, _, _, _ := newCacheTestServer(t, "u-1")
	good, content := submitBodyFromFixture(t, "1-学期学分.json")
	if r := doJSON(t, engine, http.MethodPost, cachePath+"secondclassCredit", string(good)); r.status != http.StatusOK {
		t.Fatalf("先写入一份好数据失败: %d %s", r.status, r.raw)
	}

	// 构造 >1MiB 的回传体（MaxBytesReader 在读取层截断）。
	big := `{"schemaVersion":1,"payload":{},"note":"` + strings.Repeat("x", (1<<20)+64) + `"}`
	res := doRaw(t, engine, http.MethodPost, cachePath+"secondclassCredit", []byte(big))
	if res.status != http.StatusBadRequest {
		t.Fatalf("超限应回 400，实际 %d: %s", res.status, res.raw)
	}
	if res.body["code"] != "invalid_payload" {
		t.Errorf("超限 code 应为 invalid_payload，实际 %v", res.body["code"])
	}

	// 旧数据仍可读。
	hit := doJSON(t, engine, http.MethodGet, cachePath+"secondclassCredit", "")
	if hit.body["code"] == "cache_miss" {
		t.Fatal("超限写入不应破坏旧数据")
	}
	if !jsonEqual(t, payloadOf(t, hit), content) {
		t.Error("超限后旧 payload 应保持不变")
	}
}

// 坏 schema → 400 invalid_payload，且旧数据仍可读（一个字节都不写）。
func TestCache_BadSchemaReturnsInvalidPayloadAndKeepsOldData(t *testing.T) {
	engine, _, cache, _ := newCacheTestServer(t, "u-1")
	good, _ := submitBodyFromFixture(t, "2-学分明细.json")
	if r := doJSON(t, engine, http.MethodPost, cachePath+"creditRecordDetail", string(good)); r.status != http.StatusOK {
		t.Fatalf("先写入好数据失败: %d %s", r.status, r.raw)
	}
	// 记下写入前的缓存字节，坏数据提交后必须逐字节不变。
	key := cacheDataKey("u-1", DataTypeCreditRecordDetail)
	before, _ := cache.Get(context.Background(), key)

	bad := doJSON(t, engine, http.MethodPost, cachePath+"creditRecordDetail",
		`{"schemaVersion":1,"payload":{"notAnArray":true}}`)
	if bad.status != http.StatusBadRequest {
		t.Fatalf("坏 schema 应回 400，实际 %d: %s", bad.status, bad.raw)
	}
	if bad.body["code"] != "invalid_payload" {
		t.Errorf("坏 schema code 应为 invalid_payload，实际 %v", bad.body["code"])
	}

	after, _ := cache.Get(context.Background(), key)
	if string(before) != string(after) {
		t.Error("坏 schema 一个字节都不应写入缓存")
	}
	hit := doJSON(t, engine, http.MethodGet, cachePath+"creditRecordDetail", "")
	if hit.body["code"] == "cache_miss" {
		t.Fatal("坏 schema 提交后旧数据应仍可读")
	}
}

// DELETE 60 秒节流（规格 §6.12 / §8.2）。
func TestCache_InvalidateThrottledFor60Seconds(t *testing.T) {
	engine, _, _, clock := newCacheTestServer(t, "u-1")

	first := doJSON(t, engine, http.MethodDelete, cachePath+"ranking", "")
	if first.status != http.StatusOK || first.body["invalidated"] != true {
		t.Fatalf("首次失效应成功: %d %s", first.status, first.raw)
	}
	second := doJSON(t, engine, http.MethodDelete, cachePath+"ranking", "")
	if second.status != http.StatusTooManyRequests {
		t.Fatalf("60 秒内重复失效应回 429，实际 %d: %s", second.status, second.raw)
	}
	if second.body["code"] != "rate_limited" {
		t.Errorf("节流 code 应为 rate_limited，实际 %v", second.body["code"])
	}
	if second.header.Get("Retry-After") != "60" {
		t.Errorf("Retry-After 应为 60，实际 %q", second.header.Get("Retry-After"))
	}

	clock.Advance(61 * time.Second)
	third := doJSON(t, engine, http.MethodDelete, cachePath+"ranking", "")
	if third.status != http.StatusOK {
		t.Fatalf("超过 60 秒后应放行，实际 %d: %s", third.status, third.raw)
	}
}

// DELETE 后数据真的没了（失效的可见副作用）。
func TestCache_InvalidateRemovesData(t *testing.T) {
	engine, _, _, _ := newCacheTestServer(t, "u-1")
	body, _ := submitBodyFromFixture(t, "1-学期学分.json")
	_ = doJSON(t, engine, http.MethodPost, cachePath+"secondclassCredit", string(body))

	_ = doJSON(t, engine, http.MethodDelete, cachePath+"secondclassCredit", "")
	hit := doJSON(t, engine, http.MethodGet, cachePath+"secondclassCredit", "")
	if hit.body["code"] != "cache_miss" {
		t.Errorf("失效后应未命中，实际: %s", hit.raw)
	}
}

// 未知 dataType 一律 400，绝不把用户输入拼进缓存键。
func TestCache_UnknownDataTypeReturns400(t *testing.T) {
	engine, _, cache, _ := newCacheTestServer(t, "u-1")
	// 单个 path 段里塞注入形态的字符（含冒号/点号/大写），路由能匹配但必须被白名单挡下。
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		res := doJSON(t, engine, method, cachePath+"user:evil..etcSECOND", "")
		if res.status != http.StatusBadRequest {
			t.Errorf("%s 未知 dataType 应回 400，实际 %d", method, res.status)
		}
		if res.body["code"] != "unknown_datatype" {
			t.Errorf("%s 未知 dataType code 应为 unknown_datatype，实际 %v", method, res.body["code"])
		}
	}
	// 缓存里没有任何由恶意字符串拼出的键。
	cache.mu.Lock()
	n := len(cache.data)
	cache.mu.Unlock()
	if n != 0 {
		t.Errorf("未知 dataType 不应产生任何缓存键，实际 %d 个", n)
	}
}

// 未认证 → 401（身份只从 school.CtxUserID 取）。
func TestCache_RequiresAuthenticatedUser(t *testing.T) {
	engine, _, _, _ := newCacheTestServer(t, "") // 无身份中间件
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		res := doJSON(t, engine, method, cachePath+"secondclassCredit", `{"schemaVersion":1,"payload":{}}`)
		if res.status != http.StatusUnauthorized {
			t.Errorf("%s 未认证应回 401，实际 %d", method, res.status)
		}
		if res.body["code"] != "unauthorized" {
			t.Errorf("%s 未认证 code 应为 unauthorized，实际 %v", method, res.body["code"])
		}
	}
}

// 缓存键按用户隔离：甲的副本不会出现在乙的读取里（规格 §6.9）。
// 两个引擎共享同一个 Service（同一份缓存），仅身份上下文不同。
func TestCache_IsolatedByUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	clock := newFakeClock()
	shared := newMemCache(func() time.Time { return clock.Now() })
	v, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("编译 schema 失败: %v", err)
	}
	svc := New(Options{Cache: shared, Schema: v, Clock: clock})

	engineFor := func(userID string) *gin.Engine {
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set("authenticatedUserID", userID)
			c.Next()
		})
		RegisterCache(r, svc)
		return r
	}
	alice := engineFor("alice")
	bob := engineFor("bob")

	body, _ := submitBodyFromFixture(t, "1-学期学分.json")
	_ = doJSON(t, alice, http.MethodPost, cachePath+"secondclassCredit", string(body))

	bobHit := doJSON(t, bob, http.MethodGet, cachePath+"secondclassCredit", "")
	if bobHit.body["code"] != "cache_miss" {
		t.Errorf("乙不应读到甲的副本: %s", bobHit.raw)
	}

	// 键里含 userID，且是 <userID>:<dataType> 的语义段（platform 再加前缀 cache:）。
	shared.mu.Lock()
	keys := make([]string, 0, len(shared.data))
	for k := range shared.data {
		keys = append(keys, k)
	}
	shared.mu.Unlock()
	found := false
	for _, k := range keys {
		if strings.Contains(k, "alice") && strings.Contains(k, "secondclassCredit") && !strings.Contains(k, "bob") {
			found = true
		}
		if strings.Contains(k, "bob") {
			t.Errorf("乙的读取不应产生任何缓存键: %q", k)
		}
	}
	if !found {
		t.Errorf("缓存键应带 userID=alice，实际键: %v", keys)
	}
}

// 新鲜度窗口过后正常 GET 判未命中；带 fallback=1 才下发 stale=true（两套时间语义）。
func TestCache_StaleOnlyOnFallback(t *testing.T) {
	engine, _, cache, _ := newCacheTestServer(t, "u-1")
	body, _ := submitBodyFromFixture(t, "1-学期学分.json")
	_ = doJSON(t, engine, http.MethodPost, cachePath+"secondclassCredit", string(body))

	// 把 fetchedAt 往前拨 9 小时（超过 8h 新鲜度窗口，但未到 7 天硬上限）。
	cache.forceAge(t, "u-1", DataTypeSecondclassCredit, 9*time.Hour)

	normal := doJSON(t, engine, http.MethodGet, cachePath+"secondclassCredit", "")
	if normal.body["code"] != "cache_miss" {
		t.Errorf("超过新鲜度窗口的正常 GET 应未命中: %s", normal.raw)
	}

	fallback := doJSON(t, engine, http.MethodGet, cachePath+"secondclassCredit?fallback=1", "")
	if fallback.body["code"] == "cache_miss" {
		t.Fatalf("fallback=1 应下发旧数据: %s", fallback.raw)
	}
	if fallback.body["stale"] != true {
		t.Errorf("fallback 命中应标 stale=true: %v", fallback.body["stale"])
	}
}

// stale 硬上限 7 天：超过视为不存在。
func TestCache_StaleHardCapSevenDays(t *testing.T) {
	engine, _, cache, _ := newCacheTestServer(t, "u-1")
	body, _ := submitBodyFromFixture(t, "1-学期学分.json")
	_ = doJSON(t, engine, http.MethodPost, cachePath+"secondclassCredit", string(body))

	cache.forceAge(t, "u-1", DataTypeSecondclassCredit, 8*24*time.Hour)
	res := doJSON(t, engine, http.MethodGet, cachePath+"secondclassCredit?fallback=1", "")
	if res.body["code"] != "cache_miss" {
		t.Errorf("超过 7 天的旧数据即使 fallback 也不应下发: %s", res.raw)
	}
}

// 缓存未绑定时端点回 503（可选依赖降级 gate 的形状）。
func TestCache_UnavailableWhenNotBound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := New(Options{Clock: newFakeClock()}) // 不注入 Cache/Schema
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("authenticatedUserID", "u-1")
		c.Next()
	})
	RegisterCache(router, svc)

	res := doJSON(t, router, http.MethodGet, cachePath+"ranking", "")
	if res.status != http.StatusServiceUnavailable {
		t.Fatalf("缓存未绑定应回 503，实际 %d: %s", res.status, res.raw)
	}
	if res.body["code"] != "cache_unavailable" {
		t.Errorf("code 应为 cache_unavailable，实际 %v", res.body["code"])
	}
}

// 审计双通道：配方下发记 secondclass.recipe_issue、副本写入记 secondclass.record，
// 且 AuditEntry 只含六字段（不含 token/GPS/响应体——规格 §3.4 / §6.11）。
func TestCache_AuditActionsAreSpecNames(t *testing.T) {
	gin.SetMode(gin.TestMode)
	clock := newFakeClock()
	cache := newMemCache(func() time.Time { return clock.Now() })
	v, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("编译 schema 失败: %v", err)
	}
	audit := &recordingAuditStore{}
	svc := New(Options{Cache: cache, Schema: v, Clock: clock, Audit: audit})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("authenticatedUserID", "u-1")
		c.Next()
	})
	RegisterCache(router, svc)

	// 未命中 → 配方下发审计。
	_ = doJSON(t, router, http.MethodGet, cachePath+"secondclassCredit", "")
	// 回传 → 副本写入审计。
	body, _ := submitBodyFromFixture(t, "1-学期学分.json")
	_ = doJSON(t, router, http.MethodPost, cachePath+"secondclassCredit", string(body))

	if got := audit.results("secondclass.recipe_issue"); len(got) != 1 || got[0] != "ok" {
		t.Errorf("配方下发应记一条 secondclass.recipe_issue/ok，实际 %v", got)
	}
	if got := audit.results("secondclass.record"); len(got) != 1 || got[0] != "ok" {
		t.Errorf("副本写入应记一条 secondclass.record/ok，实际 %v", got)
	}
	// 审计条目不含任何被禁字段：序列化后不能出现 token / 响应体内容。
	for _, e := range audit.entries {
		b, _ := json.Marshal(e)
		if strings.Contains(string(b), "x-access-token") || strings.Contains(string(b), "二课学时") {
			t.Errorf("审计条目泄漏了被禁字段: %s", b)
		}
	}
}
