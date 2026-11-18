package schoolauth_test

// 教务取数链路（换票 → 建会话 → 抓学籍）的测试。
//
// 协议来自知识库《川农 e 认证「一次登录处处可达」靠 go-admin-tgt cookie 免密换票》：
//   1) 客户端交 go-admin-tgt（auth 域 cookie，10 天有效）
//   2) 后端 GET https://auth.sicau.edu.cn/api/v1/cas/login?appid=<教务appid>
//      带 Cookie: go-admin-tgt=... → 响应 JSON 信封，解密得 st
//   3) 回投 https://jiaowu.sicau.edu.cn/jiaoshi/aspsso/caslogin.asp?appid=..&ticket=<st>
//      跟随跳转建立教务会话（种 ASPSESSIONID*）
//   4) 带会话 GET https://jiaowu.sicau.edu.cn/xuesheng/dangan/banji/bjiben.asp
//      响应体按 GBK 解码
//
// 关键约束：
//   - 换票的 appid 决定给哪个应用发票，传错会 INVALID_TICKET
//   - 「不跟随指向真站的 3xx」是安全护栏；建会话的跳转发生在**白名单内的教务域**，
//     因此允许受限跟随，不构成任意 URL 代理（ADR-0001 第 1 条）

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school/schoolauth"
)

