// Package xsgz 是学工系统（xsgz.sicau.edu.cn）的取数与身份源。
//
// 为什么单独成包：它有一套完整的协议（换 token → 进大厅 → 取档案），
// 与 CAS 换票（auth 域）和教务（jiaowu 域）互不相干。混在一个包里时
// sso.go 涨到 800+ 行、三十个顶层声明，改动时很难看清边界。
//
// 本包**不 import schoolauth**：端口类型（TicketExchanger 等）由调用方
// 在本包定义最小接口（见 Exchanger），避免两个包互相依赖。
//
// 完整链路（2026-11-10 实测，缺任一步都会 500「验证权限异常」）：
//
//	① POST /gateway/xg-agg-pc/cas/login          form: ticket=<st>:<userId>
//	② POST /gateway/xg-agg-pc/user/checkUserAndGetUserLevel
//	③ POST /gateway/xg-agg-pc/user/enterHall     ← 少了这一步必 500
//	④ GET  /gateway/zovecenter-saip-xg-app-student/student/detail/<学号>
package xsgz

import (
	"encoding/json"
	"fmt"
)

// ── 类型 ────────────────────────────────────────────────────────────

// StudentDetail 是学工系统的完整学生档案（一次请求返回 13 个分组）。
//
// 端点：GET /gateway/zovecenter-saip-xg-app-student/student/detail/<学号>
// 覆盖用户清单里几乎所有字段——包括教务档案页和 ywtb 都给不了的身份证号、
// 家庭住址、考生号、紧急联系人、教育经历。
//
// 设计：**每个分组同时保留原始 JSON**（Groups map），上游新增字段不会丢；
// 常用字段通过 Typed 提取。这样既不会因上游加字段而崩，也不用把上百个字段
// 全定义成 Go 结构体。
type StudentDetail struct {
	// Groups 保留 13 个分组的完整原文，key 形如 "baseDto" / "examDto"。
	Groups map[string]json.RawMessage
	// Typed 是把常用字段解出来的便利视图（供上层直接读）。
	Typed Typed
}

// Typed 是从各分组提取出的常用字段（用户清单里的字段优先）。
type Typed struct {
	StudentNo             string
	OneCardNo             string
	StudentName           string
	Sex                   string
	SexName               string
	Nationality           string
	NationalityName       string
	IDCardNo              string // 身份证号（完整，上层立即加密）
	Birthday              string
	Photo                 string
	CollegeNo             string
	CollegeName           string
	SpecialtyNo           string
	SpecialtyName         string
	Grade                 string
	ClassNo               string
	ClassName             string
	CampusName            string
	DormAddress           string
	StudyYears            string
	InSchoolDate          string
	StudentStateName      string
	CurrentStateName      string
	ExamineeNo            string
	HighSchool            string
	SourceLocation        string
	Email                 string
	MobilePhone           string
	Wechat                string
	QQ                    string
	EmergencyContact      string
	EmergencyRelationship string
	EmergencyTelephone    string
	EmergencyAddress      string
	FamilyAddress         string
	FamilyDetailAddress   string
	FamilyTelephone       string
	FamilyPostalNo        string
	NamePinyin            string
	BloodTypeName         string
	Height                string
	Weight                string
	NativePlace           string
	Age                   int
}

// GroupKeys 是实测的 13 个分组名（顺序固定，便于遍历）。
var GroupKeys = []string{
	"baseDto", "studentStatusDto", "personalInfoDto", "contactInfoDto",
	"emergencyContactInfoDto", "examDto", "familyDto", "economyDto",
	"workUnitDto", "familyMemberList", "sociologyMemberList", "resumeList", "age",
}

// ProfileData 是 detail 展开后、可直接交给存储层的纯数据。
type ProfileData struct {
	StudentNo     string
	Fields        map[string]string
	Groups        map[string]string
	FamilyMembers []string
	Resumes       []string
	IDCardNo      string
	Age           int
}

