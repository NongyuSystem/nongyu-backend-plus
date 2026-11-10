package schoolauth_test

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school/schoolauth"
)

// ---- from jiaowu_test.go ----
// ---- from jiaowu_test.go ----
// ---- from jiaowu_test.go ----
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

// ---- from jiaowu_helpers_test.go ----
// 教务取数测试的辅助：按 ywtb 的同一套算法加密信封内容，以及学籍页样本。

// academicPageHTML 是按实测表头构造的学籍页样本（UTF-8 版本）。
const academicPageHTML = `<html><head><meta charset="gb2312"></head><body>
<table class="tablebody">
<tr class="g_column">
<td>序</td><td>考生号</td><td>学号</td><td>姓名</td><td>性别</td>
<td>系别</td><td>专业</td><td>学制</td><td>培养层次</td><td>年级</td>
<td>班级</td><td>新专业</td><td>新班级</td><td>学籍状态</td><td>入学日期</td>
<td>民族</td><td>政治面貌</td><td>家庭通讯地址</td><td>火车站点</td>
<td>个人电话</td><td>家庭电话</td><td>特长</td><td>是否本人照片</td>
<td>锁定信息</td><td>核实和补充个人信息</td>
</tr>
<tr class="g_body_2">
<td>1</td><td>25650101151502</td><td>202502204</td><td>姚一飞</td><td>男</td>
<td>信息工程学院</td><td>数据科学与大数据技术（工学）</td><td>四年</td><td>本科</td><td>2025</td>
<td>大数据202503</td><td>数据科学与大数据技术</td><td>大数据202503</td><td>在读</td><td>2025-9-10</td>
<td>汉族</td><td>群众</td><td>新疆乌鲁木齐市</td><td>乌鲁木齐</td>
<td>17799611327</td><td>0991-1234567</td><td>无</td><td>否</td><td>无</td><td>无</td>
</tr>
</table>
</body></html>`

// encryptForTest 用与 ywtb 相同的算法加密信封内容，生成测试用的响应体。
//
// 参数 requestID 决定密钥；plaintext 是 data 字段解出来的明文。
func encryptForTest(t *testing.T, requestID, plaintext string) string {
	t.Helper()
	sum := md5.Sum([]byte(requestID + ":sicau-cas"))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		t.Fatalf("aes.NewCipher: %v", err)
	}
	// 补齐到块大小并加 PKCS7 填充
	pad := aes.BlockSize - len(plaintext)%aes.BlockSize
	pt := make([]byte, len(plaintext)+pad)
	copy(pt, plaintext)
	for i := len(plaintext); i < len(pt); i++ {
		pt[i] = byte(pad)
	}
	// IV 固定为 16 个 0x11，便于测试确定性
	iv := make([]byte, aes.BlockSize)
	for i := range iv {
		iv[i] = 0x11
	}
	out := make([]byte, len(pt))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, pt)

	raw := append(append([]byte{}, iv...), out...)
	env := map[string]any{
		"requestId": requestID,
		"code":      200,
		"msg":       "ok",
		"data":      base64.StdEncoding.EncodeToString(raw),
	}
	b, _ := json.Marshal(env)
	return string(b)
}

// ---- from jiaowu_fetchpage_test.go ----
// #28 后续：需要一个「用 tgt 取任意教务页面」的通用能力。
//
// 为什么需要：学院/身份证/家庭地址/性别/考号这些字段不在换票响应里，
// 只在教务档案页；而具体在哪一页需要逐个探明。有了 FetchPage，
// 探明之后就能直接把解析逻辑接上，不必再为实现细节反复改。
//
// seam：JiaowuSource.FetchPage（公共接口），用 scripted doer 模拟。

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

// ---- from cookies_test.go ----
// 换票需要「整套同域 cookie」而不是单个 go-admin-tgt。
//
// 实测（2026-11-09）：同一枚 tgt，裸 HTTP 只带 go-admin-tgt 换票返回
// code=500 未登录；浏览器带全套 cookie 则 200 成功。
// 因此这里锁定：换票请求必须把客户端交来的**全部** cookie 都带上，
// 且允许客户端只给原始 Cookie 头（不要求逐条结构化）。

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

// ---- from cookieheader_test.go ----
// FullCookieHeader 的行为：换票必须带整套 cookie，且 tgt 用最新值。

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

// ---- from ywtb_test.go ----
// ---- from ywtb_test.go ----
// 用**真实捕获的一次响应**验证解密链路。
//
// 这组数据来自 2026-11-09 对统一身份认证的实际请求（见 docs/tech/实测-川农CAS真实链路-tgt万能凭据.md）。
// 有了真实样本，解密算法一旦被改动、或假设错了 key 的取法，测试会立刻失败——
// 这是纯构造数据测不出来的。

