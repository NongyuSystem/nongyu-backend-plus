package schoolauth_test

// FullCookieHeader 的行为：换票必须带整套 cookie，且 tgt 用最新值。

import (
	"strings"
	"testing"

	"nongyu-backend-plus/internal/school/schoolauth"
)

func TestFullCookieHeader_KeepsAllAndReplacesTGT(t *testing.T) {
	cred := schoolauth.Credential{
		RawCookie: "go-admin-tgt=OLD; laravel_session=LS; XSRF-TOKEN=XS",
	}
	got := cred.FullCookieHeader("NEW")
	for _, want := range []string{"go-admin-tgt=NEW", "laravel_session=LS", "XSRF-TOKEN=XS"} {
		if !strings.Contains(got, want) {
			t.Fatalf("应包含 %s，实际 %q", want, got)
		}
	}
	if strings.Contains(got, "go-admin-tgt=OLD") {
		t.Fatalf("旧的 tgt 应被替换，实际 %q", got)
	}
}

func TestFullCookieHeader_AppendsWhenTGTMissing(t *testing.T) {
	cred := schoolauth.Credential{RawCookie: "laravel_session=LS"}
	got := cred.FullCookieHeader("NEW")
	if !strings.Contains(got, "laravel_session=LS") || !strings.Contains(got, "go-admin-tgt=NEW") {
		t.Fatalf("应追加 tgt，实际 %q", got)
	}
}

func TestFullCookieHeader_NoTGTGiven(t *testing.T) {
	cred := schoolauth.Credential{RawCookie: "laravel_session=LS"}
	if got := cred.FullCookieHeader(""); got != "laravel_session=LS" {
		t.Fatalf("不给 tgt 时应原样返回，实际 %q", got)
	}
}

func TestFullCookieHeader_StructuredCookies(t *testing.T) {
	cred := schoolauth.Credential{
		Cookies: []schoolauth.SessionCookie{
			{Name: "laravel_session", Value: "LS"},
		},
	}
	got := cred.FullCookieHeader("T")
	if !strings.Contains(got, "laravel_session=LS") || !strings.Contains(got, "go-admin-tgt=T") {
		t.Fatalf("结构化 cookie 也应保留，实际 %q", got)
	}
}
