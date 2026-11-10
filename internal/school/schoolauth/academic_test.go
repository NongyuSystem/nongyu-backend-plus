package schoolauth_test

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"

	"nongyu-backend-plus/internal/school/schoolauth"
)

// ---- from academic_test.go ----
// ---- from academic_test.go ----
// 教务学籍解析的测试。
//
// 解析方法来自知识库《从川农教务 bjiben.asp 抓学籍》（已实测验证）：
//   - 页面是 GBK 而非 UTF-8（用 UTF-8 解码会得到乱码并误判「没抓到」）
//   - 表头在 <tr class="g_column">，值在 <tr class="g_body_2">，不是 thead/tbody
//   - 按索引 zip 成 {中文表头: 值}，再用**关键字包含**匹配映射（不依赖列顺序）
//   - 值里带括注（如「数据科学与大数据技术（工学）」）要先清洗
//   - 登录超时会返回 HTTP 200，必须靠内容判定失效
//
// 测试用**真实结构的页面样本**（表头 25 列，取自实测记录），
// 并专门构造「列顺序被打乱」「含括注」「登录超时」三个坑。

// realPageHTML 是按实测表头构造的页面（值已换成示例学生）。
const realPageHTML = `<html><head><meta charset="gb2312"></head><body>
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
<td>1</td><td>25650101151502</td><td>202500001</td><td>张三</td><td>男</td>
<td>信息工程学院</td><td>数据科学与大数据技术（工学）</td><td>四年</td><td>本科</td><td>2025</td>
<td>大数据202503</td><td>数据科学与大数据技术</td><td>大数据202503</td><td>在读</td><td>2025-9-10</td>
<td>汉族</td><td>群众</td><td>新疆乌鲁木齐市</td><td>乌鲁木齐</td>
<td>13800138000</td><td>0991-1234567</td><td>无</td><td>否</td><td>无</td><td>无</td>
</tr>
</table>
</body></html>`

// 正常解析：专业/班级/年级/学院取到，且括注被清洗。
func TestParseAcademic_RealStructure(t *testing.T) {
	got, err := schoolauth.ParseAcademic([]byte(realPageHTML))
	if err != nil {
		t.Fatalf("ParseAcademic: %v", err)
	}
	if got.Major != "数据科学与大数据技术" {
		t.Fatalf("专业应去掉括注，实际 %q", got.Major)
	}
	if got.ClassName != "大数据202503" {
		t.Fatalf("班级不符：%q", got.ClassName)
	}
	if got.Grade != "2025" {
		t.Fatalf("年级不符：%q", got.Grade)
	}
	if got.College != "信息工程学院" {
		t.Fatalf("学院（系别）不符：%q", got.College)
	}
	if got.StudentNo != "202500001" {
		t.Fatalf("学号不符：%q", got.StudentNo)
	}
	if got.Name != "张三" {
		t.Fatalf("姓名不符：%q", got.Name)
	}
	// 隐私口径已于 2026-11-10 由维护者**显式覆盖**：
	// 原断言「解析层不产出个人电话/家庭电话/家庭住址」作废，
	// 这些字段现在要求落库（见 issue #29 的字段清单）。
	//
	// 仍然禁止的是**身份证**——但原因不是隐私，而是教务档案页根本没有这个字段；
	// 身份证的唯一来源是 ywtb 的 getUserInfo（见 sso.go 的 FetchYWTBIdentity）。
	// 若有一天有人在 Academic 里加了 IDCard，说明来源搞混了，应当拦下。
	typ := reflect.TypeOf(got)
	for _, forbidden := range []string{"IDCard", "IdCard"} {
		if _, ok := typ.FieldByName(forbidden); ok {
			t.Fatalf("Academic 不应有字段 %s：身份证只能来自 ywtb，不在教务档案页", forbidden)
		}
	}
	// 反向确认：这三个字段**应当存在**（维护者要求落库）。
	for _, required := range []string{"PersonalPhone", "HomePhone", "HomeAddress"} {
		if _, ok := typ.FieldByName(required); !ok {
			t.Fatalf("Academic 应有字段 %s（维护者要求落库）", required)
		}
	}
}

