package schoolauth_test

// tgt 形态校验的回归测试。
//
// 背景（2026-11-13 实测）：Redis 里保管的「tgt」其实是一次 302 重定向的 HTML——
// `<a href="https://webvpn.sicau.edu.cn/login">Found</a>.`。它被原样存下来，
// 之后每次换票都返回 500「未登录」，而登录本身是成功的（换票拿到了学号+姓名），
// 于是表现为「能登录但画像全空」，且日志里零线索。
//
// 修法：TGTManager.Save 前校验形态，拒绝明显不是 tgt 的值，并给出可诊断的错误。

import (
	"context"
	"strings"
	"testing"
	"time"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school/schoolauth"
)

func TestTGTManager_SaveRejectsNonTGT(t *testing.T) {
	cases := []struct {
		name string
		tgt  string
		ok   bool
	}{
		// 合法形态的**伪造样例**（不是任何真实 tgt；真实凭据一律不进仓库）
		{"合法 tgt", "201690:TGT:FAKE-not-a-real-ticket-0000000000000000=", true},
		{"合法 tgt（短些）", "201690:TGT:abc123", true},
		// 真实踩到的值：302 重定向的 HTML
		{"重定向 HTML", `<a href="https://webvpn.sicau.edu.cn/login">Found</a>.`, false},
		{"JSON 错误响应", `{"code":500,"msg":"未登录"}`, false},
		{"空串", "", false},
		{"全空白", "   ", false},
		{"太短", "t", false},
		{"无冒号", "abcdefghij", false},
		{"含空格", "201690:TGT:abc 123", false},
		{"含换行", "201690:TGT:abc\n123", false},
		{"超长", "201690:TGT:" + strings.Repeat("a", 600), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestManager(t, newMemTGTStore(), platform.NewMockClock(time.Now()))
			err := m.Save(context.Background(), "u1", schoolauth.TGTBundle{TGT: tc.tgt})
			if tc.ok && err != nil {
				t.Fatalf("应接受，实际被拒: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("应拒绝，实际通过")
			}
		})
	}
}

// 错误信息必须能指向真实原因（出现「重定向 HTML」提示），否则排障还是摸黑。
func TestTGTManager_SaveErrorHintsHTML(t *testing.T) {
	m := newTestManager(t, newMemTGTStore(), platform.NewMockClock(time.Now()))
	err := m.Save(context.Background(), "u1", schoolauth.TGTBundle{
		TGT: `<a href="https://webvpn.sicau.edu.cn/login">Found</a>.`,
	})
	if err == nil {
		t.Fatal("应拒绝")
	}
	if !strings.Contains(err.Error(), "重定向 HTML") {
		t.Fatalf("错误信息应提示重定向 HTML，实际：%v", err)
	}
}

// 合法 tgt 必须能正常保存并读回（校验不能把正常路径也挡掉）。
func TestTGTManager_SaveAcceptsAndRoundTrips(t *testing.T) {
	m := newTestManager(t, newMemTGTStore(), platform.NewMockClock(time.Now()))
	// 用伪造样例，不用任何真实凭据
	tgt := "201690:TGT:FAKE-roundtrip-sample-1111111111111111="
	if err := m.Save(context.Background(), "u1", schoolauth.TGTBundle{TGT: tgt}); err != nil {
		t.Fatalf("合法 tgt 应被接受: %v", err)
	}
	got, _, err := m.Load(context.Background(), "u1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.TGT != tgt {
		t.Fatalf("往返失真: want %q got %q", tgt, got.TGT)
	}
}