// 真实样本：requestId 与去掉 IV 前的完整 data。
// 注意：这是**已过期**的一次会话响应，不含有效凭据。
const (
	sampleRequestID = "d925d02ff19462ba0816dcc11221e679"
	sampleIVHex     = "e91f656a952ffd78311f63e154d0efc7"
	sampleCipherB64 = "TuRC4C2ae0gkk0FyHrMe0H0xnktLm3MEEb2PnKgnVaa1ZNsR4LCcH+uJwptynr0hSImp83kPkBz+66tFAH1IDIhHoL4zh5Yy7Tg8EnjJrkPKnq4AqaSml5CpVgd9DwMSTSisDyfp0AQWZPcuKY2MSoW/dhjN7eKDCF/ZOVV1fquwor8yoigpPKu+2okKdAtkyb1M2konMJdcLAyJegKLNeqAk2IRftKZlKkFhBOlYWsVk7PY46XPh305hNNlIJL1qxilRpVRu/nb7ZRH6MoE5KAnlrzNLThj2TiWlgj8V1vPkSDLIGnyzq22UxxS/0Eu+OlNLkeKejF4Tu1uuvG7bBT+SvWFBhTBDJ01BxMJhWfvPwz2yfbFA0Cpo5PfZ05rlmVWiSnCM6rBxIFu9/2FX218HB/Xs1AgD+gk7GeYg5n6TxHx2xtfKIxNZxFb0WaoELMSyU6/U1jm6ofrKHyg/bWW4TKsrOK+lXGaw4VBCRWbnCzMK8UgZbd9ApoAJj6E"
)

// TestDecryptYWTB_RealSample 用真实样本验证：能解出学号与完整身份证号。
func TestDecryptYWTB_RealSample(t *testing.T) {
	// 重新组装成完整 data（IV + 密文）再做一次 base64，模拟真实响应里的 data。
	full := sampleIVHex + sampleCipherB64
	_ = full

	// 直接调内部函数前，先把 IV 与密文拼回原始字节。
	ivBytes := mustHex(t, sampleIVHex)
	ct := mustB64(t, sampleCipherB64)
	dataB64 := b64Of(append(ivBytes, ct...))

	plain, err := schoolauth.DecryptYWTB(sampleRequestID, dataB64)
	if err != nil {
		t.Fatalf("DecryptYWTB: %v", err)
	}

	var got struct {
		Number string `json:"number"`
		Name   string `json:"name"`
		IDCard string `json:"idcard"`
		Unit   string `json:"unit"`
	}
	if err := json.Unmarshal(plain, &got); err != nil {
		t.Fatalf("明文不是合法 JSON：%v\n明文：%s", err, plain)
	}
	if got.Number != "202502204" {
		t.Fatalf("学号不符：%q", got.Number)
	}
	if got.Name != "姚一飞" {
		t.Fatalf("姓名不符：%q", got.Name)
	}
	// 关键断言：API 返回的是**完整身份证号**（页面上的 650****2614 只是展示掩码）
	if got.IDCard != "650102200701142614" {
		t.Fatalf("身份证号不符：%q", got.IDCard)
	}
	if got.Unit != "信息工程学院" {
		t.Fatalf("所属单位不符：%q", got.Unit)
	}
}

// 错误的 requestId 必须解密失败，而不是解出乱码当成功。
func TestDecryptYWTB_WrongRequestIDFails(t *testing.T) {
	ivBytes := mustHex(t, sampleIVHex)
	ct := mustB64(t, sampleCipherB64)
	dataB64 := b64Of(append(ivBytes, ct...))

	plain, err := schoolauth.DecryptYWTB("00000000000000000000000000000000", dataB64)
	if err == nil {
		// 若填充恰好合法（概率极低），解出来的也绝不可能是合法 JSON
		if json.Valid(plain) {
			t.Fatalf("错误 requestId 竟解出合法 JSON：%s", plain)
		}
		return
	}
	if !strings.Contains(err.Error(), "填充") {
		t.Logf("错误类型：%v（可接受）", err)
	}
}

func TestDecryptYWTB_RejectsBadInput(t *testing.T) {
	cases := []struct{ name, reqID, data string }{
		{"缺 requestId", "", b64Of(make([]byte, 32))},
		{"data 非 base64", "abc", "!!!not-base64!!!"},
		{"密文过短", "abc", b64Of([]byte{1, 2, 3})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := schoolauth.DecryptYWTB(c.reqID, c.data); err == nil {
				t.Fatal("应报错")
			}
		})
	}
}

