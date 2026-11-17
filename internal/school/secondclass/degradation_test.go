package secondclass

// #77 验收补完：三态可观测 + 两套时间语义 + 降级 fail-closed。
//
// seam：RegisterCache 挂出的三条 HTTP 端点（GET/POST/DELETE），与 app 装配层
// 的 seam（app.New 的 Handler / 日志）互补。期望值来自规格 §6.10 / §6.12 与
// 票面 #77 的 7 条验收标准，不是从 cache.go 里重算出来的。
//
// ── 关于「要不要给 CacheSnapshot 加 Status 枚举」的评估结论 ──
//
// **已足够，不需加字段。** 现有两个对外信号已经把三态完整编码、运维侧可直接判定：
//
//   ┌────────────────┬──────────────────────┬───────────────────────────┐
//   │ 状态           │ Service.CacheGet     │ HTTP 响应（运维实际看到） │
//   ├────────────────┼──────────────────────┼───────────────────────────┤
//   │ ① 从未回传     │ ErrCacheMiss         │ code=cache_miss + recipe  │
//   │ ② 新鲜         │ (snap, nil), Stale=F │ stale=false + payload     │
//   │ ③ 过期 stale   │ (snap, nil), Stale=T │ stale=true  + payload     │
//   └────────────────┴──────────────────────┴───────────────────────────┘
//
// `(err == ErrCacheMiss, Stale)` 这个二元组在 Service 层、`(code, stale)` 在 HTTP
// 层都是三值互斥的，足以区分「从未回传 / 已过期 / 新鲜」。再加一个 Status 枚举只是
// 把同一份信息换个名字重编码，还多一处与信封字段漂移的风险——票面明确「不要为加
// 字段而加字段」，故本票不改 CacheSnapshot / cacheEnvelope。下面的
// TestCache_ThreeStatesObservable_NoStatusFieldNeeded 把这条结论锁成可执行断言。

import (
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/school"
)

