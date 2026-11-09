package schoolauth_test

// #29：bjiben.asp 的 21 个字段都要能解析出来并落库。
// 本文件验证**解析层**：给定页面 HTML，Academic 要带出全部字段。
//
// 表头来自 2026-11-09 真实页面实测：
// 考生号 | 学号 | 姓名 | 性别 | 系别 | 专业 | 学制 | 培养层次 |
// 年级 | 班级 | 新专业 | 新班级 | 学籍状态 | 入学日期 | 民族 |
// 政治面貌 | 家庭通讯地址 | 火车站点 | 个人电话 | 家庭电话 | 特长

import (
	"strings"
	"testing"

	"nongyu-backend-plus/internal/school/schoolauth"
)

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
		"2511010101", "202502204", "姚一飞", "男", "信息工程学院", "数据科学与大数据技术",
		"四年", "本科", "2025", "大数据202503", "数据科学与大数据技术", "大数据202503",
		"在读", "2025-09-01", "汉族", "群众", "乌鲁木齐", "乌鲁木齐站",
		"17799611327", "0991-0000000", "编程",
	}

	ac, err := schoolauth.ParseAcademic(buildBjibenPage(headers, values))
	if err != nil {
		t.Fatalf("ParseAcademic: %v", err)
	}

	checks := map[string][2]string{
		"ExamNo":           {ac.ExamNo, "2511010101"},
		"StudentNo":        {ac.StudentNo, "202502204"},
		"Name":             {ac.Name, "姚一飞"},
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
		"PersonalPhone":    {ac.PersonalPhone, "17799611327"},
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
	values := []string{"202502204", "姚一飞", "老专业名", "新专业名"}
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
