package secondclass

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/platform/mail"
	"nongyu-backend-plus/internal/school"
)

// newRecipeTestServer 装配「只挂 RegisterRecipe」的 gin 引擎，独立验证写类端点行为。
// 不注入 cache/schema（配方是纯静态数据，Redis 没配也应可用——规格 §4.6 / §8.2）。
// userID 非空时用最薄中间件塞身份；传空串模拟未认证。
func newRecipeTestServer(t *testing.T, userID string, audit mail.AuditStore) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc := New(Options{Audit: audit})
	engine := gin.New()
	if userID != "" {
		engine.Use(func(c *gin.Context) {
			c.Set(school.CtxUserID, userID)
			c.Next()
		})
	}
	RegisterRecipe(engine, svc)
	return engine
}

// tableACodes 是规格 §3.2 表 A 里后端会产生的错误码（未知 recipeId 必须从这里取）。
var tableACodes = map[string]bool{
	"target_denied": true, "unauthorized": true, "session_expired": true,
	"cache_unavailable": true, "rate_limited": true, "invalid_payload": true,
	"unknown_datatype": true, "internal_error": true,
}

// 票面验收：GET 已知 recipeId 返回 200 + 完整 Recipe 对象（直接返回，不包一层信封）。
// 同时证明「未配置 Redis 时配方端点仍可用」。
func TestRecipeEndpoint_KnownIDReturnsFullRecipe(t *testing.T) {
	engine := newRecipeTestServer(t, "student-1", &recordingAuditStore{})
	for _, id := range writeRecipeIDs {
		res := doJSON(t, engine, http.MethodGet, "/school/secondclass/recipe/"+id, "")
		if res.status != http.StatusOK {
			t.Errorf("GET %q 应 200，实际 %d: %s", id, res.status, res.raw)
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(res.raw, &obj); err != nil {
			t.Errorf("GET %q 响应不是对象: %v", id, err)
			continue
		}
		for _, k := range []string{"id", "target", "method", "path", "contentType", "auth", "params", "fields", "headers"} {
			if _, ok := obj[k]; !ok {
				t.Errorf("GET %q 响应缺字段 %q", id, k)
			}
		}
		if obj["id"] != id {
			t.Errorf("GET %q 返回的 id = %v", id, obj["id"])
		}
		if obj["target"] != "secondclass" {
			t.Errorf("GET %q 返回的 target = %v", id, obj["target"])
		}
		// 成功响应不带 code（规格 §3.1）。
		if _, ok := obj["code"]; ok {
			t.Errorf("GET %q 成功响应不应带 code 字段", id)
		}
	}
}

// 票面验收：未知 recipeId 返回 400（不是 404、不是 200 空），错误码取自规格表 A。
func TestRecipeEndpoint_UnknownIDReturns400(t *testing.T) {
	engine := newRecipeTestServer(t, "student-1", &recordingAuditStore{})
	// 只用「单段、无 .. / 无尾斜杠」的未知 id：带 .. 或纯尾斜杠的 URL 会被 gin
	// 的 cleanPath / RedirectTrailingSlash 拦成 301/404，根本到不了 handler，
	// 那属于路由层行为、不是本端点的 400 语义。白名单拒绝由纯函数测试覆盖。
	for _, bogus := range []string{
		"bogus", "secondclass.activity.nope",
		"secondclass.credit.getSecondActivityCredit", "UNKNOWN",
	} {
		res := doJSON(t, engine, http.MethodGet, "/school/secondclass/recipe/"+bogus, "")
		if res.status == http.StatusNotFound {
			t.Errorf("GET %q 不应是 404", bogus)
		}
		if res.status != http.StatusBadRequest {
			t.Errorf("GET %q 应 400，实际 %d: %s", bogus, res.status, res.raw)
			continue
		}
		code, _ := res.body["code"].(string)
		if !tableACodes[code] {
			t.Errorf("GET %q 的错误码 %q 不在规格表 A 中", bogus, code)
		}
		// 不能是空配方（200 + 空对象）那种「假装成功」。
		if _, ok := res.body["recipe"]; ok {
			t.Errorf("GET %q 的 400 不应带 recipe", bogus)
		}
	}
}

// 票面验收：端点需会话；身份只从 school.CtxUserID 取——query/body 里的 userID 一律不认。
func TestRecipeEndpoint_RequiresSessionFromContextOnly(t *testing.T) {
	// 无上下文身份，即使 query 里塞了 userID 也 401。
	engine := newRecipeTestServer(t, "", &recordingAuditStore{})
	res := doJSON(t, engine, http.MethodGet,
		"/school/secondclass/recipe/"+writeRecipeIDs[0]+"?userID=attacker", "")
	if res.status != http.StatusUnauthorized {
		t.Errorf("未认证应 401，实际 %d: %s", res.status, res.raw)
	}
	if code, _ := res.body["code"].(string); code != "unauthorized" {
		t.Errorf("未认证错误码应 unauthorized，实际 %q", code)
	}

	// 有上下文身份就 200（且响应不含任何 query 伪造的身份痕迹）。
	engine2 := newRecipeTestServer(t, "real-student", &recordingAuditStore{})
	res2 := doJSON(t, engine2, http.MethodGet,
		"/school/secondclass/recipe/"+writeRecipeIDs[0]+"?userID=attacker", "")
	if res2.status != http.StatusOK {
		t.Errorf("已认证应 200，实际 %d: %s", res2.status, res2.raw)
	}
	if strings.Contains(string(res2.raw), "attacker") {
		t.Errorf("响应里不应回显 query 伪造的 userID: %s", res2.raw)
	}
}

// 票面验收（脱敏）：响应体不含 token 具体值、GPS 具体值、扫码时间戳具体值。
// client_local 参数只给名字不给值；client-long/lat 头只给名字不给值；
// x-access-token 只以 auth{in,name,from} 形式出现（名称可以，值不可以）。
func TestRecipeEndpoint_Desensitized(t *testing.T) {
	engine := newRecipeTestServer(t, "student-1", &recordingAuditStore{})
	for _, id := range writeRecipeIDs {
		res := doJSON(t, engine, http.MethodGet, "/school/secondclass/recipe/"+id, "")
		if res.status != http.StatusOK {
			continue
		}
		body := string(res.raw)
		// 无任何凭据值形状。
		if strings.Contains(body, "Bearer") {
			t.Errorf("GET %q 响应出现 Bearer 凭据", id)
		}
		var obj map[string]any
		_ = json.Unmarshal(res.raw, &obj)
		auth, _ := obj["auth"].(map[string]any)
		if len(auth) != 3 || auth["name"] != "x-access-token" || auth["from"] != "client" {
			t.Errorf("GET %q auth 形状非法（可能混入 token 值）: %v", id, auth)
		}
		for k := range auth {
			if k != "in" && k != "name" && k != "from" {
				t.Errorf("GET %q auth 出现多余字段 %q（可能是 token 值）", id, k)
			}
		}
		// 参数槽位：每个对象只能有 name/required/origin/note 四键，绝无 value。
		params, _ := obj["params"].([]any)
		for _, p := range params {
			pm, _ := p.(map[string]any)
			for k := range pm {
				switch k {
				case "name", "required", "origin", "note":
				default:
					t.Errorf("GET %q 参数槽位出现非契约字段 %q: %v", id, k, pm)
				}
			}
		}
		// 7 个设备头：client-long / client-lat 序列化后绝不带 value。
		headers, _ := obj["headers"].([]any)
		if len(headers) != 7 {
			t.Errorf("GET %q 应有 7 个设备头，实际 %d", id, len(headers))
		}
		for _, h := range headers {
			hm, _ := h.(map[string]any)
			if hm["name"] == "client-long" || hm["name"] == "client-lat" {
				if _, ok := hm["value"]; ok {
					t.Errorf("GET %q 的 %v 带了 value（GPS 值泄漏）", id, hm)
				}
			}
			if hm["name"] == "x-access-token" {
				t.Errorf("GET %q 的 headers 不应含 x-access-token", id)
			}
		}
		// 已知固定常量（pu-version 的 12.10.0…）是允许出现的；GPS/时间戳那种现场值不允许。
		// 用一个「像毫秒时间戳」的 13 位连续数字做反例探测：配方里没有任何这种值。
		if strings.Contains(body, "1730000000000") {
			t.Errorf("GET %q 响应里出现了伪造的扫码时间戳", id)
		}
	}
}

// 票面验收：配方下发记审计 secondclass.recipe_issue，target 为 recipeId，
// 且审计里不出现任何敏感值（六字段口径，规格 §3.4 / §6.11）。
func TestRecipeEndpoint_AuditsRecipeIssueWithoutSecrets(t *testing.T) {
	audit := &recordingAuditStore{}
	engine := newRecipeTestServer(t, "student-1", audit)
	id := writeRecipeIDs[0]
	res := doJSON(t, engine, http.MethodGet, "/school/secondclass/recipe/"+id, "")
	if res.status != http.StatusOK {
		t.Fatalf("GET 应 200，实际 %d", res.status)
	}
	found := false
	for _, e := range audit.entries {
		if e.Action == "secondclass.recipe_issue" && e.Target == id && e.Result == "ok" {
			found = true
		}
		// 六字段里绝不能混进 token / GPS / 时间戳具体值。
		blob := e.UserID + e.Action + e.Target + e.Result + e.RequestID
		for _, forbidden := range []string{"Bearer", "x-access-token:", "1730000000000"} {
			if strings.Contains(blob, forbidden) {
				t.Errorf("审计字段里出现敏感值 %q: %+v", forbidden, e)
			}
		}
	}
	if !found {
		t.Errorf("未记 secondclass.recipe_issue(target=%q, result=ok) 审计: %+v", id, audit.entries)
	}
	// 未知 id 的 400 不应记「成功下发」审计。
	engine2 := newRecipeTestServer(t, "student-1", audit)
	_ = doJSON(t, engine2, http.MethodGet, "/school/secondclass/recipe/bogus", "")
}
