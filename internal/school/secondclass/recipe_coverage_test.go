package secondclass

import (
	"net/http"
	"testing"
)

// 本文件是 code-review BLOCKER-1 的防复发不变量守卫。
//
// 背景：读类配方的唯一下发入口是 cache_miss（GET /recipe/:recipeId 只服务写类）。
// 历史缺陷：pageRecipes 只有 A 组 12 条，而 cacheMiss 曾用 `recipe, _ := RecipeFor(dt)`
// 丢弃 ok、还注释「dt 已过白名单，必然命中」，导致 24 个 B 组 dataType 未命中时
// 下发 id/path 全空的配方，客户端拿不到请求地址。现有 miss 用例全是 A 组，故全绿也
// 没抓到。这里从「受控词表」这一唯一事实源出发，锁死两件事：
//   1) 词表里每一个 dataType 都必须取得到字段完整的读配方（防以后加类型忘补配方）；
//   2) 每个 B 组 dataType 的 cache_miss 响应必须含非空 id/path 的完整配方；
//   3) 缺配方时必须 fail-closed（回 internal_error），绝不下发空配方。

// bGroupFromVocabulary 从权威词表派生 B 组 = KnownDataTypes() 去掉 A 组 12 个。
// 用「词表 - A 组」派生而不是写死 24 个名字，是为了以后新增 B 组 dataType 时
// 自动纳入守卫（这正是「防复发」的用意）。
func bGroupFromVocabulary() []DataType {
	aGroup := map[DataType]bool{}
	for _, dt := range allTwelve {
		aGroup[dt] = true
	}
	var b []DataType
	for _, dt := range KnownDataTypes() {
		if !aGroup[dt] {
			b = append(b, dt)
		}
	}
	return b
}

// TestRecipeCoverage_EveryKnownDataTypeHasCompleteRecipe：全词表配方不变量。
// 每个受控 dataType 都必须有一条 id/target/method/path 非空、id 稳定唯一、
// 且带必需设备头的读配方。
func TestRecipeCoverage_EveryKnownDataTypeHasCompleteRecipe(t *testing.T) {
	known := KnownDataTypes()
	if len(known) != 36 {
		t.Fatalf("受控词表应为 36 个 dataType（A 组 12 + B 组 24），实际 %d", len(known))
	}
	seen := map[string]bool{}
	for _, dt := range known {
		r, ok := RecipeFor(dt)
		if !ok {
			t.Errorf("受控词表 %q 取不到读配方（RecipeFor !ok）——新增 dataType 必须同步补读配方", dt)
			continue
		}
		if r.ID == "" {
			t.Errorf("%q 配方 id 为空", dt)
		}
		if r.Target != "secondclass" {
			t.Errorf("%q 配方 target 应为 secondclass，实际 %q", dt, r.Target)
		}
		if r.Method == "" {
			t.Errorf("%q 配方 method 为空", dt)
		}
		if r.Path == "" {
			t.Errorf("%q 配方 path 为空", dt)
		}
		if seen[r.ID] {
			t.Errorf("读配方 id 重复: %q（dataType %q）", r.ID, dt)
		}
		seen[r.ID] = true
		if len(r.Headers) == 0 {
			t.Errorf("%q 配方缺 headers（应为 7 个设备头）", dt)
		}
		for _, h := range r.Headers {
			if h.Name == "" {
				t.Errorf("%q 配方有空 header 名", dt)
			}
		}
	}
}

// TestRecipeCoverage_BGroupCacheMissDeliversCompleteRecipe：B 组读通路的唯一入口是
// cache_miss，必须对每个 B 组 dataType 下发非空 id/path 的完整配方，否则客户端
// 不知道该请求哪个地址。走真实 HTTP handler（外部行为 seam），不直接查表。
func TestRecipeCoverage_BGroupCacheMissDeliversCompleteRecipe(t *testing.T) {
	engine, _, _, _ := newCacheTestServer(t, "u-1")
	b := bGroupFromVocabulary()
	if len(b) != 24 {
		t.Fatalf("B 组应为 24 个 dataType，实际 %d", len(b))
	}
	for _, dt := range b {
		res := doJSON(t, engine, http.MethodGet, cachePath+string(dt), "")
		if res.status != http.StatusOK {
			t.Errorf("%q 未命中应回 200，实际 %d: %s", dt, res.status, res.raw)
			continue
		}
		if res.body["code"] != "cache_miss" {
			t.Errorf("%q 未命中 code 应为 cache_miss，实际 %v", dt, res.body["code"])
			continue
		}
		recipe, ok := res.body["recipe"].(map[string]any)
		if !ok {
			t.Errorf("%q cache_miss 响应缺 recipe 对象: %s", dt, res.raw)
			continue
		}
		id, _ := recipe["id"].(string)
		path, _ := recipe["path"].(string)
		if id == "" {
			t.Errorf("%q cache_miss 下发空配方 id（客户端拿不到配方标识）", dt)
		}
		if path == "" {
			t.Errorf("%q cache_miss 下发空配方 path（客户端拿不到请求地址）", dt)
		}
		if recipe["target"] != "secondclass" {
			t.Errorf("%q cache_miss 配方 target 应为 secondclass，实际 %v", dt, recipe["target"])
		}
	}
}

// TestRecipeCoverage_MissingRecipeFailsClosed：当某个受控 dataType 意外缺失配方时，
// handler 必须 fail-closed 回 500 internal_error，绝不能 200 + cache_miss 空配方。
// 用 delete/restore 临时制造「词表认识它、配方表却没有」的状态（本包无 t.Parallel，
// 串行执行 + defer 复原是安全的）；这直接复现 BLOCKER-1 的故障条件。
func TestRecipeCoverage_MissingRecipeFailsClosed(t *testing.T) {
	engine, _, _, _ := newCacheTestServer(t, "u-1")
	const victim = DataTypeActDetail
	saved, had := pageRecipes[victim]
	delete(pageRecipes, victim)
	defer func() {
		if had {
			pageRecipes[victim] = saved
		}
	}()

	res := doJSON(t, engine, http.MethodGet, cachePath+string(victim), "")
	if res.status != http.StatusInternalServerError {
		t.Errorf("缺配方应回 500，实际 %d: %s", res.status, res.raw)
	}
	if res.body["code"] != "internal_error" {
		t.Errorf("缺配方错误码应为 internal_error，实际 %v", res.body["code"])
	}
	if res.body["recipe"] != nil {
		t.Errorf("缺配方时绝不能下发 recipe 字段（哪怕是空配方）: %s", res.raw)
	}
}