// TestCache_ThreeStatesObservable_NoStatusFieldNeeded 把「三态可区分」做成汇总测试，
// 并据此证明不需要新增 Status 字段（标准 1 的「接线后 GET 路径仍正确返回三态」）。
//
// 一条时间线上依次出现三态：从未回传 → 提交后新鲜 → 越过新鲜度窗口的过期 stale。
// 三态的可观察标识 (code, stale) 必须互不相同的，运维据此即可判定，无需额外字段。
func TestCache_ThreeStatesObservable_NoStatusFieldNeeded(t *testing.T) {
	engine, _, _, clock := newCacheTestServer(t, "u-1")
	dt := "secondclassCredit"

	// 态①「从未回传」：200 + code=cache_miss + 完整配方；不带 stale 字段。
	never := doJSON(t, engine, http.MethodGet, cachePath+dt, "")
	if never.status != http.StatusOK {
		t.Fatalf("未命中应回 200，实际 %d: %s", never.status, never.raw)
	}
	if never.body["code"] != "cache_miss" {
		t.Errorf("从未回传应是 cache_miss，实际 %v", never.body["code"])
	}
	if _, has := never.body["recipe"]; !has {
		t.Errorf("从未回传应捎带完整配方: %s", never.raw)
	}
	if _, has := never.body["stale"]; has {
		t.Errorf("从未回传不该带 stale 字段（它与「新鲜」靠 code 区分）: %s", never.raw)
	}

	// 提交一份真实副本，进入态②。
	body, content := submitBodyFromFixture(t, "1-学期学分.json")
	if sub := doJSON(t, engine, http.MethodPost, cachePath+dt, string(body)); sub.status != http.StatusOK {
		t.Fatalf("回传应成功: %d %s", sub.status, sub.raw)
	}

	// 态②「新鲜」：命中信封，stale=false，payload 原样，不带 code。
	fresh := doJSON(t, engine, http.MethodGet, cachePath+dt, "")
	if fresh.body["code"] != nil {
		t.Errorf("新鲜命中不应带 code: %s", fresh.raw)
	}
	if fresh.body["stale"] != false {
		t.Errorf("新鲜命中 stale 应为 false: %v", fresh.body["stale"])
	}
	if !jsonEqual(t, payloadOf(t, fresh), content) {
		t.Errorf("新鲜命中 payload 应原样透出")
	}

	// 推进时间越过 8h 新鲜度窗口，但仍在 7 天硬上限 / 8 天留存内。
	clock.Advance(9 * time.Hour)

	// 态③「过期 stale」：只有 fallback=1 才下发，stale=true，payload 仍可读。
	stale := doJSON(t, engine, http.MethodGet, cachePath+dt+"?fallback=1", "")
	if stale.body["code"] != nil {
		t.Errorf("过期 stale 下发不应带 code: %s", stale.raw)
	}
	if stale.body["stale"] != true {
		t.Errorf("过期 stale 应标 stale=true: %v", stale.body["stale"])
	}
	if !jsonEqual(t, payloadOf(t, stale), content) {
		t.Errorf("过期 stale 的数据仍应可读且 payload 不变")
	}

	// 三态标识互不相同：仅靠现有 (code, stale) 两字段即可区分。
	stateOf := func(res httpResult) string {
		if res.body["code"] == "cache_miss" {
			return "cache_miss"
		}
		if res.body["stale"] == true {
			return "stale"
		}
		if res.body["stale"] == false {
			return "fresh"
		}
		return "unknown"
	}
	got := map[string]string{
		stateOf(never): "never", stateOf(fresh): "fresh", stateOf(stale): "stale",
	}
	for _, want := range []string{"cache_miss", "fresh", "stale"} {
		if got[want] == "" {
			t.Errorf("三态应各占一个可观察标识，缺 %q；实际映射 %v", want, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("三态标识应恰好 3 个且互不重合，实际 %v", got)
	}
}

// TestCache_TwoTimeSemanticsAreIndependent 验收标准 6：
// 「TTL 到期变 miss」与「新鲜度到期只标 stale」是两套独立语义。
//
// 真 Redis 集成测要外置服务，这里按票面允许的做法用 fakeClock 精确控制时间：
//   - 新鲜度窗口 dt.TTL()（本例 8h）是**读路径判定**：过了只让正常 GET 判 miss，
//     字节仍留在存储里，fallback=1 标 stale=true 继续下发（数据没被删）；
//   - Redis 留存 redisRetention（=8 天）是**存储层淘汰**：到期字节真的没了，
//     连 fallback=1 也回 cache_miss（区别于上面的「只标 stale」）。
//
// 两个子用例用同一套提交动作、只改推进的时间，差异完全来自时间语义本身。
func TestCache_TwoTimeSemanticsAreIndependent(t *testing.T) {
	const dt = "secondclassCredit"
	submit := func(t *testing.T, engine *gin.Engine) {
		t.Helper()
		body, _ := submitBodyFromFixture(t, "1-学期学分.json")
		if r := doJSON(t, engine, http.MethodPost, cachePath+dt, string(body)); r.status != http.StatusOK {
			t.Fatalf("前置：先写入一份好数据失败 %d %s", r.status, r.raw)
		}
	}

	t.Run("新鲜度窗口过期只标stale不删数据", func(t *testing.T) {
		engine, _, _, clock := newCacheTestServer(t, "u-1")
		submit(t, engine)
		// > 8h 新鲜度窗口，< 7d 硬上限，< 8d 留存。
		clock.Advance(9 * time.Hour)

		normal := doJSON(t, engine, http.MethodGet, cachePath+dt, "")
		if normal.body["code"] != "cache_miss" {
			t.Errorf("超过新鲜度窗口的正常 GET 应判 miss: %s", normal.raw)
		}
		fallback := doJSON(t, engine, http.MethodGet, cachePath+dt+"?fallback=1", "")
		if fallback.body["code"] == "cache_miss" {
			t.Fatalf("新鲜度窗口过期**不该删除数据**，fallback 仍应下发: %s", fallback.raw)
		}
		if fallback.body["stale"] != true {
			t.Errorf("过新鲜度窗口的 fallback 应只标 stale=true: %v", fallback.body["stale"])
		}
	})

	t.Run("Redis留存TTL到期数据消失", func(t *testing.T) {
		engine, _, _, clock := newCacheTestServer(t, "u-1")
		submit(t, engine)
		// 推进到超过留存期（redisRetention = 8 天）：存储层把字节淘汰掉。
		clock.Advance(redisRetention + time.Second)

		fallback := doJSON(t, engine, http.MethodGet, cachePath+dt+"?fallback=1", "")
		if fallback.body["code"] != "cache_miss" {
			t.Fatalf("留存 TTL 到期后数据应真的不存在，连 fallback=1 也回 cache_miss: %s", fallback.raw)
		}
		normal := doJSON(t, engine, http.MethodGet, cachePath+dt, "")
		if normal.body["code"] != "cache_miss" {
			t.Errorf("留存到期后正常 GET 也应 cache_miss: %s", normal.raw)
		}
	})
}

// TestCache_AllReadEndpointsReturn503WhenCacheUnbound 验收标准 4 的包内等价物：
// 「读副本三条回 cache_unavailable 而非启动失败」。
//
// 装配层在可选依赖缺失时选择**不挂路由**（app 层测试断言 404）；这里补另一半：
// 路由已注册、但 Service 没绑定 Cache/Schema 时，GET/POST/DELETE 三条都必须
// 稳定回 503 cache_unavailable，且进程照常可用（不是 panic / 不是 5xx 崩）。
// 已有的 TestCache_UnavailableWhenNotBound 只覆盖 GET，本表把三条补全。
func TestCache_AllReadEndpointsReturn503WhenCacheUnbound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := New(Options{Clock: newFakeClock()}) // 不注入 Cache / Schema
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(school.CtxUserID, "u-1")
		c.Next()
	})
	RegisterCache(router, svc)

	for _, spec := range []struct{ method, path string }{
		{http.MethodGet, cachePath + "ranking"},
		{http.MethodPost, cachePath + "ranking"},
		{http.MethodDelete, cachePath + "ranking"},
	} {
		res := doJSON(t, router, spec.method, spec.path, `{"schemaVersion":1,"payload":{}}`)
		if res.status != http.StatusServiceUnavailable {
			t.Errorf("%s 读副本端点在缓存未绑定时应回 503，实际 %d: %s",
				spec.method, res.status, res.raw)
		}
		if res.body["code"] != "cache_unavailable" {
			t.Errorf("%s 的 code 应为 cache_unavailable，实际 %v", spec.method, res.body["code"])
		}
	}
}

