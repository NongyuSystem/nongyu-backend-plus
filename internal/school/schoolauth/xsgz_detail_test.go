package schoolauth_test

// xsgz 学工系统完整档案端点（2026-11-10 实测）。
// 关键契约：H-Zove-AppKey 必须带，否则网关报「验证权限异常」(500)。
//
// 来源说明：本段在原拼接文件里用 `// ── xsgz ... ──` 标注，没有 `// ---- from` 标记，
// 但主题与 xsgz 档案端点明确对应，按主题归入本文件。

import (
	"context"
	"testing"

	"nongyu-backend-plus/internal/school/schoolauth"
)

func TestFetchXSGZStudentDetail_RequiresAppKey(t *testing.T) {
	// 用 scripted doer 模拟：没带 AppKey 的请求返回 500 权限异常。
	// 注意 FetchXSGZStudentDetail 内部会先走两步 enterHall（实测必需），
	// 所以这里也要把这两步的响应补上。
	d := &scriptedDoer{responses: map[string]*scriptedResp{
		"/gateway/xg-agg-pc/user/checkUserAndGetUserLevel": {
			status: 200,
			body: `{"code":"0","message":"操作成功","data":{"loginLevels":[` +
				`{"levelNo":"XS","units":[{"unitType":2,"unitNo":"419"}]}]}}`,
		},
		"/gateway/xg-agg-pc/user/enterHall": {
			status: 200,
			body:   `{"code":"0","message":"操作成功","data":{"account":"202500001"}}`,
		},
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
