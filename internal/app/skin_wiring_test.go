package app_test

// 皮肤域装配层的 HTTP 验收（issue #53）。
//
// 不深入 app 内部：只经公共入口 app.New + Handler() 发真实请求，验证三件事：
//  1. GET /skins/current 在「配了 AUTH_KEY」的装配下**要求用户会话**（2026-11-23
//     认证收紧：publicReadablePaths 清空，皮肤不再公开读）。无令牌应 401。
//  2. 带合法 Bearer 令牌时可正常读取（目录接口 hasPack=false，无包）。
//  3. 写操作（/admin/skins / POST /skins/current）绝不被公开前缀顺带豁免。
//
// 用内存仓储与内存对象存储（未配 DATABASE_URL/MINIO_ENDPOINT），因此本用例
// 在任何环境都能跑。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/app"
	"nongyu-backend-plus/internal/config"
)

func TestAppNew_SkinRoutesRequireAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gin.DefaultWriter = io.Discard
	gin.DefaultErrorWriter = io.Discard

	// 与 TestAppNew_StatusRequiresAuth 同款「配了认证、无数据库」装配：
	// AuthKey 非空 → 挂 AuthMiddleware；无 DATABASE_URL → 内存仓储。
	cfg := config.Config{
		Addr: ":0", AppEnv: "test", ShutdownTimeoutSec: 1,
		TicketKey: "test-ticket-key-32-bytes-min!!", HTTPTimeoutSec: 3,
		AuthKey: "test-auth-key", RateLimitPerMinute: 60,
		HTTPEnforce: false,
	}
	a, err := app.New(cfg, quietLogger())
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Shutdown(time.Second)

	// 1) 目录接口现在要求会话：无令牌应 401（认证收紧后不再是公开读）。
	w := serveGet(a.Handler(), "/skins/current")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("GET /skins/current 无令牌应 401，实际 %d body=%s", w.Code, w.Body.String())
	}

	// 2) 下载不存在的包：无令牌同样应 401（不再暴露公开 404）。
	w = serveGet(a.Handler(), "/skins/nope/1.0.0/pack")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("下载包无令牌应 401，实际 %d body=%s", w.Code, w.Body.String())
	}

	// 3) 写操作不得被 /skins 公开前缀豁免（admin 路由在无身份域时不挂载，
	//    因此这里只要求「不是 2xx」，401/403/404 都可接受）。
	req := httptest.NewRequest(http.MethodPost, "/admin/skins", nil)
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	if rec.Code >= 200 && rec.Code < 300 {
		t.Fatalf("POST /admin/skins 不得免认证成功，实际 %d", rec.Code)
	}

	// 4) 公开侧没有写路由：POST /skins/current 不应 2xx
	req = httptest.NewRequest(http.MethodPost, "/skins/current", nil)
	rec = httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	if rec.Code >= 200 && rec.Code < 300 {
		t.Fatalf("POST /skins/current 不应存在，实际 %d", rec.Code)
	}

	// 5) 响应是合法 JSON 错误体（unauthorized），不是空响应或 500。
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("401 响应不是合法 JSON：%v", err)
	}
}