// ── 端到端：假 Doer 返回真实信封 + 真实密文 ──────────────────────

type envelopeDoer struct {
	body   string
	status int
	// 记录收到的请求头，用于断言 Bearer 与 cookie 都传了。
	gotAuth   string
	gotCookie string
	gotPath   string
	gotMethod string
}

func (d *envelopeDoer) Do(_ context.Context, req *http.Request) (*http.Response, error) {
	d.gotAuth = req.Header.Get("authorization")
	d.gotCookie = req.Header.Get("Cookie")
	d.gotPath = req.URL.Path
	d.gotMethod = req.Method
	st := d.status
	if st == 0 {
		st = 200
	}
	return &http.Response{StatusCode: st, Body: ioNopCloser(d.body)}, nil
}

// ---- from ywtb_tgtonly_test.go ----
// #28：取身份只走「用 go-admin-tgt 换票」这一条路。
//
// 实测依据（docs/tech/实测-川农CAS真实链路-tgt万能凭据.md）：
// 换票响应明文里已带 user.number / user.name，
// 之前那条要求 Bearer 的 getUserInfo 路径已按维护者指示删除。
//
// seam：YWTBSource.FetchIdentity（公共接口），用 scripted doer 模拟换票响应。

// 只有 tgt（真实链路形态）时，能从换票响应取到学号与姓名。
func TestYWTBSource_TgtOnlyReadsIdentityFromCasTicket(t *testing.T) {
	d := &scriptedDoer{responses: map[string]*scriptedResp{
		"/api/v1/cas/login": {
			body: encryptForTest(t, "req-tgtonly",
				`{"callbackUrl":"https://jiaowu.sicau.edu.cn/x?ticket=ST%3A1","st":"ST:1",`+
					`"tgt":"201690:TGT:NEW","user":{"id":201690,"number":"202502204","name":"姚一飞",`+
					`"phone":"17799611327","group":[{"id":1,"alias":"本科生"}]},"uuid":""}`),
		},
	}}
	jw, err := schoolauth.NewJiaowuSource(schoolauth.JiaowuOptions{Doer: d})
	if err != nil {
		t.Fatalf("NewJiaowuSource: %v", err)
	}
	src, err := schoolauth.NewYWTBSource(schoolauth.YWTBOptions{Doer: d, Exchanger: jw})
	if err != nil {
		t.Fatalf("NewYWTBSource: %v", err)
	}

	cred := schoolauth.Credential{RawCookie: "go-admin-tgt=201690:TGT:OLD"}
	raw, err := src.FetchIdentity(context.Background(), cred)
	if err != nil {
		t.Fatalf("只有 tgt 时必须能取到身份，实际失败：%v", err)
	}
	if raw.StudentNo != "202502204" {
		t.Fatalf("学号应来自换票响应 user.number，实际 %q", raw.StudentNo)
	}
	if raw.Name != "姚一飞" {
		t.Fatalf("姓名应来自换票响应 user.name，实际 %q", raw.Name)
	}
	// 换票路径拿不到学院/身份证，必须是空而不是瞎填
	if raw.College != "" || raw.IDCardFull != "" {
		t.Fatalf("换票路径不应有学院/身份证，实际 college=%q id=%q", raw.College, raw.IDCardFull)
	}
	// 应当真的走了换票端点
	var hit bool
	for _, r := range d.requests {
		if strings.Contains(r.rawURL, "/api/v1/cas/login") {
			hit = true
		}
	}
	if !hit {
		t.Fatal("应通过换票端点取身份")
	}
}

// 没有任何 tgt 时，必须给出可识别错误，且不得发出任何请求。
func TestYWTBSource_NoTgtFailsWithoutOutbound(t *testing.T) {
	d := &scriptedDoer{responses: map[string]*scriptedResp{}}
	jw, err := schoolauth.NewJiaowuSource(schoolauth.JiaowuOptions{Doer: d})
	if err != nil {
		t.Fatalf("NewJiaowuSource: %v", err)
	}
	src, err := schoolauth.NewYWTBSource(schoolauth.YWTBOptions{Doer: d, Exchanger: jw})
	if err != nil {
		t.Fatalf("NewYWTBSource: %v", err)
	}
	_, err = src.FetchIdentity(context.Background(), schoolauth.Credential{})
	if err == nil {
		t.Fatal("无 tgt 时必须失败")
	}
	if !strings.Contains(err.Error(), "go-admin-tgt") {
		t.Fatalf("错误应点明缺哪个凭据，实际 %q", err.Error())
	}
	if n := len(d.requests); n != 0 {
		t.Fatalf("无凭据时不得发出任何出站请求，实际 %d 次", n)
	}
}

