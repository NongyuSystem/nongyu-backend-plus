package schoolauth_test

import (
	"testing"

	"nongyu-backend-plus/internal/school/schoolauth"
)

// 桥接层：把适配器结果映射成身份域形状，字段必须一一对应。
func TestIdentity_AsDomain(t *testing.T) {
	i := schoolauth.Identity{
		StudentNo: "202502204", Name: "姚一飞",
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
