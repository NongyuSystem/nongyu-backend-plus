package app_test

// #21 M3：dev 下 AUTH_KEY 为空时，必须显式开关才允许「无认证」装配。
// seam 是公共入口 app.New；期望值来自票正文验收判据。
// 不触碰 config.Load()，因此不会打断「无环境变量调用 Load()」的既有测试。

import (
	"io"
	"log/slog"
	"testing"

	"nongyu-backend-plus/internal/app"
	"nongyu-backend-plus/internal/config"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestAppNew_DevWithoutAuthKeyRequiresExplicitInsecureSwitch(t *testing.T) {
	base := config.Config{
		Addr: ":0", AppEnv: "dev", ShutdownTimeoutSec: 10,
		TicketKey: "test-key", HTTPTimeoutSec: 5,
		RateLimitPerMinute: 60,
		HTTPEnforce:        false, // 避免真实出站
	}

	// 未开开关 → 拒绝装配，且错误要说清原因
	_, err := app.New(base, testLogger())
	if err == nil {
		t.Fatal("dev 下 AUTH_KEY 为空且未开 ALLOW_INSECURE_DEV 时，app.New 必须拒绝")
	}
	if !contains(err.Error(), "AUTH_KEY") {
		t.Fatalf("错误信息应提到 AUTH_KEY，实际 %q", err.Error())
	}

	// 显式开开关 → 允许装配（并打 WARN，这里只验不报错）
	withSwitch := base
	withSwitch.AllowInsecureDev = true
	a, err := app.New(withSwitch, testLogger())
	if err != nil {
		t.Fatalf("显式开 ALLOW_INSECURE_DEV 后应允许装配，实际 %v", err)
	}
	_ = a
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
