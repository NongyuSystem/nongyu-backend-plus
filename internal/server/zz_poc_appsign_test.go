package server_test

// 0-day 回归测试（sec-school 独占写入文件，zz_poc_ 前缀）。
//
// 两个用例最初是「红能力」PoC：断言安全期望、在当前（有漏洞的）代码上
// 确定性 FAIL。修复落地后翻成回归断言 —— 断言漏洞**不再成立**，全绿。
//
//   POC-A  requestid.go：X-Request-Id 完全采信客户端值，且无长度/字符集校验。
//          该值被写进 gin.Context、request.Context 与响应头，业务域用它当
//          「审计流水号」(school.audit → AuditStore.RequestID)。修复后：
//          含 HTAB / 超长的客户端值一律丢弃并自生成，响应头只回规范化值。
//
//   POC-B  appsign.go + revocable_auth.go + status.go：/status 曾被签名豁免
//          与免认证清单双重放行。修复后：/status 不再免签、不再免认证，
//          未签名/未认证请求被拒，拿不到内部基础设施状态。
//
// 不改动任何生产代码，不 import 未导出的实现细节；只用导出的中间件、
// 导出谓词与 httpserver.RegisterStatus 复现真实路径。

import (
	"io"
	"net/http"
	"net/http/httptest"
	"nongyu-backend-plus/internal/app/httpserver"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school"
	"nongyu-backend-plus/internal/server"
)

// ---------------------------------------------------------------
// POC-A 回归：客户端 X-Request-Id 不再原样成为「审计流水号」
// ---------------------------------------------------------------

func TestZZPocSchool_RequestIDMustNotBeTrustedVerbatim_RED(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// HTAB 在 HTTP field-value 里是合法可见字符，能真实到达服务端；
	// 超长 + 制表符正是日志/CSV/审计注入与取证的典型载荷。
	forged := "ATTACKER-TRACE\t" + strings.Repeat("A", 200)

	r := gin.New()
	r.Use(server.RequestIDMiddleware())
	var seenGin, seenCtx string
	r.GET("/probe", func(c *gin.Context) {
		seenGin = c.GetString(school.CtxRequestID)
		if v, ok := c.Request.Context().Value(school.CtxRequestID).(string); ok {
			seenCtx = v
		}
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("X-Request-Id", forged)
	r.ServeHTTP(w, req)

	// 回归断言：服务端应校验/规范化追踪号，绝不把客户端原值
	// 原封不动写进响应头与两处上下文。
	if got := w.Header().Get("X-Request-Id"); got == forged || got == "" {
		t.Fatalf("未修复：响应头回显了伪造值或为空（len=%d）", len(got))
	}
	if seenGin == forged || seenCtx == forged || seenGin == "" || seenCtx == "" {
		t.Fatalf("未修复：业务/审计上下文里的追踪号被污染或缺失：gin=%q ctx=%q", seenGin, seenCtx)
	}
	// 三处必须是同一个、且是规范化后的服务端值。
	norm := w.Header().Get("X-Request-Id")
	if seenGin != norm || seenCtx != norm {
		t.Fatalf("响应头/gin/ctx 三处追踪号不一致：head=%q gin=%q ctx=%q", norm, seenGin, seenCtx)
	}
	// 规范化值必须落在 [A-Za-z0-9._-] 且长度有界（128）。
	if len(norm) > 128 || len(norm) == 0 {
		t.Fatalf("追踪号长度越界：%d", len(norm))
	}
	for i := 0; i < len(norm); i++ {
		c := norm[i]
		ok := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-'
		if !ok {
			t.Fatalf("追踪号含非法字符 %q（第 %d 字节）", c, i)
		}
	}

	// 合法的客户端值仍应被复用（跨服务串联能力不被破坏）。
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req2.Header.Set("X-Request-Id", "upstream-trace_42.v1")
	r.ServeHTTP(w2, req2)
	if got := w2.Header().Get("X-Request-Id"); got != "upstream-trace_42.v1" {
		t.Fatalf("合法客户端追踪号应被复用，实际 %q", got)
	}
}

// ---------------------------------------------------------------
// POC-B 回归：/status 不再免签、不再免认证、不再对匿名调用泄露内部配置
// ---------------------------------------------------------------

func TestZZPocSchool_StatusLeaksInternalStateUnsigned_RED(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gin.DefaultWriter = io.Discard
	clock := platform.NewMockClock(time.Unix(1760000000, 0))

	// 前置断言（随修复翻转）：/status 既不免签名、也不免认证；
	// /healthz 仍是唯一免签免认证的存活探针。
	if server.IsAppSignExempt("/status") {
		t.Fatalf("未修复：/status 仍被 IsAppSignExempt 豁免")
	}
	if !server.IsAppSignExempt("/healthz") {
		t.Fatalf("/healthz 必须继续作为唯一免签探针")
	}
	if server.AllowedWithoutAuth(http.MethodGet, "/status") {
		t.Fatalf("未修复：/status 仍被 AllowedWithoutAuth 放行")
	}
	if !server.IsPublicPath("/healthz") {
		t.Fatalf("/healthz 必须继续免认证")
	}

	r := gin.New()
	// 真实生产中间件链：请求签名 + 认证。两者都应拦住未签名/未认证请求。
	r.Use(server.AppSignMiddleware(
		map[string][]byte{"poc": make([]byte, 32)},
		server.NewMemoryNonceStore(clock), clock, nil))
	r.Use(server.AuthMiddleware(server.NewTokenVerifier("poc-auth-key", clock)))
	// 真实 /status 实现（internal/app/status.go 的导出装配入口）。
	httpserver.RegisterStatus(r, httpserver.StatusProbe{
		Postgres:    func() bool { return true },
		Redis:       func() bool { return true },
		Breaker:     func() string { return "half-open" },
		Storage:     func() string { return "postgres" },
		ObjectStore: func() string { return "minio" },
		Track:       func() (int64, int64) { return 100, 7 },
	})
	// 唯一免签存活探针：只报进程可达，不含内部字段。
	r.GET("/healthz", func(c *gin.Context) { c.Status(http.StatusOK) })

	// 完全不带签名头、不带 Authorization。
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/status", nil))

	// 回归断言：未认证且未签名的探测必须被拒，且绝不能拿到内部实现字段。
	if w.Code == http.StatusOK {
		t.Fatalf("未修复：未签名未认证 GET /status 仍返回 200：%s", w.Body.String())
	}
	body := w.Body.String()
	for _, leaked := range []string{`"objectStore":"minio"`, `"storage":"postgres"`, `"breaker":"half-open"`, `"postgres":"up"`} {
		if strings.Contains(body, leaked) {
			t.Fatalf("未修复：未授权响应仍泄露内部字段 %s：%s", leaked, body)
		}
	}

	// 对照组：/healthz 仍免签免认证可达（运维存活探针不受影响）。
	probe := httptest.NewRecorder()
	r.ServeHTTP(probe, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if probe.Code != http.StatusOK {
		t.Fatalf("/healthz 唯一免签探针应仍 200，实际 %d", probe.Code)
	}
}