// ── 常量 ────────────────────────────────────────────────────────────

const (
	// host 是学工系统站点。
	host = "xsgz.sicau.edu.cn"
	// detailGateway 是「学生详情」端点所在的子应用网关。
	detailGateway = "/gateway/zovecenter-saip-xg-app-student"
	// aggGateway 是主应用（聚合）网关，登录类接口（enterHall 等）走它。
	aggGateway = "/gateway/xg-agg-pc"
	// aggAppKey 是主应用的 appKey（与子应用的 appKey 不同）。
	aggAppKey = "zovecenter-saip-xg-agg-pc"
	// appKey 是子应用的 appKey。
	appKey = "zovecenter-saip-xg-app-student"
	// tenant 是租户标识。
	tenant = "XG"
	// maxBody 是响应体上限。
	maxBody = 1 << 20
	// goAdminTgtCookie 是统一身份认证下发的登录态 cookie 名。
	goAdminTgtCookie = "go-admin-tgt"
)

// ── 解析 ────────────────────────────────────────────────────────────

// strField 从分组原文里安全取字符串字段：字段不存在、null、或为 number 时
// 都能取到字符串表示（number 用其字面量），绝不因类型不匹配报错。
func strField(g json.RawMessage, key string) string {
	if len(g) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(g, &m) != nil {
		return ""
	}
	raw, ok := m[key]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	// 字符串直接去引号；其它类型（number/bool）用原始字面量
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	return string(raw)
}

// ParseDetail 解析 detail 端点的 data 段（供落库/测试复用）。
func ParseDetail(data json.RawMessage) (StudentDetail, error) {
	return parseDetail(data)
}

// parseDetail 把网关 data 解成 StudentDetail（分组原文 + 常用字段视图）。
func parseDetail(data json.RawMessage) (StudentDetail, error) {
	var groups map[string]json.RawMessage
	if err := json.Unmarshal(data, &groups); err != nil {
		return StudentDetail{}, fmt.Errorf("解析 data 分组失败：%w", err)
	}
	d := StudentDetail{Groups: groups}
	g := func(k string) json.RawMessage { return groups[k] }

	base, status, personal := g("baseDto"), g("studentStatusDto"), g("personalInfoDto")
	contact, emergency, exam := g("contactInfoDto"), g("emergencyContactInfoDto"), g("examDto")
	family := g("familyDto")

	t := Typed{
		StudentNo:             strField(base, "studentNo"),
		OneCardNo:             strField(base, "oneCardNo"),
		StudentName:           strField(base, "studentName"),
		Sex:                   strField(base, "sex"),
		SexName:               strField(base, "sexName"),
		Nationality:           strField(base, "nationality"),
		NationalityName:       strField(base, "nationalityName"),
		IDCardNo:              strField(base, "idCardNo"),
		Birthday:              strField(base, "birthday"),
		Photo:                 strField(base, "photo"),
		CollegeNo:             strField(status, "collegeNo"),
		CollegeName:           strField(status, "collegeName"),
		SpecialtyNo:           strField(status, "specialtyNo"),
		SpecialtyName:         strField(status, "specialtyName"),
		Grade:                 strField(status, "grade"),
		ClassNo:               strField(status, "classNo"),
		ClassName:             strField(status, "className"),
		CampusName:            strField(status, "campusName"),
		DormAddress:           strField(status, "dormAddress"),
		StudyYears:            strField(status, "studyYears"),
		InSchoolDate:          strField(status, "inSchoolDate"),
		StudentStateName:      strField(status, "studentStateName"),
		CurrentStateName:      strField(status, "currentStateName"),
		ExamineeNo:            strField(exam, "examineeNo"),
		HighSchool:            strField(exam, "highSchool"),
		SourceLocation:        strField(exam, "sourceLocation"),
		Email:                 strField(contact, "email"),
		MobilePhone:           strField(contact, "mobilePhone"),
		Wechat:                strField(contact, "wechat"),
		QQ:                    strField(contact, "qq"),
		FamilyAddress:         strField(family, "familyAddress"),
		FamilyDetailAddress:   strField(family, "familyDetailAddress"),
		FamilyTelephone:       strField(family, "familyTelephone"),
		FamilyPostalNo:        strField(family, "familyPostalNo"),
		EmergencyContact:      strField(emergency, "emergencyContact"),
		EmergencyRelationship: strField(emergency, "emergencyContactRelationship"),
		EmergencyTelephone:    strField(emergency, "emergencyContactTelephone"),
		EmergencyAddress:      strField(emergency, "emergencyContactAddress"),
		NamePinyin:            strField(personal, "namePinyin"),
		BloodTypeName:         strField(personal, "bloodTypeName"),
		Height:                strField(personal, "height"),
		Weight:                strField(personal, "weight"),
		NativePlace:           strField(personal, "nativePlace"),
	}
	if a := strField(groups["age"], ""); a == "" {
		var age int
		if json.Unmarshal(groups["age"], &age) == nil {
			t.Age = age
		}
	}
	d.Typed = t
	if t.StudentNo == "" {
		return StudentDetail{}, fmt.Errorf("响应缺少学号（baseDto.studentNo）")
	}
	return d, nil
}