// 列顺序被打乱也要能解析（关键字匹配，不依赖索引）。
func TestParseAcademic_ColumnOrderIndependent(t *testing.T) {
	shuffled := `<table class="tablebody">
<tr class="g_column"><td>姓名</td><td>学号</td><td>专业</td><td>年级</td><td>班级</td><td>系别</td></tr>
<tr class="g_body_2"><td>李四</td><td>202502205</td><td>计算机科学与技术</td><td>2024</td><td>计科202401</td><td>信息工程学院</td></tr>
</table>`
	got, err := schoolauth.ParseAcademic([]byte(shuffled))
	if err != nil {
		t.Fatalf("ParseAcademic: %v", err)
	}
	if got.Name != "李四" || got.StudentNo != "202502205" {
		t.Fatalf("基本字段不符：%+v", got)
	}
	if got.Major != "计算机科学与技术" || got.Grade != "2024" || got.ClassName != "计科202401" {
		t.Fatalf("乱序后字段错位：%+v", got)
	}
}

// 登录超时的页面：HTTP 200 但没有数据表 → 必须判失败，不能返回空值当成「没数据」。
func TestParseAcademic_LoginExpiredPageFails(t *testing.T) {
	expired := `<html><head><title>教务管理系统</title></head><body>
<div>登录超时，请重新登录</div>
</body></html>`
	if _, err := schoolauth.ParseAcademic([]byte(expired)); err == nil {
		t.Fatal("登录超时页面必须判失败（否则会被当成「没抓到」而静默通过）")
	}
}

// 完全没有 tablebody → 同样判失败。
func TestParseAcademic_NoTableFails(t *testing.T) {
	if _, err := schoolauth.ParseAcademic([]byte("<html><body>空</body></html>")); err == nil {
		t.Fatal("无表格应报错")
	}
}

// GBK 字节流：解析前必须先按 GBK 解码，否则中文全是乱码。
func TestParseAcademic_GBKBytes(t *testing.T) {
	// 用 GBK 编码构造同一个页面（部分字段），验证按 GBK 解码能出中文。
	page := `<table class="tablebody">
<tr class="g_column"><td>学号</td><td>姓名</td><td>专业</td></tr>
<tr class="g_body_2"><td>202500001</td><td>张三</td><td>数据科学与大数据技术</td></tr>
</table>`
	enc, err := encodeGBK(page)
	if err != nil {
		t.Fatalf("GBK 编码失败：%v", err)
	}
	got, err := schoolauth.ParseAcademic(enc)
	if err != nil {
		t.Fatalf("ParseAcademic(GBK): %v", err)
	}
	if got.Name != "张三" {
		t.Fatalf("GBK 解码后姓名应为张三，实际 %q", got.Name)
	}
	if got.Major != "数据科学与大数据技术" {
		t.Fatalf("GBK 解码后专业不符，实际 %q", got.Major)
	}
}

