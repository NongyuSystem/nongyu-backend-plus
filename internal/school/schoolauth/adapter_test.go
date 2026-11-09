package schoolauth_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nongyu-backend-plus/internal/school/schoolauth"
)

// 适配器的完整行为测试。全部用假源，不依赖真实学校环境。

type fakeAuth struct {
	raw schoolauth.RawIdentity
	err error
	// gotCookie 记录被传入的 cookie 头，用于断言「凭据确实传下去了」。
	gotCookie string
	calls     atomic.Int32
}

func (f *fakeAuth) FetchIdentity(_ context.Context, cookieHeader string) (schoolauth.RawIdentity, error) {
	f.calls.Add(1)
	f.gotCookie = cookieHeader
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

func (f *fakeAcademic) FetchAcademic(ctx context.Context, _ string) (schoolauth.RawAcademic, error) {
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
		StudentNo: "202502204", Name: "姚一飞",
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
	if ident.StudentNo != "202502204" || ident.Name != "姚一飞" {
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
		StudentNo: "202502204", Name: "姚一飞", College: "信息工程学院", IDCardMasked: "650****2614",
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
	fa := &fakeAuth{raw: schoolauth.RawIdentity{StudentNo: "202502204", Name: "姚一飞"}}
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
	fa := &fakeAuth{raw: schoolauth.RawIdentity{Name: "姚一飞"}}
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
	fa := &fakeAuth{raw: schoolauth.RawIdentity{StudentNo: "202502204", College: "信息工程学院"}}
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
	fa := &fakeAuth{raw: schoolauth.RawIdentity{StudentNo: "202502204"}}
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