// ToProfileData 把 detail 展开成落库用的纯数据。
func (d StudentDetail) ToProfileData() ProfileData {
	t := d.Typed
	fields := map[string]string{
		"oneCardNo": t.OneCardNo, "studentName": t.StudentName,
		"sex": t.Sex, "sexName": t.SexName,
		"nationality": t.Nationality, "nationalityName": t.NationalityName,
		"birthday": t.Birthday, "photo": t.Photo,
		"collegeNo": t.CollegeNo, "collegeName": t.CollegeName,
		"specialtyNo": t.SpecialtyNo, "specialtyName": t.SpecialtyName,
		"grade": t.Grade, "classNo": t.ClassNo, "className": t.ClassName,
		"campusName": t.CampusName, "dormAddress": t.DormAddress,
		"studyYears": t.StudyYears, "inSchoolDate": t.InSchoolDate,
		"studentStateName": t.StudentStateName, "currentStateName": t.CurrentStateName,
		"examineeNo": t.ExamineeNo, "highSchool": t.HighSchool,
		"sourceLocation": t.SourceLocation,
		"email":          t.Email, "mobilePhone": t.MobilePhone,
		"wechat": t.Wechat, "qq": t.QQ,
		"emergencyContact":             t.EmergencyContact,
		"emergencyContactRelationship": t.EmergencyRelationship,
		"emergencyContactTelephone":    t.EmergencyTelephone,
		"emergencyContactAddress":      t.EmergencyAddress,
		"familyAddress":                t.FamilyAddress,
		"familyDetailAddress":          t.FamilyDetailAddress,
		"familyTelephone":              t.FamilyTelephone, "familyPostalNo": t.FamilyPostalNo,
		"namePinyin": t.NamePinyin, "bloodTypeName": t.BloodTypeName,
		"height": t.Height, "weight": t.Weight, "nativePlace": t.NativePlace,
	}
	groups := make(map[string]string, len(d.Groups))
	for k, v := range d.Groups {
		groups[k] = string(v)
	}
	var members, resumes []string
	var arr []json.RawMessage
	if json.Unmarshal(d.Groups["familyMemberList"], &arr) == nil {
		for _, v := range arr {
			members = append(members, string(v))
		}
	}
	arr = nil
	if json.Unmarshal(d.Groups["resumeList"], &arr) == nil {
		for _, v := range arr {
			resumes = append(resumes, string(v))
		}
	}
	return ProfileData{
		StudentNo: t.StudentNo, Fields: fields, Groups: groups,
		FamilyMembers: members, Resumes: resumes,
		IDCardNo: t.IDCardNo, Age: t.Age,
	}
}
