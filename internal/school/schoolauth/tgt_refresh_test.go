package schoolauth_test

// #20 M5 正向：存整套 cookie 时续期成功。
// 与 tgt_cookie_test.go 的「缺 cookie 必失败」成对，构成双向证据。

import (
	"context"
	"testing"
	"time"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school/schoolauth"
)

func TestTGTManager_RefreshWithFullCookieHeaderSucceeds(t *testing.T) {
	// 可控换票响应：带回新的 tgt，且请求里带了整套 cookie。
	d := &scriptedDoer{responses: map[string]*scriptedResp{
		"/api/v1/cas/login": {
			body: encryptForTest(t, "req-tgt-refresh", `{"st":"ST:FAKE","tgt":"201690:TGT:NEW"}`),
		},
	}}
	src, err := schoolauth.NewJiaowuSource(schoolauth.JiaowuOptions{
		Doer:          d,
		AuthBaseURL:   "https://auth.sicau.edu.cn",
		JiaowuBaseURL: "https://jiaowu.sicau.edu.cn",
	})
	if err != nil {
		t.Fatalf("NewJiaowuSource: %v", err)
	}

	store := newMemTGTStore()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	m, err := schoolauth.NewTGTManager(schoolauth.TGTManagerOptions{
		Store:         store,
		Cipher:        platform.NewMockCipher("tgt-key-for-tests-32-bytes!!!!!"),
		Source:        src,
		Clock:         platform.NewMockClock(now),
		TTL:           24 * time.Hour,
		RefreshBefore: 25 * time.Hour, // 强制走续期分支
	})
	if err != nil {
		t.Fatalf("NewTGTManager: %v", err)
	}
	ctx := context.Background()

	// 保管整套 cookie（含 go-admin-tgt 旧值 + 配套 cookie）
	old := "201690:TGT:OLD"
	full := "go-admin-tgt=" + old + "; laravel_session=LS; XSRF-TOKEN=XS"
	if err := m.Save(ctx, "u1", schoolauth.TGTBundle{TGT: old, CookieHeader: full}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := m.EnsureFresh(ctx, "u1", schoolauth.Credential{})
	if err != nil {
		t.Fatalf("存整套 cookie 时续期应成功，实际 %v", err)
	}
	if got != "201690:TGT:NEW" {
		t.Fatalf("应返回新 tgt，实际 %q", got)
	}

	// 换票请求必须带整套 cookie（不只是 tgt）
	if len(d.requests) == 0 {
		t.Fatal("应发出换票请求")
	}
	ck := d.requests[0].cookie
	for _, want := range []string{"go-admin-tgt=", "laravel_session=LS", "XSRF-TOKEN=XS"} {
		if !containsStr(ck, want) {
			t.Fatalf("换票请求应带 %s，实际 Cookie=%q", want, ck)
		}
	}

	// 滚动保存后，新 tgt 与保留的配套 cookie 都在
	stored, _, err := m.Load(ctx, "u1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stored.TGT != "201690:TGT:NEW" {
		t.Fatalf("应滚动保存新 tgt，实际 %q", stored.TGT)
	}
	if !containsStr(stored.CookieHeader, "laravel_session=LS") {
		t.Fatalf("配套 cookie 应保留，实际 %q", stored.CookieHeader)
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
