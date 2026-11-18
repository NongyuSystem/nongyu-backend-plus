package schoolauth_test

// xsgz 落库链路守卫（2026-11-10）。
//
// 为什么值得单独守卫：曾经出现过「数据能取到、但没有任何调用方把它落库」
// 的缺口——FetchDetail 写好了却没人调，日志里一切正常，数据库却是空的。
// 这两条测试锁住：① detail 的字段确实被映射进 ToProfileData；
// ② XSGZSource 取到 detail 后确实触发了 OnDetail（落库入口）。
//
// 来源说明：本段在原拼接文件里用 `// ── xsgz ... ──` 标注，没有 `// ---- from` 标记，
// 按主题归入本文件。

import (
	"context"
	"encoding/json"
	"testing"

	"nongyu-backend-plus/internal/school/schoolauth"
)

func TestXSGZDetail_ToProfileDataMapsFields(t *testing.T) {
	raw := json.RawMessage(`{
		"baseDto":{"studentNo":"202500001","studentName":"张三","sexName":"男",
			"nationalityName":"汉族","idCardNo":"110101200001011234","birthday":"2000-01-01","photo":"p.jpg"},
		"studentStatusDto":{"collegeNo":"419","collegeName":"信息工程学院","specialtyNo":"080910T",
			"specialtyName":"数据科学与大数据技术","grade":"2025","classNo":"C1","className":"大数据202503",
			"campusName":"雅安校区","dormAddress":"y22B313","studyYears":"4","inSchoolDate":"2025-09-10",
			"studentStateName":"在籍在校","currentStateName":"在读"},
		"examDto":{"examineeNo":"E123","highSchool":"某中学","sourceLocation":"成都"},
		"contactInfoDto":{"email":"a@b.com","mobilePhone":"13800138000","wechat":"wx","qq":"123"},
		"emergencyContactInfoDto":{"emergencyContact":"李四","emergencyContactRelationship":"母子",
			"emergencyContactTelephone":"13900139000","emergencyContactAddress":"某地址"},
		"familyDto":{"familyAddress":"家庭地址","familyDetailAddress":"详细","familyTelephone":"028","familyPostalNo":"610000"},
		"personalInfoDto":{"namePinyin":"zhangsan","bloodTypeName":"A型","height":180,"weight":70,"nativePlace":"成都"},
		"familyMemberList":[{"name":"李四","relationshipName":"母子"},{"name":"王五","relationshipName":"父子"}],
		"resumeList":[{"studyUnit":"某小学","position":"小学"}],
		"sociologyMemberList":[],"economyDto":{},"workUnitDto":{},
		"age":25
	}`)
	d, err := schoolauth.ParseXSGZDetail(raw)
	if err != nil {
		t.Fatalf("ParseXSGZDetail: %v", err)
	}
	pd := d.ToProfileData()
	if pd.StudentNo != "202500001" || pd.Fields["studentName"] != "张三" {
		t.Fatalf("基础字段未映射: %+v", pd.Fields)
	}
	// 关键字段逐个抽查（这些是用户清单里明确要的）
	for k, want := range map[string]string{
		"collegeName": "信息工程学院", "specialtyName": "数据科学与大数据技术",
		"className": "大数据202503", "dormAddress": "y22B313",
		"examineeNo": "E123", "familyAddress": "家庭地址",
		"emergencyContact": "李四", "mobilePhone": "13800138000",
		"email": "a@b.com", "nativePlace": "成都",
	} {
		if got := pd.Fields[k]; got != want {
			t.Errorf("字段 %s 应为 %q，实际 %q", k, want, got)
		}
	}
	// number 类型字段（height=180，JSON 里是数字）必须被取成字符串而不是报错
	if pd.Fields["height"] != "180" {
		t.Errorf("height 应取到 \"180\"（number 也能取），实际 %q", pd.Fields["height"])
	}
	// 身份证原值要带出来（上层据此加密）
	if pd.IDCardNo != "110101200001011234" {
		t.Errorf("身份证未带出: %q", pd.IDCardNo)
	}
	// 列表分组要拆成逐条 JSON
	if len(pd.FamilyMembers) != 2 || len(pd.Resumes) != 1 {
		t.Errorf("列表分组拆分错误: members=%d resumes=%d", len(pd.FamilyMembers), len(pd.Resumes))
	}
	// 13 个分组原文必须全部保留（不丢字段）
	if len(pd.Groups) != 13 {
		t.Errorf("分组原文应有 13 组，实际 %d", len(pd.Groups))
	}
}

func TestXSGZSource_TriggersOnDetail(t *testing.T) {
	// 用 scriptedDoer 让换票与 detail 都返回成功，验证 OnDetail 被调用。
	detailBody := `{"code":"0","message":"ok","data":{` +
		`"baseDto":{"studentNo":"202500001","studentName":"张三"},` +
		`"studentStatusDto":{},"examDto":{},"contactInfoDto":{},` +
		`"emergencyContactInfoDto":{},"familyDto":{},"personalInfoDto":{},` +
		`"familyMemberList":[],"resumeList":[],"sociologyMemberList":[],` +
		`"economyDto":{},"workUnitDto":{},"age":20}}`
	d := &scriptedDoer{responses: map[string]*scriptedResp{
		// FetchXSGZStudentDetail 内部会先走两步 enterHall（实测必需）
		"/gateway/xg-agg-pc/user/checkUserAndGetUserLevel": {
			status: 200,
			body: `{"code":"0","message":"操作成功","data":{"loginLevels":[` +
				`{"levelNo":"XS","units":[{"unitType":2,"unitNo":"419"}]}]}}`,
		},
		"/gateway/xg-agg-pc/user/enterHall": {
			status: 200,
			body:   `{"code":"0","message":"操作成功","data":{}}`,
		},
		"/gateway/zovecenter-saip-xg-app-student/student/detail/202500001": {status: 200, body: detailBody},
	}}
	var gotStudentNo string
	src, err := schoolauth.NewXSGZSource(schoolauth.XSGZOptions{
		Doer:      d,
		Exchanger: fakeExchanger{studentNo: "202500001", name: "张三"},
		Token:     "tok",
		OnDetail: func(_ context.Context, det schoolauth.XSGZStudentDetail) error {
			gotStudentNo = det.Typed.StudentNo
			return nil
		},
	})
	if err != nil {
		t.Fatalf("NewXSGZSource: %v", err)
	}
	ident, err := src.FetchIdentity(context.Background(), schoolauth.Credential{RawCookie: "go-admin-tgt=x", XSGZToken: "tok"})
	if err != nil {
		t.Fatalf("FetchIdentity: %v", err)
	}
	if ident.StudentNo != "202500001" {
		t.Fatalf("身份学号错误: %q", ident.StudentNo)
	}
	if gotStudentNo != "202500001" {
		t.Fatalf("OnDetail 未被调用（落库入口断了），收到 %q", gotStudentNo)
	}
}

// fakeExchanger 是 TicketExchanger 的测试替身。
//
// 来源说明：这个类型在原拼接文件里没有来源标记（紧跟在 XSGZSource 用例之后），
// 按主题归入本文件——它是 xsgz 取数用例的配套替身。
type fakeExchanger struct {
	studentNo string
	name      string
}

func (f fakeExchanger) ExchangeForIdentity(_ context.Context, _ schoolauth.Credential) (schoolauth.TicketIdentity, error) {
	return schoolauth.TicketIdentity{StudentNo: f.studentNo, Name: f.name}, nil
}
