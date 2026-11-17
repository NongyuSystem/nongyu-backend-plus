package app_test

// #77 验收：学校二课域在**装配层**的降级接线（internal/app/app.go 的 secondclassSvc 段）。
//
// seam：公共装配入口 app.New(cfg, logger) + 它产出的 http.Handler + 结构化日志。
// 这三层都是外部可观察的，不深入 app 内部函数、不重算装配代码里的判据。
//
// 为什么测在装配层：二课域自己的包内测试（cache_test.go 等）已经把三态/降级/
// TTL 逐条测透，但「RegisterRecipe 无条件挂、RegisterCache 走降级 gate」这件事
// 只有装配层视角能验证——包内测试看不到 app.go 有没有真的把路由挂到引擎上。
//
// 期望值来自票面 #77 的验收标准与 app.go 接线的设计约定，不是从代码里重算出来的：
//   - 无 REDIS_ADDR：进程照常起；写类配方端点仍可用；读副本三条不挂载（404）；
//   - 降级必须显式 Warn，写明「哪些能力未启用、为什么」，并带 cache_configured 字段；
//   - 绝不因可选依赖缺失让整个进程起不来。
//
// 关于「读副本三条回 cache_unavailable」这条票面措辞：app 层接线选择的是
// **降级时干脆不挂路由**（404），而不是挂上去再让 handler 回 503。两者都是
// 「不启动失败」的合法形态；本测试按接线的实际形态（404）验收，并在
// internal/school/secondclass/degradation_test.go 里补「路由已注册但 Service
// 未绑定缓存 → 三条都 503 cache_unavailable」那条包内 seam。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/app"
	"nongyu-backend-plus/internal/config"
	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school/secondclass"
	"nongyu-backend-plus/internal/server"
)

// scWiringAuthKey 是测试专用的会话签名密钥。
// AuthKey 非空才会挂鉴权中间件——这样「带有效令牌的请求」能把
// 「路由未挂载（404）」与「被鉴权拦下（401）」区分开，是本测试的关键前提。
const scWiringAuthKey = "test-secondclass-wiring-auth-key"

// scWiringCfg 是「无 Redis、无 DB」的最小装配配置。
// RedisAddr / DatabaseURL 故意留空：这正是要验收的降级场景。
// HTTPEnforce=false 避免装配期构造真实出站闸口（本测试零网络）。
func scWiringCfg() config.Config {
	return config.Config{
		Addr: ":0", AppEnv: "test", ShutdownTimeoutSec: 1,
		TicketKey: "test-ticket-key-32-bytes-min!!", HTTPTimeoutSec: 3,
		AuthKey: scWiringAuthKey, RateLimitPerMinute: 600,
		HTTPEnforce: false,
	}
}

// scWiringToken 用与装配层同一把密钥、同一个真实时钟签发一枚有效令牌。
// 装配层在 identityStore=nil 时走 AuthMiddleware（自包含令牌），验的正是 tokenClaims。
func scWiringToken(t *testing.T) string {
	t.Helper()
	v := server.NewTokenVerifier(scWiringAuthKey, platform.NewRealClock())
	return v.Issue("sc-wiring-user", time.Now().Add(time.Hour))
}

// scWiringApp 装配一个进程并捕获它的结构化日志。
// app.New 返回 error 即视为「启动失败」——降级场景绝不允许走到那里。
func scWiringApp(t *testing.T, buf *bytes.Buffer) (*app.App, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	gin.DefaultWriter = io.Discard
	gin.DefaultErrorWriter = io.Discard

	logger := slog.New(slog.NewJSONHandler(buf, nil))
	a, err := app.New(scWiringCfg(), logger)
	if err != nil {
		t.Fatalf("未配 Redis 也应能完成装配（进程照常起，绝不启动失败），实际失败: %v\n日志:\n%s", err, buf.String())
	}
	t.Cleanup(func() { _ = a.Shutdown(time.Second) })
	return a, scWiringToken(t)
}

// scWiringRequest 带（可选的）有效令牌发一个请求。
func scWiringRequest(h http.Handler, method, path, token string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	h.ServeHTTP(w, req)
	return w
}

// scWiringLogRecords 把 JSON 日志逐行解析成记录，供断言「降级告警是否说清原因」。
func scWiringLogRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("日志行不是合法 JSON: %q (%v)", line, err)
		}
		out = append(out, rec)
	}
	return out
}

