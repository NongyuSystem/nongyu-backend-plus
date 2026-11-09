package app_test

// #21 M6（Spec #18）：观测出口必须反映真实依赖状态，不是硬编码 ok。
// seam = HTTP 端点 /status；期望值来自真实 Ping 结果。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/app"
)

func TestStatusEndpoint_ReportsRealDependencyState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gin.DefaultWriter = io.Discard
	gin.DefaultErrorWriter = io.Discard

	// Postgres 探针故意失败、Redis 探针成功：端点必须如实反映，不能一律 ok。
	probe := app.StatusProbe{
		Postgres: func() bool { return false },
		Redis:    func() bool { return true },
		Breaker:  func() string { return "closed" },
		Storage:  func() string { return "postgres" },
	}
	r := gin.New()
	app.RegisterStatus(r, probe)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/status", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/status 应 200，实际 %d", w.Code)
	}
	var body app.StatusReport
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if body.Postgres != "down" {
		t.Fatalf("Postgres 不可达时应报 down，实际 %q", body.Postgres)
	}
	if body.Redis != "up" {
		t.Fatalf("Redis 可达时应报 up，实际 %q", body.Redis)
	}
	if body.Breaker != "closed" {
		t.Fatalf("熔断器状态应如实上报，实际 %q", body.Breaker)
	}
	if body.Process != "up" {
		t.Fatalf("进程存活应恒为 up，实际 %q", body.Process)
	}
}