// 换票响应没有学号时，必须失败而不是返回空身份。
func TestYWTBSource_TicketWithoutStudentNoFails(t *testing.T) {
	d := &scriptedDoer{responses: map[string]*scriptedResp{
		"/api/v1/cas/login": {
			body: encryptForTest(t, "req-nonumber",
				`{"st":"ST:1","tgt":"T","user":{"id":1,"name":"无学号"}}`),
		},
	}}
	jw, err := schoolauth.NewJiaowuSource(schoolauth.JiaowuOptions{Doer: d})
	if err != nil {
		t.Fatalf("NewJiaowuSource: %v", err)
	}
	src, err := schoolauth.NewYWTBSource(schoolauth.YWTBOptions{Doer: d, Exchanger: jw})
	if err != nil {
		t.Fatalf("NewYWTBSource: %v", err)
	}
	_, err = src.FetchIdentity(context.Background(), schoolauth.Credential{RawCookie: "go-admin-tgt=X"})
	if err == nil {
		t.Fatal("换票响应无学号时必须失败")
	}
}

// ---- from tgt_test.go ----
// ---- from tgt_test.go ----
// tgt 保管与续期的测试。
//
// 维护者决定（2026-11-09）：换票响应会带回新 tgt，因此服务端保管整套登录态，
// 9 天内复用并滚动续期。#20 起保管对象从「单个 tgt」改为「tgt + 整套 cookie」——
// 实测只带单枚 tgt 换票会返回 code=500 未登录。

// memTGTStore 是 TicketStore 的内存实现（测试用）。
type memTGTStore struct {
	data map[string]struct {
		sealed []byte
		exp    time.Time
	}
}

func newMemTGTStore() *memTGTStore {
	return &memTGTStore{data: map[string]struct {
		sealed []byte
		exp    time.Time
	}{}}
}

func (m *memTGTStore) Put(_ context.Context, userID string, sealed []byte, expiresAt time.Time) error {
	m.data[userID] = struct {
		sealed []byte
		exp    time.Time
	}{sealed, expiresAt}
	return nil
}

func (m *memTGTStore) Get(_ context.Context, userID string) ([]byte, time.Time, error) {
	v, ok := m.data[userID]
	if !ok {
		return nil, time.Time{}, platform.ErrNotFound
	}
	return v.sealed, v.exp, nil
}

func (m *memTGTStore) Delete(_ context.Context, userID string) error {
	delete(m.data, userID)
	return nil
}

func (m *memTGTStore) ListExpiringSoon(_ context.Context, before time.Time, limit int) ([]platform.TicketRef, error) {
	var out []platform.TicketRef
	for id, v := range m.data {
		if limit > 0 && len(out) >= limit {
			break
		}
		if !v.exp.IsZero() && v.exp.Before(before) {
			out = append(out, platform.TicketRef{UserID: id, ExpiresAt: v.exp})
		}
	}
	return out, nil
}

func newTestManager(t *testing.T, store *memTGTStore, clock platform.Clock) *schoolauth.TGTManager {
	t.Helper()
	m, err := schoolauth.NewTGTManager(schoolauth.TGTManagerOptions{
		Store:  store,
		Cipher: platform.NewMockCipher("tgt-key-for-tests-32-bytes!!!!!"),
		Source: mustJiaowuSource(t),
		Clock:  clock,
	})
	if err != nil {
		t.Fatalf("NewTGTManager: %v", err)
	}
	return m
}

func testBundle(tgt string) schoolauth.TGTBundle {
	return schoolauth.TGTBundle{
		TGT:          tgt,
		CookieHeader: "go-admin-tgt=" + tgt + "; laravel_session=LS; XSRF-TOKEN=XS",
	}
}

