package schoolauth_test

// #28：取身份只走「用 go-admin-tgt 换票」这一条路。
//
// 实测依据（docs/tech/实测-川农CAS真实链路-tgt万能凭据.md）：
// 换票响应明文里已带 user.number / user.name，
// 之前那条要求 Bearer 的 getUserInfo 路径已按维护者指示删除。
//
// seam：YWTBSource.FetchIdentity（公共接口），用 scripted doer 模拟换票响应。

import (
	"context"
	"strings"
	"testing"

	"nongyu-backend-plus/internal/school/schoolauth"
)

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