// TestIssueSessionCookies_ReturnsASPSESSIONIDAndDoesNotFetchCoursePage
// 只建会话、不抓课表页，且不把 go-admin-tgt 交出去。
func TestIssueSessionCookies_ReturnsASPSESSIONIDAndDoesNotFetchCoursePage(t *testing.T) {
	ticketData := encryptForTest(t, "ticket-req-issue", `{"st":"ST:FAKE-TICKET","tgt":"201690:TGT:new"}`)
	d := &scriptedDoer{responses: map[string]*scriptedResp{
		"/api/v1/cas/login": {body: ticketData},
		"/jiaoshi/aspsso/caslogin.asp": {
			status:   302,
			location: "https://jiaowu.sicau.edu.cn/web/web/web/index.asp",
			headers: map[string]string{
				"Set-Cookie": "ASPSESSIONIDabc=xyz; Path=/",
			},
		},
		"/web/web/web/index.asp": {body: "ok"},
	}}
	src, err := schoolauth.NewJiaowuSource(schoolauth.JiaowuOptions{
		Doer:          d,
		AuthBaseURL:   "https://auth.sicau.edu.cn",
		JiaowuBaseURL: "https://jiaowu.sicau.edu.cn",
	})
	if err != nil {
		t.Fatalf("NewJiaowuSource: %v", err)
	}
	now := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
	mgr, err := schoolauth.NewTGTManager(schoolauth.TGTManagerOptions{
		Store:  newMemTGTStore(),
		Cipher: platform.NewMockCipher("tgt-key-for-tests-32-bytes!!!!!"),
		Source: src,
		Clock:  platform.NewMockClock(now),
	})
	if err != nil {
		t.Fatalf("NewTGTManager: %v", err)
	}
	if err := mgr.Save(context.Background(), "42", schoolauth.TGTBundle{
		TGT:          "201690:TGT:stored",
		CookieHeader: "go-admin-tgt=201690:TGT:stored",
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	src.WithTGT(mgr)

	cookies, err := src.IssueSessionCookies(context.Background(), "42")
	if err != nil {
		t.Fatalf("IssueSessionCookies: %v", err)
	}
	if len(cookies) != 1 || cookies[0].Name != "ASPSESSIONIDabc" || cookies[0].Value != "xyz" {
		t.Fatalf("cookies = %+v", cookies)
	}
	for _, c := range cookies {
		if c.Name == "go-admin-tgt" {
			t.Fatal("不应把 tgt 下发给客户端")
		}
	}
	for _, req := range d.requests {
		if strings.Contains(req.path, "kbbanji.asp") {
			t.Fatalf("建会话不应抓课表页，实际请求了 %s", req.path)
		}
	}
}

// 换票与抓取都要有正确请求形状。
func TestJiaowuSource_ExchangeAndFetch(t *testing.T) {
	// 构造一份「换票成功」的响应：data 是用真实算法加密的密文。
	ticketData := encryptForTest(t, "ticket-req-1", `{"st":"ST:FAKE-TICKET","callbackUrl":"https://jiaowu.sicau.edu.cn/cb"}`)

	d := &scriptedDoer{responses: map[string]*scriptedResp{
		"/api/v1/cas/login": {body: ticketData},
		// 回投：302 跳到教务首页
		"/jiaoshi/aspsso/caslogin.asp": {
			status:   302,
			location: "https://jiaowu.sicau.edu.cn/xuesheng/dangan/banji/bjiben.asp",
			headers:  map[string]string{"Set-Cookie": "ASPSESSIONIDabc=xyz; Path=/"},
		},
		"/xuesheng/dangan/banji/bjiben.asp": {body: academicPageHTML},
	}}

	src, err := schoolauth.NewJiaowuSource(schoolauth.JiaowuOptions{
		Doer:          d,
		AuthBaseURL:   "https://auth.sicau.edu.cn",
		JiaowuBaseURL: "https://jiaowu.sicau.edu.cn",
	})
	if err != nil {
		t.Fatalf("NewJiaowuSource: %v", err)
	}

	acc, err := src.FetchAcademic(context.Background(), schoolauth.Credential{
		RawCookie: "go-admin-tgt=TGT-VALUE",
	})
	if err != nil {
		t.Fatalf("FetchAcademic: %v", err)
	}
	if acc.Major != "数据科学与大数据技术" {
		t.Fatalf("专业不符：%q", acc.Major)
	}
	if acc.ClassName != "大数据202503" {
		t.Fatalf("班级不符：%q", acc.ClassName)
	}
	if acc.Grade != "2025" {
		t.Fatalf("年级不符：%q", acc.Grade)
	}

	// 请求形状检查
	if len(d.requests) < 2 {
		t.Fatalf("至少应有换票与抓取两次请求，实际 %d", len(d.requests))
	}
	// 第一次是换票：GET /api/v1/cas/login?appid=<教务appid>，带 go-admin-tgt
	ex := d.requests[0]
	if ex.path != "/api/v1/cas/login" {
		t.Fatalf("首次请求应为换票接口，实际 %s", ex.path)
	}
	if !strings.Contains(ex.rawURL, "appid="+schoolauth.JiaowuAppID) {
		t.Fatalf("换票必须带教务 appid，实际 %s", ex.rawURL)
	}
	if !strings.Contains(ex.cookie, "go-admin-tgt=") {
		t.Fatalf("换票必须带 go-admin-tgt，实际 %q", ex.cookie)
	}
	// 抓取请求必须带教务会话 cookie
	last := d.requests[len(d.requests)-1]
	if last.path != "/xuesheng/dangan/banji/bjiben.asp" {
		t.Fatalf("末次请求应为学籍页，实际 %s", last.path)
	}
	if !strings.Contains(last.cookie, "ASPSESSIONID") {
		t.Fatalf("抓取必须带教务会话 cookie，实际 %q", last.cookie)
	}
}

// 缺 go-admin-tgt 时直接拒绝，不发请求。
func TestJiaowuSource_RequiresGoAdminTgt(t *testing.T) {
	d := &scriptedDoer{responses: map[string]*scriptedResp{}}
	src, _ := schoolauth.NewJiaowuSource(schoolauth.JiaowuOptions{Doer: d})
	_, err := src.FetchAcademic(context.Background(), schoolauth.Credential{RawCookie: "laravel_session=x"})
	if err == nil {
		t.Fatal("缺 go-admin-tgt 应报错")
	}
	if len(d.requests) != 0 {
		t.Fatalf("缺凭据时不应发请求，实际发了 %d 次", len(d.requests))
	}
}

// 换票业务码非 200 → 失败。
func TestJiaowuSource_TicketExchangeFailed(t *testing.T) {
	d := &scriptedDoer{responses: map[string]*scriptedResp{
		"/api/v1/cas/login": {body: `{"requestId":"r","code":401,"msg":"未登录","data":""}`},
	}}
	src, _ := schoolauth.NewJiaowuSource(schoolauth.JiaowuOptions{Doer: d})
	_, err := src.FetchAcademic(context.Background(), schoolauth.Credential{RawCookie: "go-admin-tgt=x"})
	if err == nil {
		t.Fatal("换票失败应报错")
	}
}

// 真实跳转链是 3 跳：caslogin.asp → check_cas.asp?sign=.. → note.asp → index1.asp
// 必须跟到最终 200 页，不能跳一次就当作成功。
func TestJiaowuSource_FollowsMultiHopToFinalPage(t *testing.T) {
	d := &scriptedDoer{responses: map[string]*scriptedResp{
		"/api/v1/cas/login": {body: encryptForTest(t, "req-3hop", `{"st":"ST:FAKE"}`)},
		"/jiaoshi/aspsso/caslogin.asp": {
			status:   302,
			location: "https://jiaowu.sicau.edu.cn/jiaoshi/bangong/check_cas.asp?sign=abc",
		},
		"/jiaoshi/bangong/check_cas.asp": {
			status:   302,
			location: "/jiaoshi/bangong/main/note.asp",
			// 实测：ASPSESSIONID 在这类中间跳上下发
			headers: map[string]string{"Set-Cookie": "ASPSESSIONIDxyz=abc; Path=/"},
		},
		"/jiaoshi/bangong/main/note.asp": {
			status:   302,
			location: "/jiaoshi/bangong/main/index1.asp",
		},
		"/jiaoshi/bangong/main/index1.asp":  {body: `<html>首页</html>`},
		"/xuesheng/dangan/banji/bjiben.asp": {body: academicPageHTML},
	}}

	src, _ := schoolauth.NewJiaowuSource(schoolauth.JiaowuOptions{
		Doer:          d,
		AuthBaseURL:   "https://auth.sicau.edu.cn",
		JiaowuBaseURL: "https://jiaowu.sicau.edu.cn",
	})
	acc, err := src.FetchAcademic(context.Background(), schoolauth.Credential{RawCookie: "go-admin-tgt=T"})
	if err != nil {
		t.Fatalf("FetchAcademic: %v", err)
	}
	if acc.Major == "" {
		t.Fatal("三跳之后应取到学籍")
	}
	// 应该真的走过 check_cas 与 note.asp
	var sawCheckCas, sawNote, sawIndex bool
	for _, r := range d.requests {
		switch r.path {
		case "/jiaoshi/bangong/check_cas.asp":
			sawCheckCas = true
		case "/jiaoshi/bangong/main/note.asp":
			sawNote = true
		case "/jiaoshi/bangong/main/index1.asp":
			sawIndex = true
		}
	}
	if !sawCheckCas || !sawNote || !sawIndex {
		t.Fatalf("应跟随全部三跳，实际 checkCas=%v note=%v index=%v", sawCheckCas, sawNote, sawIndex)
	}
}

// 相对 Location 也要能跟随（真实链路里 note.asp 用的是相对路径）。
func TestJiaowuSource_FollowsRelativeRedirect(t *testing.T) {
	d := &scriptedDoer{responses: map[string]*scriptedResp{
		"/api/v1/cas/login": {body: encryptForTest(t, "req-rel", `{"st":"ST:FAKE"}`)},
		"/jiaoshi/aspsso/caslogin.asp": {
			status:   302,
			location: "/jiaoshi/bangong/main/index1.asp", // 相对路径
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
	if _, err := src.FetchAcademic(context.Background(), schoolauth.Credential{RawCookie: "go-admin-tgt=T"}); err != nil {
		t.Fatalf("相对跳转应能跟随：%v", err)
	}
}

// 换票响应里**直接带 tgt**，应能取出来（可用于后续换票，避免二次登录）。
func TestJiaowuSource_ExposesTGTFromTicketResponse(t *testing.T) {
	d := &scriptedDoer{responses: map[string]*scriptedResp{
		"/api/v1/cas/login": {
			body: encryptForTest(t, "req-tgt", `{"st":"ST:FAKE","tgt":"201690:TGT:abc123"}`),
		},
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
	raw, err := src.FetchAcademic(context.Background(), schoolauth.Credential{RawCookie: "go-admin-tgt=T"})
	if err != nil {
		t.Fatalf("FetchAcademic: %v", err)
	}
	// 真实换票响应会把新的 tgt 带回，便于续期。
	if raw.RefreshedTGT != "201690:TGT:abc123" {
		t.Fatalf("应带回新 tgt，实际 %q", raw.RefreshedTGT)
	}
}

// 跳转指向**白名单外**的 host 时必须拒绝（防止变成任意 URL 代理）。
func TestJiaowuSource_RejectsOffWhitelistRedirect(t *testing.T) {
	d := &scriptedDoer{responses: map[string]*scriptedResp{
		"/api/v1/cas/login": {body: encryptForTest(t, "ticket-req-2", `{"st":"ST:FAKE"}`)},
		"/jiaoshi/aspsso/caslogin.asp": {
			status:   302,
			location: "https://evil.example.com/steal",
		},
	}}
	src, _ := schoolauth.NewJiaowuSource(schoolauth.JiaowuOptions{Doer: d})
	_, err := src.FetchAcademic(context.Background(), schoolauth.Credential{RawCookie: "go-admin-tgt=x"})
	if err == nil {
		t.Fatal("跳向白名单外必须拒绝")
	}
	if !strings.Contains(err.Error(), "白名单") {
		t.Fatalf("错误信息应说明是白名单问题，实际 %v", err)
	}
}

var _ = http.MethodGet