// TestSecondClassWiring_NoRedis_RecipeMountedRecordNotMounted 验收 #77 标准 4：
// 未配 Redis 时进程照常起、写类配方端点无条件可用、读副本三条走降级 gate 不挂载。
//
// 期望值来自票面「配方端点只依赖编译期常量 + 审计，不依赖 Redis/schema」与
// 「RegisterCache 走降级 gate → 不挂路由」两条设计约定。
func TestSecondClassWiring_NoRedis_RecipeMountedRecordNotMounted(t *testing.T) {
	var buf bytes.Buffer
	a, token := scWiringApp(t, &buf)
	h := a.Handler()

	// 进程整体没被降级拖垮：唯一免认证存活探针仍可达。
	probe := scWiringRequest(h, http.MethodGet, "/healthz", "")
	if probe.Code != http.StatusOK {
		t.Fatalf("降级不应影响进程存活，/healthz 应 200，实际 %d: %s", probe.Code, probe.Body.String())
	}

	// 写类配方端点：无条件挂载，带有效身份应拿到一条完整配方（不是 404、不是 401）。
	recipe := scWiringRequest(h, http.MethodGet,
		"/school/secondclass/recipe/secondclass.activity.signInOrOut", token)
	if recipe.Code != http.StatusOK {
		t.Fatalf("写类配方端点应无条件可用（不依赖 Redis），实际 %d: %s", recipe.Code, recipe.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(recipe.Body.Bytes(), &body); err != nil {
		t.Fatalf("配方响应不是 JSON: %v", err)
	}
	if body["code"] != nil {
		t.Errorf("配方命中不应带错误码: %s", recipe.Body.String())
	}
	if body["id"] != "secondclass.activity.signInOrOut" {
		t.Errorf("应下发所请求的写类配方，实际 id=%v", body["id"])
	}
	if body["target"] != "secondclass" {
		t.Errorf("配方 target 应为 secondclass，实际 %v", body["target"])
	}

	// 读副本三条：降级 gate 生效 → 不挂载 → 404。
	// 带有效令牌是必要的：否则 401 会掩盖「未挂载」这件事。
	for _, spec := range []struct{ method, path string }{
		{http.MethodGet, "/school/secondclass/cache/secondclassCredit"},
		{http.MethodPost, "/school/secondclass/cache/secondclassCredit"},
		{http.MethodDelete, "/school/secondclass/cache/secondclassCredit"},
	} {
		res := scWiringRequest(h, spec.method, spec.path, token)
		if res.Code != http.StatusNotFound {
			t.Errorf("无 Redis 时 %s %s 应因降级不挂载（404），实际 %d: %s",
				spec.method, spec.path, res.Code, res.Body.String())
		}
	}
}

// TestSecondClassWiring_NoRedis_DegradationLogExplainsWhy 验收 #77 标准 7：
// 降级必须显式告警、写明「哪些能力未启用、为什么」，绝不静默。
//
// 期望值来自票面「Warn/Error 带 cache_configured 字段」与
// 「日志写明哪些能力未启用、为什么」两条判据。
func TestSecondClassWiring_NoRedis_DegradationLogExplainsWhy(t *testing.T) {
	var buf bytes.Buffer
	_, _ = scWiringApp(t, &buf)

	var found map[string]any
	for _, rec := range scWiringLogRecords(t, &buf) {
		msg, _ := rec["msg"].(string)
		if strings.Contains(msg, "二课记录端点未挂载") {
			found = rec
			break
		}
	}
	if found == nil {
		t.Fatalf("降级必须显式告警（不能静默），实际日志:\n%s", buf.String())
	}
	if fmt.Sprint(found["level"]) != "WARN" {
		t.Errorf("可选依赖缺失应是 WARN 级（进程照常起），实际 %v", found["level"])
	}
	msg, _ := found["msg"].(string)
	// 「哪些能力未启用、为什么」：点名未启用的是记录端点、仍可用的是配方端点，
	// 并写明补齐条件（REDIS_ADDR + schema 全部编译通过）。
	for _, want := range []string{"二课记录端点未挂载", "写类配方端点仍可用", "REDIS_ADDR", "schema"} {
		if !strings.Contains(msg, want) {
			t.Errorf("告警应写明「哪些能力未启用、为什么」（缺 %q）: %s", want, msg)
		}
	}
	// 结构化字段：让运维能按字段筛选，而不是靠 grep 中文文案。
	if v, ok := found["cache_configured"]; !ok || v != false {
		t.Errorf("告警应带 cache_configured=false 说明原因，实际记录: %v", found)
	}
}

// TestSecondClassWiring_SchemaValidatorCompilesAtStartup 说明为什么
// #77 标准 5「schema 编译失败 → logger.Error + 不挂路由」这一分支**无法用可控 cfg
// 在装配层模拟**，只能静态核对 app.go 的接线：
//
// secondclass.NewSchemaValidator() 读的是 //go:embed 进二进制的 schema/*.json，
// 是编译期常量——只要能编译出这个二进制，启动期就必然全部编译成功，运行期
// 不存在「注入一份坏 schema」的入口；而本票文件边界又禁止改 app.go / schema/*.json
// 去抽出一个可注入的失败点。因此该分支在真实装配里不可达，logger.Error 那段是
// 防御性兜底。包内可测的等价 fail-closed 语义（schema 未注入 → 写入被拒、
// 旧数据不受影响、不静默）由 degradation_test.go 覆盖。
func TestSecondClassWiring_SchemaValidatorCompilesAtStartup(t *testing.T) {
	if _, err := secondclass.NewSchemaValidator(); err != nil {
		t.Fatalf("内嵌 schema 应在编译期就全部编译通过（这正是该降级分支不可达的原因）: %v", err)
	}
}
