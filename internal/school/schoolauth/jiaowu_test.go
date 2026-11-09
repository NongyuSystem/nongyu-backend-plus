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

	"nongyu-backend-plus/internal/school/schoolauth"
)

// scriptedDoer 按 URL 路径返回预设响应，并记录请求。
type scriptedDoer struct {
	// responses 键是路径（不含 host），值为响应。
	responses map[string]*scriptedResp
	// requests 记录收到的请求（顺序）。
	requests []*recordedReq
}

type scriptedResp struct {
	status   int
	body     string
	location string
	headers  map[string]string
}

type recordedReq struct {
	method  string
	host    string
	path    string
	rawURL  string
	cookie  string
	authHdr string
}

func (d *scriptedDoer) Do(_ context.Context, req *http.Request) (*http.Response, error) {
	d.requests = append(d.requests, &recordedReq{
		method:  req.Method,
		host:    req.URL.Hostname(),
		path:    req.URL.Path,
		rawURL:  req.URL.String(),
		cookie:  req.Header.Get("Cookie"),
		authHdr: req.Header.Get("authorization"),
	})
	// 先按完整 URL 找，再按路径找。
	if r, ok := d.responses[req.URL.String()]; ok {
		return buildResp(r), nil
	}
	if r, ok := d.responses[req.URL.Path]; ok {
		return buildResp(r), nil
	}
	return buildResp(&scriptedResp{status: 404, body: "not found"}), nil
}

func buildResp(r *scriptedResp) *http.Response {
	h := http.Header{}
	for k, v := range r.headers {
		h.Set(k, v)
	}
	if r.location != "" {
		h.Set("Location", r.location)
	}
	status := r.status
	if status == 0 {
		status = 200
	}
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       ioNopCloser(r.body),
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
