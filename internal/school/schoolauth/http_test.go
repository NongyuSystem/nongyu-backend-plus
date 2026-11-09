package schoolauth_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"nongyu-backend-plus/internal/school/schoolauth"
)

// fakeDoer 实现 platform.HTTPDoer，记录收到的请求并按需返回响应。
type fakeDoer struct {
	status int
	body   string
	err    error
	// gotCookie 记录请求头里的 Cookie，用于断言凭据确实带上了。
	gotCookie string
	calls     int
}

func (f *fakeDoer) Do(_ context.Context, req *http.Request) (*http.Response, error) {
	f.calls++
	f.gotCookie = req.Header.Get("Cookie")
	if f.err != nil {
		return nil, f.err
	}
	return &http.Response{
		StatusCode: f.status,
		Body:       io.NopCloser(strings.NewReader(f.body)),
	}, nil
}

func TestHTTPSource_SendsCookieAndParses(t *testing.T) {
	d := &fakeDoer{status: 200, body: "学号:202502204"}
	src, err := schoolauth.NewHTTPSource(schoolauth.HTTPSourceOptions{
		Doer:        d,
		IdentityURL: "https://example.invalid/person",
		ParseIdentity: func(b []byte) (schoolauth.RawIdentity, error) {
			return schoolauth.RawIdentity{StudentNo: "202502204", Name: "姚一飞"}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewHTTPSource: %v", err)
	}

	raw, err := src.FetchIdentity(context.Background(), schoolauth.Credential{RawCookie: "go-admin-tgt=T1; JSESSIONID=S1"})
	if err != nil {
		t.Fatalf("FetchIdentity: %v", err)
	}
	if raw.StudentNo != "202502204" {
		t.Fatalf("解析结果不符：%+v", raw)
	}
	if !strings.Contains(d.gotCookie, "go-admin-tgt=T1") {
		t.Fatalf("应把 cookie 带进请求，实际 %q", d.gotCookie)
	}
}

func TestHTTPSource_Non200IsError(t *testing.T) {
	d := &fakeDoer{status: 401}
	src, _ := schoolauth.NewHTTPSource(schoolauth.HTTPSourceOptions{
		Doer: d, IdentityURL: "https://example.invalid/p",
		ParseIdentity: func([]byte) (schoolauth.RawIdentity, error) {
			return schoolauth.RawIdentity{StudentNo: "1"}, nil
		},
	})
	if _, err := src.FetchIdentity(context.Background(), schoolauth.Credential{RawCookie: "a=1"}); err == nil {
		t.Fatal("非 200 应报错")
	}
}

// 真实坑：学校站在会话失效时常常返回 200 + 登录页。
// 因此解析函数必须校验内容结构，解析失败即视为失败。
func TestHTTPSource_200WithLoginPageIsFailure(t *testing.T) {
	d := &fakeDoer{status: 200, body: "<html>请重新登录</html>"}
	src, _ := schoolauth.NewHTTPSource(schoolauth.HTTPSourceOptions{
		Doer: d, IdentityURL: "https://example.invalid/p",
		ParseIdentity: func(b []byte) (schoolauth.RawIdentity, error) {
			// 解析器认不出学号 → 报错，让上层按认证失败处理。
			if !strings.Contains(string(b), "学号") {
				return schoolauth.RawIdentity{}, errors.New("页面结构不符（疑似已跳转登录页）")
			}
			return schoolauth.RawIdentity{StudentNo: "1"}, nil
		},
	})
	raw, err := src.FetchIdentity(context.Background(), schoolauth.Credential{RawCookie: "a=1"})
	if err == nil {
		t.Fatalf("200 但是登录页时必须判失败，实际返回 %+v", raw)
	}
	// 上层据此转成 ErrAuthFailed
	if _, aerr := mustAuth(t, src); !errors.Is(aerr, schoolauth.ErrAuthFailed) {
		t.Fatalf("应转成 ErrAuthFailed，实际 %v", aerr)
	}
}

// mustAuth 用该源构造适配器并试一次认证，返回错误供断言。
func mustAuth(t *testing.T, src schoolauth.AuthSource) (schoolauth.Identity, error) {
	t.Helper()
	a, err := schoolauth.New(schoolauth.Options{Auth: src})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a.Authenticate(context.Background(), cred("a=1"))
}

func TestHTTPSource_DoerErrorPropagates(t *testing.T) {
	d := &fakeDoer{err: errors.New("网络不可达")}
	src, _ := schoolauth.NewHTTPSource(schoolauth.HTTPSourceOptions{
		Doer: d, IdentityURL: "https://example.invalid/p",
		ParseIdentity: func([]byte) (schoolauth.RawIdentity, error) {
			return schoolauth.RawIdentity{StudentNo: "1"}, nil
		},
	})
	if _, err := src.FetchIdentity(context.Background(), schoolauth.Credential{RawCookie: "a=1"}); err == nil {
		t.Fatal("出站错误应向上抛")
	}
}

func TestHTTPSource_AcademicUnconfigured(t *testing.T) {
	d := &fakeDoer{status: 200}
	src, _ := schoolauth.NewHTTPSource(schoolauth.HTTPSourceOptions{
		Doer: d, IdentityURL: "https://example.invalid/p",
		ParseIdentity: func([]byte) (schoolauth.RawIdentity, error) {
			return schoolauth.RawIdentity{StudentNo: "1"}, nil
		},
	})
	if _, err := src.FetchAcademic(context.Background(), schoolauth.Credential{RawCookie: "a=1"}); err == nil {
		t.Fatal("教务源未配置时应报错（由适配器转成「画像不完整」）")
	}
}

func TestHTTPSource_RequiresParseIdentity(t *testing.T) {
	d := &fakeDoer{status: 200}
	if _, err := schoolauth.NewHTTPSource(schoolauth.HTTPSourceOptions{
		Doer: d, IdentityURL: "https://example.invalid/p",
	}); err == nil {
		t.Fatal("缺解析函数应在构造期报错，不留到运行期")
	}
}