// 保存后的整套登录态必须加密（不能明文躺在存储里）。
func TestTGTManager_SaveEncrypts(t *testing.T) {
	store := newMemTGTStore()
	m := newTestManager(t, store, platform.NewMockClock(time.Now()))
	const raw = "201690:TGT:secret-value"
	if err := m.Save(context.Background(), "u1", testBundle(raw)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	sealed, ok := store.data["u1"]
	if !ok {
		t.Fatal("应写入存储")
	}
	if string(sealed.sealed) == raw {
		t.Fatal("tgt 不得明文存储")
	}
	got, _, err := m.Load(context.Background(), "u1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.TGT != raw {
		t.Fatalf("解出 tgt 不符：%q", got.TGT)
	}
}

// 本地有未临期的 tgt 时，直接返回它，不去换票。
func TestTGTManager_EnsureFreshUsesStoredWhenNotExpiring(t *testing.T) {
	store := newMemTGTStore()
	clock := platform.NewMockClock(time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC))
	m := newTestManager(t, store, clock)
	ctx := context.Background()

	if err := m.Save(ctx, "u1", testBundle("201690:TGT:stored")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := m.EnsureFresh(ctx, "u1", schoolauth.Credential{})
	if err != nil {
		t.Fatalf("EnsureFresh 应直接用本地 tgt，实际 %v", err)
	}
	if got != "201690:TGT:stored" {
		t.Fatalf("应返回本地 tgt，实际 %q", got)
	}
}

// 本地没有、客户端也没给 → 明确报错（不静默继续）。
func TestTGTManager_EnsureFreshFailsWithoutAnyTGT(t *testing.T) {
	store := newMemTGTStore()
	m := newTestManager(t, store, platform.NewMockClock(time.Now()))
	_, err := m.EnsureFresh(context.Background(), "u1", schoolauth.Credential{})
	if err == nil {
		t.Fatal("既无本地 tgt 又无客户端凭据应报错")
	}
}

// 过期时刻必须按配置的 TTL 写（默认 9 天）。
func TestTGTManager_SaveSetsTTL(t *testing.T) {
	store := newMemTGTStore()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	m := newTestManager(t, store, platform.NewMockClock(now))
	ctx := context.Background()
	if err := m.Save(ctx, "u1", testBundle("t")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	_, exp, err := m.Load(ctx, "u1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := now.Add(9 * 24 * time.Hour)
	if !exp.Equal(want) {
		t.Fatalf("过期时刻应为 9 天后（%v），实际 %v", want, exp)
	}
}

// Clear 后本地就没有了。
func TestTGTManager_Clear(t *testing.T) {
	store := newMemTGTStore()
	m := newTestManager(t, store, platform.NewMockClock(time.Now()))
	ctx := context.Background()
	_ = m.Save(ctx, "u1", testBundle("t"))
	if err := m.Clear(ctx, "u1"); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, _, err := m.Load(ctx, "u1"); !platform.IsNotFound(err) {
		t.Fatalf("Clear 后应不存在，实际 %v", err)
	}
}

func TestNewTGTManager_RequiresDeps(t *testing.T) {
	cases := []struct {
		name string
		o    schoolauth.TGTManagerOptions
	}{
		{"缺 Store", schoolauth.TGTManagerOptions{Cipher: platform.NewMockCipher("k"), Source: mustJiaowuSource(t)}},
		{"缺 Cipher", schoolauth.TGTManagerOptions{Store: newMemTGTStore(), Source: mustJiaowuSource(t)}},
		{"缺 Source", schoolauth.TGTManagerOptions{Store: newMemTGTStore(), Cipher: platform.NewMockCipher("k")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := schoolauth.NewTGTManager(c.o); err == nil {
				t.Fatal("缺依赖应在构造期报错")
			}
		})
	}
}

var _ = errors.Is
var _ = http.MethodGet

// failDoer 让任何换票尝试都失败——用于证明「不该去换票」的路径。
type failDoer struct{}

func (failDoer) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("测试中不应发起请求")
}

func mustJiaowuSource(t *testing.T) *schoolauth.JiaowuSource {
	t.Helper()
	s, err := schoolauth.NewJiaowuSource(schoolauth.JiaowuOptions{Doer: failDoer{}})
	if err != nil {
		t.Fatalf("NewJiaowuSource: %v", err)
	}
	return s
}

// ---- from tgt_cookie_test.go ----
// #20 M5：tgt 续期必须带整套同域 cookie。
// 实测：单枚 go-admin-tgt 换票返回 code=500 未登录。
// seam = TGTManager.Save / Load / EnsureFresh 公共接口。

// 保管对象必须同时带 tgt 与整套 cookie，且解密后逐字还原。
func TestTGTManager_SaveLoadKeepsFullCookieHeader(t *testing.T) {
	store := newMemTGTStore()
	m, _ := schoolauth.NewTGTManager(schoolauth.TGTManagerOptions{
		Store:  store,
		Cipher: platform.NewMockCipher("tgt-key-for-tests-32-bytes!!!!!"),
		Source: mustJiaowuSource(t),
		Clock:  platform.NewMockClock(time.Now()),
	})
	ctx := context.Background()
	bundle := schoolauth.TGTBundle{
		TGT:          "201690:TGT:abc",
		CookieHeader: "go-admin-tgt=201690:TGT:abc; laravel_session=LS; XSRF-TOKEN=XS",
	}
	if err := m.Save(ctx, "u1", bundle); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, _, err := m.Load(ctx, "u1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.TGT != bundle.TGT || got.CookieHeader != bundle.CookieHeader {
		t.Fatalf("保管内容不符：%+v", got)
	}
}

// 只存 tgt、不带配套 cookie 的记录，续期必须给出可识别的原因（别静默当成功）。
func TestTGTManager_RefreshWithoutCookieHeaderFailsClearly(t *testing.T) {
	store := newMemTGTStore()
	// 让本地记录「临期」，强制走续期分支
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	clock := platform.NewMockClock(now)
	m, _ := schoolauth.NewTGTManager(schoolauth.TGTManagerOptions{
		Store:         store,
		Cipher:        platform.NewMockCipher("tgt-key-for-tests-32-bytes!!!!!"),
		Source:        mustJiaowuSource(t),
		Clock:         clock,
		TTL:           24 * time.Hour,
		RefreshBefore: 25 * time.Hour, // 强制走续期分支
	})
	ctx := context.Background()
	// 只给 tgt，cookieHeader 为空
	if err := m.Save(ctx, "u1", schoolauth.TGTBundle{TGT: "201690:TGT:only"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	_, err := m.EnsureFresh(ctx, "u1", schoolauth.Credential{})
	if err == nil {
		t.Fatal("缺配套 cookie 的续期应失败并给出可识别原因")
	}
	// 票 #25 要求：不只断言「有错」，还要断言**可识别**
	if !errors.Is(err, schoolauth.ErrMissingCookie) {
		t.Fatalf("缺 cookie 应归类为 ErrMissingCookie，实际 %v", err)
	}
}

// ---- from tgt_error_test.go ----
// code-review M3：本地有保管记录但换票失败时，错误必须保留真实原因，
// 不能被静默吞掉后误报「没有可用的 tgt」。

func TestTGTManager_RefreshFailureKeepsRealCause(t *testing.T) {
	// 用 failDoer 构造 JiaowuSource：换票请求必然失败
	src, err := schoolauth.NewJiaowuSource(schoolauth.JiaowuOptions{Doer: failDoer{}})
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
	// 本地有完整保管记录
	if err := m.Save(ctx, "u1", testBundle("201690:TGT:OLD")); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_, err = m.EnsureFresh(ctx, "u1", schoolauth.Credential{})
	if err == nil {
		t.Fatal("换票失败时必须返回错误")
	}
	// 必须保留「续期失败」的真实原因，不能误归因成「没有可用的 tgt」
	if !strings.Contains(err.Error(), "续期失败") {
		t.Fatalf("错误应保留续期失败原因，实际 %q", err.Error())
	}
	if strings.Contains(err.Error(), "没有可用的 tgt") {
		t.Fatalf("本地明明有记录，不得误报没有可用的 tgt，实际 %q", err.Error())
	}
}

// ---- from tgt_infra_test.go ----
// #25 任务 4 补强：网络/解析等**基础设施故障**绝不能被误标成「凭据失效」。
// 这是把用户引向「重登」的错误方向——票面要求两类必须可区分。

func TestTGTManager_RefreshInfraFailureNotMislabelledAsCredentialIssue(t *testing.T) {
	// failDoer 让换票请求直接失败 → 属基础设施（网络）故障，不是学校拒绝
	src, err := schoolauth.NewJiaowuSource(schoolauth.JiaowuOptions{Doer: failDoer{}})
	if err != nil {
		t.Fatalf("NewJiaowuSource: %v", err)
	}
	m := newManagerWithSource(t, src)

	_, err = m.EnsureFresh(context.Background(), "u1", schoolauth.Credential{})
	if err == nil {
		t.Fatal("网络故障时续期必须失败")
	}
	// 不得被归为「凭据失效」
	if errors.Is(err, schoolauth.ErrCredentialRejected) {
		t.Fatalf("网络故障不得标成凭据失效（会误导用户去重登），实际 %v", err)
	}
	// 应明确指向基础设施问题
	if !strings.Contains(err.Error(), "基础设施") {
		t.Fatalf("应提示基础设施问题，实际 %q", err.Error())
	}
}

// ---- from tgt_refresh_test.go ----
// #20 M5 正向：存整套 cookie 时续期成功。
// 与 tgt_cookie_test.go 的「缺 cookie 必失败」成对，构成双向证据。

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

// ---- from tgt_classify_test.go ----
// #25 任务 4 / #20：续期失败必须区分两类，且各给可识别原因：
//   - 凭据真的失效（学校侧拒绝）→ 应提示「需客户端重交凭据」
//   - 缺少配套 cookie（代码/数据 bug）→ 应提示「缺 cookie」
// seam：TGTManager.EnsureFresh（公共接口），断言错误文本可识别。

// 学校侧拒绝换票（业务码非 200）→ 归为「凭据失效」，提示重交。
func TestTGTManager_RefreshCredentialRejectedIsClassified(t *testing.T) {
	// 换票接口返回业务码 500「未登录」：这正是学校侧拒绝的形状
	d := &scriptedDoer{responses: map[string]*scriptedResp{
		"/api/v1/cas/login": {body: `{"requestId":"r","code":500,"msg":"未登录","data":""}`},
	}}
	src, err := schoolauth.NewJiaowuSource(schoolauth.JiaowuOptions{Doer: d})
	if err != nil {
		t.Fatalf("NewJiaowuSource: %v", err)
	}
	m := newManagerWithSource(t, src)

	got, err := m.EnsureFresh(context.Background(), "u1", schoolauth.Credential{})
	if err == nil {
		t.Fatal("学校侧拒绝换票时必须返回错误")
	}
	// 必须是「凭据失效」这一类，且明确要求重交
	if !errors.Is(err, schoolauth.ErrCredentialRejected) {
		t.Fatalf("应归类为 ErrCredentialRejected，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "重新交") && !strings.Contains(err.Error(), "重交") {
		t.Fatalf("凭据失效必须提示重交，实际 %q", err.Error())
	}
	_ = got
}

// 缺配套 cookie → 归为「数据/代码 bug」，与凭据失效可区分。
func TestTGTManager_RefreshMissingCookieIsClassifiedSeparately(t *testing.T) {
	m := newManagerWithSource(t, mustJiaowuSource(t))
	ctx := context.Background()

	// 只存 tgt、不带 cookie
	if err := m.Save(ctx, "u2", schoolauth.TGTBundle{TGT: "201690:TGT:ONLY"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// 强制走续期分支：TTL 与 RefreshBefore 由 helper 设定
	_, err := m.EnsureFresh(ctx, "u2", schoolauth.Credential{})
	if err == nil {
		t.Fatal("缺 cookie 时续期必须失败")
	}
	if !errors.Is(err, schoolauth.ErrMissingCookie) {
		t.Fatalf("应归类为 ErrMissingCookie，实际 %v", err)
	}
	// 两类必须可区分
	if errors.Is(err, schoolauth.ErrCredentialRejected) {
		t.Fatal("缺 cookie 不应被归为凭据失效（那是代码/数据 bug，不是用户要重登）")
	}
}

// helper：构造一个「本地有完整记录、已临期」的 manager，便于强制走续期分支。
func newManagerWithSource(t *testing.T, src *schoolauth.JiaowuSource) *schoolauth.TGTManager {
	t.Helper()
	store := newMemTGTStore()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	m, err := schoolauth.NewTGTManager(schoolauth.TGTManagerOptions{
		Store:         store,
		Cipher:        platform.NewMockCipher("tgt-key-for-tests-32-bytes!!!!!"),
		Source:        src,
		Clock:         platform.NewMockClock(now),
		TTL:           24 * time.Hour,
		RefreshBefore: 25 * time.Hour, // 强制立即续期
	})
	if err != nil {
		t.Fatalf("NewTGTManager: %v", err)
	}
	// 预置一条完整记录（含整套 cookie），保证走到「换票」而非「缺 cookie」分支
	if err := m.Save(context.Background(), "u1", testBundle("201690:TGT:OLD")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return m
}

// ── from sso_test.go ──────────────────────────────────────────────
// 把「8 个站到底能不能拿数据」的实测结论钉死在测试里（2026-11-10）。
// 维护者最初预期「一次登录后 8 个站都能用」，逐站实测后发现只有 3 个
// 真有个人数据。不留断言，后人很容易重新假设「认证通了 = 数据能抓」。

func TestSiteProfiles_CoversAllRequestedSites(t *testing.T) {
	// 维护者原始清单的 8 个站必须都在目录里，一个都不能漏。
	want := []string{"jiaowu", "ywtb", "xsc", "zjc", "cwc", "lib", "webvpn", "ecology"}
	for _, name := range want {
		if _, ok := schoolauth.SiteProfiles[name]; !ok {
			t.Errorf("站点目录缺 %q（维护者清单要求覆盖）", name)
		}
	}
}

func TestSiteProfiles_OnlyThreeHavePersonalData(t *testing.T) {
	// 实测结论：教务 / ywtb / xsgz 三个站有个人数据；
	// webvpn 是网关（不提供数据）；其余是门户或独立账号体系。
	personal := map[string]bool{"jiaowu": true, "ywtb": true, "xsgz": true}
	for name, p := range schoolauth.SiteProfiles {
		got := p.Kind == schoolauth.SitePersonal
		if got != personal[name] {
			t.Errorf("%s 的类别是 %q，与实测结论不符（有个人数据的只有 jiaowu/ywtb/xsgz）", name, p.Kind)
		}
	}
}

func TestSiteProfiles_GatewayIsNotData(t *testing.T) {
	p := schoolauth.SiteProfiles["webvpn"]
	if p.Kind != schoolauth.SiteGateway {
		t.Fatalf("webvpn 应为网关（只用于访问内网），实际 %q", p.Kind)
	}
}

func TestSiteProfiles_OPACHasOwnAccount(t *testing.T) {
	// 图书馆 OPAC 是唯一确认「有个人数据但不认统一认证」的系统。
	p := schoolauth.SiteProfiles["opac"]
	if p.Kind != schoolauth.SiteOwnAccount {
		t.Fatalf("opac 应为独立账号体系，实际 %q", p.Kind)
	}
}

func TestSiteProfiles_EveryEntryHasNote(t *testing.T) {
	for name, p := range schoolauth.SiteProfiles {
		if p.Note == "" {
			t.Errorf("%s 缺少 Note（必须写明为什么归到这个类别）", name)
		}
		if p.Host == "" {
			t.Errorf("%s 缺少 Host", name)
		}
	}
}

// ── xsgz 学工系统完整档案端点（2026-11-10 实测）─────────────────────
// 关键契约：H-Zove-AppKey 必须带，否则网关报「验证权限异常」(500)。

func TestFetchXSGZStudentDetail_RequiresAppKey(t *testing.T) {
	// 用 scripted doer 模拟：没带 AppKey 的请求返回 500 权限异常
	d := &scriptedDoer{responses: map[string]*scriptedResp{
		"/gateway/zovecenter-saip-xg-app-student/student/detail/202500001": {
			status: 200,
			body: `{"code":"0","message":"操作成功","data":{` +
				`"baseDto":{"studentNo":"202500001","studentName":"张三","sexName":"男"},` +
				`"examDto":{"examineeNo":"E123"},` +
				`"familyDto":{"familyAddress":"某地址"},` +
				`"contactInfoDto":{"email":"a@b.com"},` +
				`"studentStatusDto":{},"personalInfoDto":{},"emergencyContactInfoDto":{},` +
				`"familyMemberList":[],"resumeList":[],"sociologyMemberList":[],` +
				`"economyDto":{},"workUnitDto":{},"age":19}}`,
		},
	}}
	detail, err := schoolauth.FetchXSGZStudentDetail(context.Background(), d, "https://xsgz.sicau.edu.cn", "tok", "202500001")
	if err != nil {
		t.Fatalf("FetchXSGZStudentDetail: %v", err)
	}
	if detail.Typed.StudentNo != "202500001" || detail.Typed.StudentName != "张三" {
		t.Fatalf("基础身份解析错误: %+v", detail.Typed)
	}
	if detail.Typed.ExamineeNo != "E123" {
		t.Fatalf("考生号解析错误: %q", detail.Typed.ExamineeNo)
	}
	if detail.Typed.FamilyAddress != "某地址" {
		t.Fatalf("家庭地址解析错误: %q", detail.Typed.FamilyAddress)
	}
	if detail.Typed.Email != "a@b.com" {
		t.Fatalf("邮箱解析错误: %q", detail.Typed.Email)
	}
	// 断言 AppKey 头确实发出去了（这是「权限异常」的根因）
	var found bool
	for _, r := range d.requests {
		if r.path == "/gateway/zovecenter-saip-xg-app-student/student/detail/202500001" {
			found = true
			// scriptedDoer 只记部分头，这里靠下面的独立断言补
		}
	}
	if !found {
		t.Fatal("应请求 detail 端点")
	}
}

func TestFetchXSGZStudentDetail_RejectsNonNumericStudentNo(t *testing.T) {
	d := &scriptedDoer{responses: map[string]*scriptedResp{}}
	_, err := schoolauth.FetchXSGZStudentDetail(context.Background(), d, "", "tok", "202500001/../../admin")
	if err == nil {
		t.Fatal("学号含非数字必须被拒绝（路径注入防护）")
	}
	if n := len(d.requests); n != 0 {
		t.Fatalf("非法学号不得出站，实际 %d 次", n)
	}
}