// TestCache_SchemaUnboundRejectsSubmitWithoutWriting 验收标准 5 的包内等价物。
//
// app 层标准 5「schema 编译失败 → logger.Error + 不挂记录端点」那一支因内嵌
// schema 是编译期常量而无法用 cfg 注入失败（见 app 层测试里的说明）；这里在包内
// 锁住它的**设计理由**：校验器没就位时，绝不能放未校验的数据进缓存（fail-closed）。
// 即便路由已挂、缓存已绑定，只要 schema 未注入，写入一律被拒且一个字节都不落。
//
// 注：这是生产接线里不可达的防御路径（app 门一旦编译失败会干脆不挂路由），
// 其响应走 writeCacheError 的默认 500 internal_error。本测试只锁「不放行 +
// 不写数据」这条不变量，不锁具体状态码，也不为此改生产代码。
func TestCache_SchemaUnboundRejectsSubmitWithoutWriting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	clock := newFakeClock()
	cache := newMemCache(func() time.Time { return clock.Now() })
	svc := New(Options{Cache: cache, Schema: nil, Clock: clock}) // 缓存有、校验器缺
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(school.CtxUserID, "u-1")
		c.Next()
	})
	RegisterCache(router, svc)

	body, _ := submitBodyFromFixture(t, "1-学期学分.json")
	res := doJSON(t, router, http.MethodPost, cachePath+"secondclassCredit", string(body))
	if res.status >= http.StatusOK && res.status < http.StatusMultipleChoices {
		t.Fatalf("schema 未注入时不得放行写入（会放未校验数据进缓存）: %s", res.raw)
	}

	// 被拒的写入不能留下任何缓存数据：读回仍是 cache_miss。
	got := doJSON(t, router, http.MethodGet, cachePath+"secondclassCredit", "")
	if got.body["code"] != "cache_miss" {
		t.Errorf("被拒的写入不应留下任何缓存数据: %s", got.raw)
	}
}
