package schoolauth_test

// #28 后续：需要一个「用 tgt 取任意教务页面」的通用能力。
//
// 为什么需要：学院/身份证/家庭地址/性别/考号这些字段不在换票响应里，
// 只在教务档案页；而具体在哪一页需要逐个探明。有了 FetchPage，
// 探明之后就能直接把解析逻辑接上，不必再为实现细节反复改。
//
// seam：JiaowuSource.FetchPage（公共接口），用 scripted doer 模拟。

import (
	"context"
	"strings"
	"testing"

	"nongyu-backend-plus/internal/school/schoolauth"
)

// 按 tgt → 换票 → 建会话 → 取指定页面 的顺序取回页面内容。
func TestJiaowuSource_FetchPageFollowsTicketAndSession(t *testing.T) {
	d := &scriptedDoer{responses: map[string]*scriptedResp{
		"/api/v1/cas/login": {
			body: encryptForTest(t, "req-fp",
				`{"st":"ST:FP","tgt":"T:FP","user":{"number":"202502204","name":"姚一飞"}}`),
		},
		"/jiaoshi/aspsso/caslogin.asp": {
			status: 302, location: "https://jiaowu.sicau.edu.cn/xuesheng/index.asp",
			headers: map[string]string{"Set-Cookie": "ASPSESSIONIDTEST=abc; path=/"},
		},
		"/xuesheng/index.asp": {
			status: 200, body: "<html>HOME</html>",
		},
		"/xuesheng/dangan/grxx.asp": {
			status: 200, body: "<html>个人档案</html>",
		},
	}}
	src, err := schoolauth.NewJiaowuSource(schoolauth.JiaowuOptions{Doer: d})
	if err != nil {
		t.Fatalf("NewJiaowuSource: %v", err)
	}

	body, err := src.FetchPage(context.Background(),
		schoolauth.Credential{RawCookie: "go-admin-tgt=T:OLD"}, "/xuesheng/dangan/grxx.asp")
	if err != nil {
		t.Fatalf("FetchPage 失败：%v", err)
	}
	if !strings.Contains(string(body), "个人档案") {
		t.Fatalf("应返回目标页面内容，实际 %q", string(body))
	}
	// 目标页请求必须带上建会话时拿到的 ASP cookie
	var sawTarget, carriedCookie bool
	for _, r := range d.requests {
		if strings.Contains(r.rawURL, "/xuesheng/dangan/grxx.asp") {
			sawTarget = true
			if strings.Contains(r.cookie, "ASPSESSIONIDTEST") {
				carriedCookie = true
			}
		}
	}
	if !sawTarget {
		t.Fatal("应请求目标页面")
	}
	if !carriedCookie {
		t.Fatal("目标页请求必须带上 ASP 会话 cookie")
	}
}

// 没有 tgt 时必须失败且不出站。
func TestJiaowuSource_FetchPageRequiresTgt(t *testing.T) {
	d := &scriptedDoer{responses: map[string]*scriptedResp{}}
	src, err := schoolauth.NewJiaowuSource(schoolauth.JiaowuOptions{Doer: d})
	if err != nil {
		t.Fatalf("NewJiaowuSource: %v", err)
	}
	_, err = src.FetchPage(context.Background(), schoolauth.Credential{}, "/x")
	if err == nil {
		t.Fatal("无 tgt 时必须失败")
	}
	if n := len(d.requests); n != 0 {
		t.Fatalf("无凭据时不得出站，实际 %d 次", n)
	}
}

// 页面路径必须来自白名单域，不接受绝对 URL（防 SSRF）。
func TestJiaowuSource_FetchPageRejectsAbsoluteURL(t *testing.T) {
	d := &scriptedDoer{responses: map[string]*scriptedResp{}}
	src, err := schoolauth.NewJiaowuSource(schoolauth.JiaowuOptions{Doer: d})
	if err != nil {
		t.Fatalf("NewJiaowuSource: %v", err)
	}
	for _, bad := range []string{"https://evil.example.com/x", "//evil.example.com/x"} {
		if _, err := src.FetchPage(context.Background(),
			schoolauth.Credential{RawCookie: "go-admin-tgt=T"}, bad); err == nil {
			t.Fatalf("绝对/协议相对 URL 必须被拒绝：%s", bad)
		}
	}
	if n := len(d.requests); n != 0 {
		t.Fatalf("非法路径不得出站，实际 %d 次", n)
	}
}