// 全角/半角括注都要能清洗。
func TestParseAcademic_CleansParentheses(t *testing.T) {
	cases := []struct{ in, want string }{
		{"数据科学与大数据技术（工学）", "数据科学与大数据技术"},
		{"数据科学与大数据技术(工学)", "数据科学与大数据技术"},
		{"  数据科学与大数据技术  ", "数据科学与大数据技术"},
		{"无括注", "无括注"},
	}
	for _, c := range cases {
		if got := schoolauth.CleanValue(c.in); got != c.want {
			t.Fatalf("CleanValue(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// 「新专业」与「专业」同时存在时要明确取哪个（实测两者相同，但别假设）。
func TestParseAcademic_PrefersNewMajorWhenPresent(t *testing.T) {
	page := `<table class="tablebody">
<tr class="g_column"><td>学号</td><td>姓名</td><td>专业</td><td>新专业</td><td>班级</td><td>新班级</td></tr>
<tr class="g_body_2"><td>202500001</td><td>张三</td><td>旧专业</td><td>新专业X</td><td>旧班级</td><td>新班级Y</td></tr>
</table>`
	got, err := schoolauth.ParseAcademic([]byte(page))
	if err != nil {
		t.Fatalf("ParseAcademic: %v", err)
	}
	// 实测两者相同，所以取哪个都一样；但必须**确定性地**取一个，不能随列序变。
	if got.Major != "旧专业" && got.Major != "新专业X" {
		t.Fatalf("专业取值不明确：%q", got.Major)
	}
	if got.ClassName != "旧班级" && got.ClassName != "新班级Y" {
		t.Fatalf("班级取值不明确：%q", got.ClassName)
	}
}

var _ = strings.TrimSpace

// ---- from academic_fields_test.go ----
// #29：bjiben.asp 的 21 个字段都要能解析出来并落库。
// 本文件验证**解析层**：给定页面 HTML，Academic 要带出全部字段。
//
// 表头来自 2026-11-09 真实页面实测：
// 考生号 | 学号 | 姓名 | 性别 | 系别 | 专业 | 学制 | 培养层次 |
// 年级 | 班级 | 新专业 | 新班级 | 学籍状态 | 入学日期 | 民族 |
// 政治面貌 | 家庭通讯地址 | 火车站点 | 个人电话 | 家庭电话 | 特长

// buildBjibenPage 用实测表头构造一个最小可解析页面。
func buildBjibenPage(headers []string, values []string) []byte {
	var hb, vb strings.Builder
	for _, h := range headers {
		hb.WriteString("<td>" + h + "</td>")
	}
	for _, v := range values {
		vb.WriteString("<td>" + v + "</td>")
	}
	return []byte("<table class=\"tablebody\"><tr class=\"g_column\">" + hb.String() +
		"</tr><tr class=\"g_body_2\">" + vb.String() + "</tr></table>")
}

func TestParseAcademic_AllBjibenFields(t *testing.T) {
	headers := []string{
		"考生号", "学号", "姓名", "性别", "系别", "专业", "学制", "培养层次",
		"年级", "班级", "新专业", "新班级", "学籍状态", "入学日期", "民族",
		"政治面貌", "家庭通讯地址", "火车站点", "个人电话", "家庭电话", "特长",
	}
	values := []string{
		"2511010101", "202500001", "张三", "男", "信息工程学院", "数据科学与大数据技术",
		"四年", "本科", "2025", "大数据202503", "数据科学与大数据技术", "大数据202503",
		"在读", "2025-09-01", "汉族", "群众", "乌鲁木齐", "乌鲁木齐站",
		"13800138000", "0991-0000000", "编程",
	}

	ac, err := schoolauth.ParseAcademic(buildBjibenPage(headers, values))
	if err != nil {
		t.Fatalf("ParseAcademic: %v", err)
	}

	checks := map[string][2]string{
		"ExamNo":           {ac.ExamNo, "2511010101"},
		"StudentNo":        {ac.StudentNo, "202500001"},
		"Name":             {ac.Name, "张三"},
		"Gender":           {ac.Gender, "男"},
		"College":          {ac.College, "信息工程学院"},
		"Major":            {ac.Major, "数据科学与大数据技术"},
		"StudyDuration":    {ac.StudyDuration, "四年"},
		"EducationLevel":   {ac.EducationLevel, "本科"},
		"Grade":            {ac.Grade, "2025"},
		"ClassName":        {ac.ClassName, "大数据202503"},
		"NewMajor":         {ac.NewMajor, "数据科学与大数据技术"},
		"NewClassName":     {ac.NewClassName, "大数据202503"},
		"EnrollmentStatus": {ac.EnrollmentStatus, "在读"},
		"EnrollmentDate":   {ac.EnrollmentDate, "2025-09-01"},
		"Nation":           {ac.Nation, "汉族"},
		"PoliticalStatus":  {ac.PoliticalStatus, "群众"},
		"HomeAddress":      {ac.HomeAddress, "乌鲁木齐"},
		"TrainStation":     {ac.TrainStation, "乌鲁木齐站"},
		"PersonalPhone":    {ac.PersonalPhone, "13800138000"},
		"HomePhone":        {ac.HomePhone, "0991-0000000"},
		"Specialty":        {ac.Specialty, "编程"},
	}
	for name, pair := range checks {
		if pair[0] != pair[1] {
			t.Errorf("%s 解析错误：期望 %q，实际 %q", name, pair[1], pair[0])
		}
	}
}

// 「新专业」含「专业」：必须取到各自正确的值，不能串。
func TestParseAcademic_NewMajorDoesNotShadowMajor(t *testing.T) {
	headers := []string{"学号", "姓名", "专业", "新专业"}
	values := []string{"202500001", "张三", "老专业名", "新专业名"}
	ac, err := schoolauth.ParseAcademic(buildBjibenPage(headers, values))
	if err != nil {
		t.Fatalf("ParseAcademic: %v", err)
	}
	if ac.Major != "老专业名" {
		t.Fatalf("Major 被新专业串值：%q", ac.Major)
	}
	if ac.NewMajor != "新专业名" {
		t.Fatalf("NewMajor 错误：%q", ac.NewMajor)
	}
}

// ---- from adapter_test.go ----
// ---- from adapter_test.go ----
// 适配器的完整行为测试。全部用假源，不依赖真实学校环境。

type fakeAuth struct {
	raw schoolauth.RawIdentity
	err error
	// gotCookie 记录被传入的 cookie 头，用于断言「凭据确实传下去了」。
	gotCookie string
	calls     atomic.Int32
}

func (f *fakeAuth) FetchIdentity(_ context.Context, cred schoolauth.Credential) (schoolauth.RawIdentity, error) {
	f.calls.Add(1)
	f.gotCookie = cred.CookieHeader()
	if f.err != nil {
		return schoolauth.RawIdentity{}, f.err
	}
	return f.raw, nil
}

type fakeAcademic struct {
	raw   schoolauth.RawAcademic
	err   error
	delay time.Duration
}

func (f *fakeAcademic) FetchAcademic(ctx context.Context, _ schoolauth.Credential) (schoolauth.RawAcademic, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return schoolauth.RawAcademic{}, ctx.Err()
		}
	}
	if f.err != nil {
		return schoolauth.RawAcademic{}, f.err
	}
	return f.raw, nil
}

type testLogger struct {
	warns []string
}

func (l *testLogger) Warn(msg string, args ...any) {
	l.warns = append(l.warns, msg)
}

func cred(cookies ...string) schoolauth.Credential {
	c := schoolauth.Credential{}
	for _, kv := range cookies {
		parts := strings.SplitN(kv, "=", 2)
		c.Cookies = append(c.Cookies, schoolauth.SessionCookie{Name: parts[0], Value: parts[1]})
	}
	return c
}

// 正常路径：认证身份 + 教务学籍 合并成完整画像。
func TestAuthenticate_MergesTwoSources(t *testing.T) {
	fa := &fakeAuth{raw: schoolauth.RawIdentity{
		StudentNo: "202500001", Name: "张三",
		College: "信息工程学院", IDCardMasked: "650****2614",
	}}
	fac := &fakeAcademic{raw: schoolauth.RawAcademic{
		Major: "数据科学与大数据技术", ClassName: "大数据202503", Grade: "2025",
	}}
	a, err := schoolauth.New(schoolauth.Options{Auth: fa, Academic: fac})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ident, err := a.Authenticate(context.Background(), cred("go-admin-tgt=TGT1", "JSESSIONID=S1"))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if ident.StudentNo != "202500001" || ident.Name != "张三" {
		t.Fatalf("认证字段不符：%+v", ident)
	}
	if ident.College != "信息工程学院" {
		t.Fatalf("学院应取自所属单位，实际 %q", ident.College)
	}
	if ident.IDCardSuffix4 != "2614" {
		t.Fatalf("证件号后四位应为 2614，实际 %q", ident.IDCardSuffix4)
	}
	if ident.Major != "数据科学与大数据技术" || ident.ClassName != "大数据202503" || ident.Grade != "2025" {
		t.Fatalf("教务字段不符：%+v", ident)
	}
	if ident.ProfileIncomplete {
		t.Fatal("两源都成功时不应标记不完整")
	}
	// 整套 cookie 必须传下去（单枚 go-admin-tgt 不够）
	if !strings.Contains(fa.gotCookie, "go-admin-tgt=TGT1") ||
		!strings.Contains(fa.gotCookie, "JSESSIONID=S1") {
		t.Fatalf("应把整套 cookie 传给认证源，实际 %q", fa.gotCookie)
	}
}

// 教务抓取失败：登录仍成功，标记不完整，学院仍保留。
func TestAuthenticate_AcademicFailureMarksIncomplete(t *testing.T) {
	fa := &fakeAuth{raw: schoolauth.RawIdentity{
		StudentNo: "202500001", Name: "张三", College: "信息工程学院", IDCardMasked: "650****2614",
	}}
	fac := &fakeAcademic{err: errors.New("教务超时")}
	log := &testLogger{}
	a, err := schoolauth.New(schoolauth.Options{Auth: fa, Academic: fac, Logger: log})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ident, err := a.Authenticate(context.Background(), cred("go-admin-tgt=TGT1"))
	if err != nil {
		t.Fatalf("教务失败不应阻断登录，实际 %v", err)
	}
	if !ident.ProfileIncomplete {
		t.Fatal("教务失败应标记画像不完整")
	}
	if ident.College != "信息工程学院" {
		t.Fatalf("学院仍应保留，实际 %q", ident.College)
	}
	if ident.Major != "" || ident.ClassName != "" || ident.Grade != "" {
		t.Fatalf("教务字段应为空：%+v", ident)
	}
	if len(log.warns) == 0 {
		t.Fatal("应记录一条告警")
	}
	// 告警里不得含 cookie 值
	for _, w := range log.warns {
		if strings.Contains(w, "TGT1") {
			t.Fatal("日志不得包含 cookie 值")
		}
	}
}

// 教务抓取超时：按不完整处理，且不会把登录拖住。
func TestAuthenticate_AcademicTimeout(t *testing.T) {
	fa := &fakeAuth{raw: schoolauth.RawIdentity{StudentNo: "202500001", Name: "张三"}}
	fac := &fakeAcademic{delay: 2 * time.Second, raw: schoolauth.RawAcademic{Major: "x"}}
	a, err := schoolauth.New(schoolauth.Options{
		Auth: fa, Academic: fac, AcademicTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	start := time.Now()
	ident, err := a.Authenticate(context.Background(), cred("go-admin-tgt=TGT1"))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("超时不应报错：%v", err)
	}
	if !ident.ProfileIncomplete {
		t.Fatal("超时应标记不完整")
	}
	if elapsed > time.Second {
		t.Fatalf("超时应在 50ms 左右返回，实际 %v", elapsed)
	}
}

// 认证失败：返回 ErrAuthFailed。
func TestAuthenticate_AuthFailure(t *testing.T) {
	fa := &fakeAuth{err: errors.New("401")}
	a, _ := schoolauth.New(schoolauth.Options{Auth: fa})
	_, err := a.Authenticate(context.Background(), cred("go-admin-tgt=BAD"))
	if !errors.Is(err, schoolauth.ErrAuthFailed) {
		t.Fatalf("应返回 ErrAuthFailed，实际 %v", err)
	}
}

// 认证源没给学号：等同认证失败（没有学号就无法确定是谁）。
func TestAuthenticate_MissingStudentNoIsFailure(t *testing.T) {
	fa := &fakeAuth{raw: schoolauth.RawIdentity{Name: "张三"}}
	a, _ := schoolauth.New(schoolauth.Options{Auth: fa})
	_, err := a.Authenticate(context.Background(), cred("go-admin-tgt=TGT1"))
	if !errors.Is(err, schoolauth.ErrAuthFailed) {
		t.Fatalf("缺学号应 ErrAuthFailed，实际 %v", err)
	}
}

// 空凭据：直接拒绝，且**不去调用认证源**（省一次无意义的出站请求）。
func TestAuthenticate_EmptyCredential(t *testing.T) {
	fa := &fakeAuth{raw: schoolauth.RawIdentity{StudentNo: "1"}}
	a, _ := schoolauth.New(schoolauth.Options{Auth: fa})
	_, err := a.Authenticate(context.Background(), schoolauth.Credential{})
	if !errors.Is(err, schoolauth.ErrAuthFailed) {
		t.Fatalf("空凭据应 ErrAuthFailed，实际 %v", err)
	}
	if fa.calls.Load() != 0 {
		t.Fatal("空凭据不应调用认证源")
	}
}

// ── 掩码解析 ──────────────────────────────────────────────

func TestSuffix4FromMasked(t *testing.T) {
	cases := []struct{ in, want string }{
		{"650****2614", "2614"},        // 实测页面形态
		{"510101200501011234", "1234"}, // 完整号也给后四位
		{"", ""},
		{"abc", ""},               // 无数字
		{"****", ""},              // 只有掩码
		{"650****261", ""},        // 末尾不足四位
		{"650****26a4", ""},       // 末四位必须是数字
		{" 650****2614 ", "2614"}, // 前后空白
	}
	for _, c := range cases {
		if got := schoolauth.Suffix4FromMasked(c.in); got != c.want {
			t.Fatalf("Suffix4FromMasked(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// ── 凭据的日志安全 ────────────────────────────────────────

func TestCredentialStringHidesValues(t *testing.T) {
	c := cred("go-admin-tgt=SECRET-TGT", "JSESSIONID=SECRET-SID")
	s := c.String()
	if strings.Contains(s, "SECRET-TGT") || strings.Contains(s, "SECRET-SID") {
		t.Fatalf("String() 不得泄露 cookie 值，实际 %q", s)
	}
	if !strings.Contains(s, "go-admin-tgt") {
		t.Fatalf("应显示 cookie 名以便排障，实际 %q", s)
	}
}

func TestCredentialCookieHeader(t *testing.T) {
	c := cred("a=1", "b=2")
	if got := c.CookieHeader(); got != "a=1; b=2" {
		t.Fatalf("CookieHeader 不符：%q", got)
	}
	// RawCookie 优先
	c2 := schoolauth.Credential{RawCookie: "x=9", Cookies: []schoolauth.SessionCookie{{Name: "a", Value: "1"}}}
	if got := c2.CookieHeader(); got != "x=9" {
		t.Fatalf("RawCookie 应优先，实际 %q", got)
	}
}

func TestCredentialEmpty(t *testing.T) {
	if !(schoolauth.Credential{}).Empty() {
		t.Fatal("空凭据应判为 Empty")
	}
	if (schoolauth.Credential{RawCookie: "  "}).Empty() != true {
		t.Fatal("纯空白应判为 Empty")
	}
	if cred("a=1").Empty() {
		t.Fatal("有 cookie 不应判为 Empty")
	}
}

// 没有教务源时：认证成功但标记不完整（而不是假装完整）。
func TestAuthenticate_NoAcademicSource(t *testing.T) {
	fa := &fakeAuth{raw: schoolauth.RawIdentity{StudentNo: "202500001", College: "信息工程学院"}}
	a, _ := schoolauth.New(schoolauth.Options{Auth: fa})
	ident, err := a.Authenticate(context.Background(), cred("go-admin-tgt=TGT1"))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if !ident.ProfileIncomplete {
		t.Fatal("没有教务源时应标记不完整")
	}
}

// 教务抓到了但字段全空：同样视为不完整。
func TestAuthenticate_EmptyAcademicMarksIncomplete(t *testing.T) {
	fa := &fakeAuth{raw: schoolauth.RawIdentity{StudentNo: "202500001"}}
	fac := &fakeAcademic{raw: schoolauth.RawAcademic{}}
	a, _ := schoolauth.New(schoolauth.Options{Auth: fa, Academic: fac})
	ident, err := a.Authenticate(context.Background(), cred("go-admin-tgt=TGT1"))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if !ident.ProfileIncomplete {
		t.Fatal("教务字段全空应标记不完整")
	}
}

func TestNew_RequiresAuthSource(t *testing.T) {
	if _, err := schoolauth.New(schoolauth.Options{}); err == nil {
		t.Fatal("缺 AuthSource 应报错（依赖缺失在构造期暴露，不留到运行期）")
	}
}

// ---- from bridge_test.go ----
// 桥接层：把适配器结果映射成身份域形状，字段必须一一对应。
func TestIdentity_AsDomain(t *testing.T) {
	i := schoolauth.Identity{
		StudentNo: "202500001", Name: "张三",
		College: "信息工程学院", IDCardSuffix4: "2614",
		Major: "数据科学与大数据技术", ClassName: "大数据202503", Grade: "2025",
		ProfileIncomplete: true,
	}
	d := i.AsDomain()
	if d.StudentNo != i.StudentNo || d.Name != i.Name || d.College != i.College {
		t.Fatalf("基础字段未映射：%+v", d)
	}
	if d.IDCardSuffix4 != "2614" {
		t.Fatalf("后四位未映射：%q", d.IDCardSuffix4)
	}
	if d.Major != i.Major || d.ClassName != i.ClassName || d.Grade != i.Grade {
		t.Fatalf("教务字段未映射：%+v", d)
	}
	if !d.ProfileIncomplete {
		t.Fatal("不完整标记未映射")
	}
}

// 零值映射不应 panic，也不应凭空造出字段。
func TestIdentity_AsDomainZeroValue(t *testing.T) {
	d := (schoolauth.Identity{}).AsDomain()
	if d.StudentNo != "" || d.ProfileIncomplete {
		t.Fatalf("零值应保持零值，实际 %+v", d)
	}
}

// ---- from helpers_test.go ----
// 测试辅助：base64/hex 转换与 io.ReadCloser 包装。

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex 解码失败 %q：%v", s, err)
	}
	return b
}

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("base64 解码失败：%v", err)
	}
	return b
}

func b64Of(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

type nopCloser struct{ io.Reader }

func (nopCloser) Close() error { return nil }

func ioNopCloser(s string) io.ReadCloser { return nopCloser{strings.NewReader(s)} }

// encodeGBK 把字符串按 GBK 编码，用于构造真实的 GBK 页面样本。
func encodeGBK(s string) ([]byte, error) {
	enc := simplifiedchinese.GBK.NewEncoder()
	return enc.Bytes([]byte(s))
}
