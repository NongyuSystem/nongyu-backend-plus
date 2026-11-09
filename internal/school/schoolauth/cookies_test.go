package schoolauth_test

// 换票需要「整套同域 cookie」而不是单个 go-admin-tgt。
//
// 实测（2026-11-09）：同一枚 tgt，裸 HTTP 只带 go-admin-tgt 换票返回
// code=500 未登录；浏览器带全套 cookie 则 200 成功。
// 因此这里锁定：换票请求必须把客户端交来的**全部** cookie 都带上，
// 且允许客户端只给原始 Cookie 头（不要求逐条结构化）。

import (
	"context"
	"strings"
	"testing"

	"nongyu-backend-plus/internal/school/schoolauth"
)

// 换票请求必须带上客户端交来的全部 cookie（不只是 go-admin-tgt）。
func TestJiaowuSource_ExchangeSendsAllCookies(t *testing.T) {
	d := &scriptedDoer{responses: map[string]*scriptedResp{
		"/api/v1/cas/login": {body: encryptForTest(t, "req-allck", `{"st":"ST:FAKE"}`)},
		"/jiaoshi/aspsso/caslogin.asp": {
			status:   302,
			location: "/jiaoshi/bangong/main/index1.asp",
			headers:  map[string]string{"Set-Cookie": "ASPSESSIONIDabc=xyz; Path=/"},
		},
		"/jiaoshi/bangong/main/index1.asp":  {body: `<html>首页</html>`},
		"/xuesheng/dangan/banji/bjiben.asp": {body: academicPageHTML},
	}}
	src, _ := schoolauth.NewJiaowuSource(schoolauth.JiaowuOptions{
		Doer:          d,
		AuthBaseURL:   "https://auth.sicau.edu.cn",
		JiaowuBaseURL: "https://jiaowu.sicau.edu.cn",
	})

	// 客户端交来整套 cookie：tgt + laravel_session + XSRF-TOKEN
	cred := schoolauth.Credential{
		RawCookie: "go-admin-tgt=201690:TGT:abc; laravel_session=ls-value; XSRF-TOKEN=xsrf-value",
	}
	if _, err := src.FetchAcademic(context.Background(), cred); err != nil {
		t.Fatalf("FetchAcademic: %v", err)
	}

	if len(d.requests) == 0 {
		t.Fatal("应有请求")
	}
	exchangeCookie := d.requests[0].cookie
	for _, want := range []string{"go-admin-tgt=", "laravel_session=", "XSRF-TOKEN="} {
		if !strings.Contains(exchangeCookie, want) {
			t.Fatalf("换票请求必须带上 %s，实际 Cookie=%q", want, exchangeCookie)
		}
	}
}

// 结构化 cookie 列表同样要全带上。
func TestJiaowuSource_ExchangeSendsStructuredCookies(t *testing.T) {
	d := &scriptedDoer{responses: map[string]*scriptedResp{
		"/api/v1/cas/login": {body: encryptForTest(t, "req-struct", `{"st":"ST:FAKE"}`)},
		"/jiaoshi/aspsso/caslogin.asp": {
			status:   302,
			location: "/jiaoshi/bangong/main/index1.asp",
			headers:  map[string]string{"Set-Cookie": "ASPSESSIONIDabc=xyz; Path=/"},
		},
		"/jiaoshi/bangong/main/index1.asp":  {body: `<html>首页</html>`},
		"/xuesheng/dangan/banji/bjiben.asp": {body: academicPageHTML},
	}}
	src, _ := schoolauth.NewJiaowuSource(schoolauth.JiaowuOptions{
		Doer:          d,
		AuthBaseURL:   "https://auth.sicau.edu.cn",
		JiaowuBaseURL: "https://jiaowu.sicau.edu.cn",
	})
	cred := schoolauth.Credential{
		Cookies: []schoolauth.SessionCookie{
			{Name: "go-admin-tgt", Value: "T"},
			{Name: "laravel_session", Value: "L"},
		},
	}
	if _, err := src.FetchAcademic(context.Background(), cred); err != nil {
		t.Fatalf("FetchAcademic: %v", err)
	}
	exchangeCookie := d.requests[0].cookie
	if !strings.Contains(exchangeCookie, "laravel_session=L") {
		t.Fatalf("结构化 cookie 也应带上，实际 %q", exchangeCookie)
	}
}

// 没有 go-admin-tgt 但有其它 cookie 时，仍然要报「缺 tgt」——
// 因为换票的入口凭据是 tgt，其它 cookie 只是配套。
func TestJiaowuSource_StillRequiresTGT(t *testing.T) {
	d := &scriptedDoer{responses: map[string]*scriptedResp{}}
	src, _ := schoolauth.NewJiaowuSource(schoolauth.JiaowuOptions{Doer: d})
	_, err := src.FetchAcademic(context.Background(), schoolauth.Credential{
		RawCookie: "laravel_session=x; XSRF-TOKEN=y",
	})
	if err == nil {
		t.Fatal("没有 tgt 时应报错")
	}
	if len(d.requests) != 0 {
		t.Fatalf("缺 tgt 时不应发请求，实际 %d 次", len(d.requests))
	}
}
